package gortmplib_test

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortmplib/pkg/message"
)

var testH264SPS720 = []byte{ // 1280x720
	0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50,
	0x05, 0xbb, 0x01, 0x6c, 0x80, 0x00, 0x00, 0x03,
	0x00, 0x80, 0x00, 0x00, 0x1e, 0x07, 0x8c, 0x18,
	0xcb,
}

var testH264PPS720 = []byte{0x68, 0xee, 0x3c, 0x80}

var testH265SPS720 = []byte{ // 1280x720
	0x42, 0x01, 0x01, 0x04, 0x08, 0x00, 0x00, 0x03,
	0x00, 0x98, 0x08, 0x00, 0x00, 0x03, 0x00, 0x00,
	0x5d, 0x90, 0x00, 0x50, 0x10, 0x05, 0xa2, 0x29,
	0x4b, 0x74, 0x94, 0x98, 0x5f, 0xfe, 0x00, 0x02,
	0x00, 0x02, 0xd4, 0x04, 0x04, 0x04, 0x10, 0x00,
	0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01,
	0xe0, 0x80,
}

func h264Width(t *testing.T, sps []byte) int {
	t.Helper()
	var p h264.SPS
	err := p.Unmarshal(sps)
	require.NoError(t, err)
	return p.Width()
}

func h265Width(t *testing.T, sps []byte) int {
	t.Helper()
	var p h265.SPS
	err := p.Unmarshal(sps)
	require.NoError(t, err)
	return p.Width()
}

