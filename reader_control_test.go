package gortmplib_test

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortmplib/pkg/message"
)

// TestReaderControlAndEmptyAudio mixes video control frames, empty audio
// messages, configuration frames and media frames. Control and empty audio
// messages must be read and skipped in both the track discovery phase and the
// playback phase, while every valid media frame must still be delivered.
func TestReaderControlAndEmptyAudio(t *testing.T) {
	messages := []message.Message{
		// control frame before the key frame, carrying the h264 codec id
		&message.VideoCommand{
			ChunkStreamID:   message.VideoChunkStreamID,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			Command:         message.VideoCommandTypeStartSeek,
		},
		// empty audio keepalive before any audio track is configured
		&message.Audio{
			ChunkStreamID:   message.AudioChunkStreamID,
			DTS:             0,
			MessageStreamID: 0x1000000,
			IsEmpty:         true,
		},
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             1 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			IsKeyFrame:      true,
			Type:            message.VideoTypeConfig,
			AVCConfig:       generateAvcC(t, testCodecH264.SPS, testCodecH264.PPS),
		},
		// control frame with no codec id
		&message.VideoCommand{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             1 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           0,
			Command:         message.VideoCommandTypeEndSeek,
		},
		&message.Audio{
			ChunkStreamID:   message.AudioChunkStreamID,
			DTS:             1 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecMPEG4Audio,
			Rate:            message.AudioRate44100,
			Depth:           message.AudioDepth16,
			IsStereo:        true,
			AACType:         message.AudioAACTypeConfig,
			AACConfig: &mpeg4audio.AudioSpecificConfig{
				Type:          mpeg4audio.ObjectTypeAACLC,
				SampleRate:    44100,
				ChannelConfig: 2,
				ChannelCount:  2, //nolint:staticcheck
			},
		},
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             2 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			IsKeyFrame:      true,
			Type:            message.VideoTypeAU,
			AU:              []byte{0, 0, 0, 2, 5, 1},
		},
		// another empty audio keepalive, interleaved with media
		&message.Audio{
			ChunkStreamID:   message.AudioChunkStreamID,
			DTS:             2 * time.Second,
			MessageStreamID: 0x1000000,
			IsEmpty:         true,
		},
		&message.Audio{
			ChunkStreamID:   message.AudioChunkStreamID,
			DTS:             2 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecMPEG4Audio,
			Rate:            message.AudioRate44100,
			Depth:           message.AudioDepth16,
			IsStereo:        true,
			AACType:         message.AudioAACTypeAU,
			AU:              []byte{0x11, 0x22},
		},
		&message.Video{
			ChunkStreamID:   message.VideoChunkStreamID,
			DTS:             3 * time.Second,
			MessageStreamID: 0x1000000,
			Codec:           message.CodecH264,
			Type:            message.VideoTypeAU,
			AU:              []byte{0, 0, 0, 2, 6, 2},
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

	// both tracks must be discovered, control frames and empty audio ignored
	tracks := r.Tracks()
	require.Len(t, tracks, 2)
	require.IsType(t, &codecs.H264{}, tracks[0].Codec)
	require.IsType(t, &codecs.MPEG4Audio{}, tracks[1].Codec)

	var videoFrames [][][]byte
	var audioFrames [][]byte

	r.OnDataH264(tracks[0], func(_ time.Duration, _ time.Duration, au [][]byte) {
		videoFrames = append(videoFrames, au)
	})
	r.OnDataMPEG4Audio(tracks[1], func(_ time.Duration, au []byte) {
		audioFrames = append(audioFrames, au)
	})

	for {
		err = r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	// config frame + key frame + inter frame, none of the control frames
	require.Equal(t, [][][]byte{
		{testCodecH264.SPS, testCodecH264.PPS},
		{{5, 1}},
		{{6, 2}},
	}, videoFrames)

	// only the real AAC access unit, none of the empty messages
	require.Equal(t, [][]byte{
		{0x11, 0x22},
	}, audioFrames)
}