func TestWriterUpdateParams(t *testing.T) {
	h264IDR := []byte{byte(h264.NALUTypeIDR), 0x01}
	h264NonIDR := []byte{byte(h264.NALUTypeNonIDR), 0x01}
	h265NonIDR := []byte{byte(h265.NALUType_TRAIL_R) << 1, 0x01}

	newWriter := func(t *testing.T, tracks []*gortmplib.Track) (*gortmplib.Writer, *message.ReadWriter) {
		t.Helper()

		var input bytes.Buffer
		var output bytes.Buffer
		c := &dummyConn{
			rw: &readWriter{
				Reader: &input,
				Writer: &output,
			},
		}
		c.initialize()

		w := &gortmplib.Writer{
			Conn:   c,
			Tracks: tracks,
		}
		err := w.Initialize()
		require.NoError(t, err)

		bc := bytecounter.NewReadWriter(&output)
		mrw := message.NewReadWriter(bc, bc, true)

		return w, mrw
	}

	t.Run("h264 legacy", func(t *testing.T) {
		tracks := []*gortmplib.Track{{Codec: &codecs.H264{
			SPS: testCodecH264.SPS,
			PPS: testCodecH264.PPS,
		}}}

		w, mrw := newWriter(t, tracks)

		// drain metadata and initial configuration
		_, err := mrw.Read()
		require.NoError(t, err)
		_, err = mrw.Read()
		require.NoError(t, err)

		err = w.WriteH264(tracks[0], time.Second, time.Second, [][]byte{h264NonIDR})
		require.NoError(t, err)

		// drain frame
		_, err = mrw.Read()
		require.NoError(t, err)

		err = w.UpdateH264Params(tracks[0], testH264SPS720, testH264PPS720)
		require.NoError(t, err)

		// the updated configuration is sent immediately, as a legacy config
		// message, with the DTS of the latest written frame.
		msg, err := mrw.Read()
		require.NoError(t, err)
		require.Equal(t, &message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			IsKeyFrame:      true, //nolint:staticcheck
			PacketType:      message.VideoPacketTypeConfig,
			DTS:             time.Second,
			AVCConfig:       generateAvcC(t, testH264SPS720, testH264PPS720),
		}, msg)

		// a duplicated update is a no-op.
		err = w.UpdateH264Params(tracks[0], testH264SPS720, testH264PPS720)
		require.NoError(t, err)

		err = w.WriteH264(tracks[0], 2*time.Second, 2*time.Second, [][]byte{h264IDR})
		require.NoError(t, err)

		// the next message is the frame, proving that the duplicated
		// update did not emit anything.
		msg, err = mrw.Read()
		require.NoError(t, err)
		require.Equal(t, &message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			IsKeyFrame:      true, //nolint:staticcheck
			PacketType:      message.VideoPacketTypeAU,
			Type:            message.VideoTypeAU, //nolint:staticcheck
			DTS:             2 * time.Second,
			AU:              []byte{0, 0, 0, 2, 5, 1},
		}, msg)
	})

	t.Run("h265 enhanced", func(t *testing.T) {
		tracks := []*gortmplib.Track{{Codec: &codecs.H265{
			VPS: testCodecH265.VPS,
			SPS: testCodecH265.SPS,
			PPS: testCodecH265.PPS,
		}}}

		w, mrw := newWriter(t, tracks)

		// drain metadata and initial configuration
		_, err := mrw.Read()
		require.NoError(t, err)
		_, err = mrw.Read()
		require.NoError(t, err)

		err = w.WriteH265(tracks[0], time.Second, time.Second, [][]byte{h265NonIDR})
		require.NoError(t, err)

		// drain frame
		_, err = mrw.Read()
		require.NoError(t, err)

		err = w.UpdateH265Params(tracks[0], testCodecH265.VPS, testH265SPS720, testCodecH265.PPS)
		require.NoError(t, err)

		// the updated configuration is sent immediately, as an enhanced
		// sequence start, with the DTS of the latest written frame.
		msg, err := mrw.Read()
		require.NoError(t, err)
		require.Equal(t, &message.VideoExSequenceStart{
			ChunkStreamID:   message.VideoChunkStreamID,
			MessageStreamID: 0x1000000,
			FrameType:       message.VideoFrameTypeKeyFrame,
			FourCC:          message.FourCCHEVC,
			DTS:             time.Second,
			HEVCConfig:      generateHvcC(t, testCodecH265.VPS, testH265SPS720, testCodecH265.PPS),
		}, msg)

		// a duplicated update is a no-op.
		err = w.UpdateH265Params(tracks[0], testCodecH265.VPS, testH265SPS720, testCodecH265.PPS)
		require.NoError(t, err)

		err = w.WriteH265(tracks[0], 2*time.Second, 2*time.Second, [][]byte{h265NonIDR})
		require.NoError(t, err)

		msg, err = mrw.Read()
		require.NoError(t, err)
		require.Equal(t, &message.VideoExFramesX{
			ChunkStreamID:   message.VideoChunkStreamID,
			MessageStreamID: 0x1000000,
			FrameType:       message.VideoFrameTypeInterFrame,
			FourCC:          message.FourCCHEVC,
			DTS:             2 * time.Second,
			Payload:         []byte{0, 0, 0, 2, 0x02, 0x01},
		}, msg)
	})

	t.Run("h264 secondary track", func(t *testing.T) {
		tracks := []*gortmplib.Track{
			{Codec: &codecs.H264{
				SPS: testCodecH264.SPS,
				PPS: testCodecH264.PPS,
			}},
			{Codec: &codecs.H264{
				SPS: testCodecH264.SPS,
				PPS: testCodecH264.PPS,
			}},
		}

		w, mrw := newWriter(t, tracks)

		// drain metadata and initial configurations
		for range 3 {
			_, err := mrw.Read()
			require.NoError(t, err)
		}

		err := w.WriteH264(tracks[1], time.Second, time.Second, [][]byte{h264IDR})
		require.NoError(t, err)

		// drain frame
		_, err = mrw.Read()
		require.NoError(t, err)

		err = w.UpdateH264Params(tracks[1], testH264SPS720, testH264PPS720)
		require.NoError(t, err)

		// the updated configuration is sent inside a multitrack message
		// with the right track ID.
		msg, err := mrw.Read()
		require.NoError(t, err)
		require.Equal(t, &message.VideoExMultitrack{
			MultitrackType: 0x0,
			TrackID:        1,
			Wrapped: &message.VideoExSequenceStart{
				ChunkStreamID:   message.VideoChunkStreamID,
				MessageStreamID: 0x1000000,
				FrameType:       message.VideoFrameTypeKeyFrame,
				FourCC:          message.FourCCAVC,
				DTS:             time.Second,
				AVCConfig:       generateAvcC(t, testH264SPS720, testH264PPS720),
			},
		}, msg)
	})

	t.Run("errors", func(t *testing.T) {
		tracks := []*gortmplib.Track{
			{Codec: &codecs.H264{
				SPS: testCodecH264.SPS,
				PPS: testCodecH264.PPS,
			}},
			{Codec: &codecs.H265{
				VPS: testCodecH265.VPS,
				SPS: testCodecH265.SPS,
				PPS: testCodecH265.PPS,
			}},
		}

		w, mrw := newWriter(t, tracks)

		// drain metadata and initial configurations
		for range 3 {
			_, err := mrw.Read()
			require.NoError(t, err)
		}

		// missing parameters
		err := w.UpdateH264Params(tracks[0], nil, testH264PPS720)
		require.EqualError(t, err, "SPS and PPS must be provided")

		err = w.UpdateH264Params(tracks[0], testH264SPS720, nil)
		require.EqualError(t, err, "SPS and PPS must be provided")

		err = w.UpdateH265Params(tracks[1], nil, testH265SPS720, testCodecH265.PPS)
		require.EqualError(t, err, "VPS, SPS and PPS must be provided")

		// invalid parameters
		err = w.UpdateH264Params(tracks[0], []byte{1, 2, 3}, testH264PPS720)
		require.ErrorContains(t, err, "invalid H264 parameters")

		err = w.UpdateH265Params(tracks[1], testCodecH265.VPS, []byte{1, 2, 3}, testCodecH265.PPS)
		require.ErrorContains(t, err, "invalid H265 parameters")

		// codec mismatch
		err = w.UpdateH264Params(tracks[1], testH264SPS720, testH264PPS720)
		require.EqualError(t, err, "track is not a H264 track")

		err = w.UpdateH265Params(tracks[0], testCodecH265.VPS, testH265SPS720, testCodecH265.PPS)
		require.EqualError(t, err, "track is not a H265 track")

		// unknown track
		err = w.UpdateH264Params(&gortmplib.Track{Codec: &codecs.H264{}}, testH264SPS720, testH264PPS720)
		require.EqualError(t, err, "track is not a video track of this writer")

		// the writer is still usable after failed updates, and failed
		// updates did not emit anything.
		err = w.WriteH264(tracks[0], 0, 0, [][]byte{h264IDR})
		require.NoError(t, err)

		msg, err := mrw.Read()
		require.NoError(t, err)
		require.Equal(t, &message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			IsKeyFrame:      true, //nolint:staticcheck
			PacketType:      message.VideoPacketTypeAU,
			Type:            message.VideoTypeAU, //nolint:staticcheck
			AU:              []byte{0, 0, 0, 2, 5, 1},
		}, msg)
	})
}

func TestReadWriteParamsUpdate(t *testing.T) {
	h264IDR := []byte{byte(h264.NALUTypeIDR), 0x01}
	h264NonIDR := []byte{byte(h264.NALUTypeNonIDR), 0x01}
	h265IDR := []byte{byte(h265.NALUType_IDR_W_RADL) << 1, 0x01}
	h265NonIDR := []byte{byte(h265.NALUType_TRAIL_R) << 1, 0x01}

	type event struct {
		kind  string // "params" or "frame"
		width int
		dts   time.Duration
		au    [][]byte
	}

	t.Run("h264", func(t *testing.T) {
		tracks := []*gortmplib.Track{{Codec: &codecs.H264{
			SPS: testCodecH264.SPS,
			PPS: testCodecH264.PPS,
		}}}

		var input bytes.Buffer
		var output bytes.Buffer
		c := &dummyConn{
			rw: &readWriter{
				Reader: &input,
				Writer: &output,
			},
		}
		c.initialize()

		w := &gortmplib.Writer{
			Conn:   c,
			Tracks: tracks,
		}
		err := w.Initialize()
		require.NoError(t, err)

		// stream starts at 1920x1080
		err = w.WriteH264(tracks[0], 0, 0, [][]byte{h264IDR})
		require.NoError(t, err)
		err = w.WriteH264(tracks[0], time.Second, time.Second, [][]byte{h264NonIDR})
		require.NoError(t, err)

		// switch to 1280x720 without interrupting the stream
		err = w.UpdateH264Params(tracks[0], testH264SPS720, testH264PPS720)
		require.NoError(t, err)

		err = w.WriteH264(tracks[0], 2*time.Second, 2*time.Second, [][]byte{h264IDR})
		require.NoError(t, err)
		err = w.WriteH264(tracks[0], 3*time.Second, 3*time.Second, [][]byte{h264NonIDR})
		require.NoError(t, err)

		cr := &dummyConn{rw: &output}
		cr.initialize()

		r := &gortmplib.Reader{Conn: cr}
		err = r.Initialize()
		require.NoError(t, err)
		require.Len(t, r.Tracks(), 1)

		var events []event

		r.OnParamsH264(r.Tracks()[0], func(sps []byte, _ []byte) {
			events = append(events, event{kind: "params", width: h264Width(t, sps)})
		})

		r.OnDataH264(r.Tracks()[0], func(_ time.Duration, dts time.Duration, au [][]byte) {
			events = append(events, event{kind: "frame", dts: dts, au: au})
		})

		for {
			err = r.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
		}

		// the new configuration is notified before the first frame that
		// uses it, and frames written before the update are delivered
		// before it.
		require.Equal(t, []event{
			{kind: "params", width: 1920},
			{kind: "frame", dts: 0, au: [][]byte{testCodecH264.SPS, testCodecH264.PPS}},
			{kind: "frame", dts: 0, au: [][]byte{h264IDR}},
			{kind: "frame", dts: time.Second, au: [][]byte{h264NonIDR}},
			{kind: "params", width: 1280},
			{kind: "frame", dts: time.Second, au: [][]byte{testH264SPS720, testH264PPS720}},
			{kind: "frame", dts: 2 * time.Second, au: [][]byte{h264IDR}},
			{kind: "frame", dts: 3 * time.Second, au: [][]byte{h264NonIDR}},
		}, events)

		// the track holds the latest parameters.
		codec := r.Tracks()[0].Codec.(*codecs.H264)
		require.Equal(t, testH264SPS720, codec.SPS)
		require.Equal(t, testH264PPS720, codec.PPS)
	})

	t.Run("h265", func(t *testing.T) {
		tracks := []*gortmplib.Track{{Codec: &codecs.H265{
			VPS: testCodecH265.VPS,
			SPS: testCodecH265.SPS,
			PPS: testCodecH265.PPS,
		}}}

		var input bytes.Buffer
		var output bytes.Buffer
		c := &dummyConn{
			rw: &readWriter{
				Reader: &input,
				Writer: &output,
			},
		}
		c.initialize()

		w := &gortmplib.Writer{
			Conn:   c,
			Tracks: tracks,
		}
		err := w.Initialize()
		require.NoError(t, err)

		err = w.WriteH265(tracks[0], 0, 0, [][]byte{h265IDR})
		require.NoError(t, err)
		err = w.WriteH265(tracks[0], time.Second, time.Second, [][]byte{h265NonIDR})
		require.NoError(t, err)

		// switch to 1280x720 without interrupting the stream
		err = w.UpdateH265Params(tracks[0], testCodecH265.VPS, testH265SPS720, testCodecH265.PPS)
		require.NoError(t, err)

		err = w.WriteH265(tracks[0], 2*time.Second, 2*time.Second, [][]byte{h265IDR})
		require.NoError(t, err)
		err = w.WriteH265(tracks[0], 3*time.Second, 3*time.Second, [][]byte{h265NonIDR})
		require.NoError(t, err)

		cr := &dummyConn{rw: &output}
		cr.initialize()

		r := &gortmplib.Reader{Conn: cr}
		err = r.Initialize()
		require.NoError(t, err)
		require.Len(t, r.Tracks(), 1)

		initialWidth := h265Width(t, testCodecH265.SPS)
		require.NotEqual(t, initialWidth, 1280)

		var events []event

		r.OnParamsH265(r.Tracks()[0], func(_ []byte, sps []byte, _ []byte) {
			events = append(events, event{kind: "params", width: h265Width(t, sps)})
		})

		r.OnDataH265(r.Tracks()[0], func(_ time.Duration, dts time.Duration, au [][]byte) {
			events = append(events, event{kind: "frame", dts: dts, au: au})
		})

		for {
			err = r.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
		}

		require.Equal(t, []event{
			{kind: "params", width: initialWidth},
			{kind: "frame", dts: 0, au: [][]byte{h265IDR}},
			{kind: "frame", dts: time.Second, au: [][]byte{h265NonIDR}},
			{kind: "params", width: 1280},
			{kind: "frame", dts: 2 * time.Second, au: [][]byte{h265IDR}},
			{kind: "frame", dts: 3 * time.Second, au: [][]byte{h265NonIDR}},
		}, events)

		// the track holds the latest parameters.
		codec := r.Tracks()[0].Codec.(*codecs.H265)
		require.Equal(t, testH265SPS720, codec.SPS)
		require.Equal(t, testCodecH265.VPS, codec.VPS)
		require.Equal(t, testCodecH265.PPS, codec.PPS)
	})
}

func TestReaderParamsUpdateDuplicate(t *testing.T) {
	messages := []message.Message{
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			PacketType:      message.VideoPacketTypeConfig,
			AVCConfig:       generateAvcC(t, testCodecH264.SPS, testCodecH264.PPS),
		},
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             2 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			PacketType:      message.VideoPacketTypeAU,
			AU:              []byte{0, 0, 0, 2, 5, 1},
		},
		// same configuration sent again
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             2 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			PacketType:      message.VideoPacketTypeConfig,
			AVCConfig:       generateAvcC(t, testCodecH264.SPS, testCodecH264.PPS),
		},
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             3 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			PacketType:      message.VideoPacketTypeAU,
			AU:              []byte{0, 0, 0, 2, 5, 2},
		},
	}

	var buf bytes.Buffer
	bc := bytecounter.NewReadWriter(&buf)
	mrw := message.NewReadWriter(bc, bc, true)

	for _, msg := range messages {
		err := mrw.Write(msg)
		require.NoError(t, err)
	}

	c := &dummyConn{rw: &buf}
	c.initialize()

	r := &gortmplib.Reader{Conn: c}
	err := r.Initialize()
	require.NoError(t, err)

	var paramsCount int
	r.OnParamsH264(r.Tracks()[0], func(sps []byte, pps []byte) {
		require.Equal(t, testCodecH264.SPS, sps)
		require.Equal(t, testCodecH264.PPS, pps)
		paramsCount++
	})

	var dts []time.Duration
	r.OnDataH264(r.Tracks()[0], func(_ time.Duration, d time.Duration, _ [][]byte) {
		dts = append(dts, d)
	})

	for {
		err = r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	// a duplicated configuration is notified again and does not disrupt
	// the stream.
	require.Equal(t, 2, paramsCount)
	require.Equal(t, []time.Duration{0, 2 * time.Second, 2 * time.Second, 3 * time.Second}, dts)
	require.Equal(t, testCodecH264.SPS, r.Tracks()[0].Codec.(*codecs.H264).SPS)
}

func TestReaderParamsUpdateCodecMismatch(t *testing.T) {
	messages := []message.Message{
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			PacketType:      message.VideoPacketTypeConfig,
			AVCConfig:       generateAvcC(t, testCodecH264.SPS, testCodecH264.PPS),
		},
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             2 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			FrameType:       message.VideoFrameTypeKeyFrame,
			PacketType:      message.VideoPacketTypeAU,
			AU:              []byte{0, 0, 0, 2, 5, 1},
		},
		// a configuration update cannot change the codec of a track
		&message.VideoExSequenceStart{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             3 * time.Second,
			MessageStreamID: 0x1000000,
			FrameType:       message.VideoFrameTypeKeyFrame,
			FourCC:          message.FourCCHEVC,
			HEVCConfig:      generateHvcC(t, testCodecH265.VPS, testCodecH265.SPS, testCodecH265.PPS),
		},
	}

	var buf bytes.Buffer
	bc := bytecounter.NewReadWriter(&buf)
	mrw := message.NewReadWriter(bc, bc, true)

	for _, msg := range messages {
		err := mrw.Write(msg)
		require.NoError(t, err)
	}

	c := &dummyConn{rw: &buf}
	c.initialize()

	r := &gortmplib.Reader{Conn: c}
	err := r.Initialize()
	require.NoError(t, err)

	r.OnDataH264(r.Tracks()[0], func(_ time.Duration, _ time.Duration, _ [][]byte) {
	})

	for {
		err = r.Read()
		if err != nil {
			break
		}
	}

	require.EqualError(t, err, "received a H265 configuration for video track 0, "+
		"but the track uses a different codec")

	// the failed update did not leave the track in a half-updated state.
	require.Equal(t, testCodecH264.SPS, r.Tracks()[0].Codec.(*codecs.H264).SPS)
	require.Equal(t, testCodecH264.PPS, r.Tracks()[0].Codec.(*codecs.H264).PPS)
}
