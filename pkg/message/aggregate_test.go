package message_test

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/bluenviron/gortmplib/pkg/message"
)

// aggregateFixtureMessages are the audio, video and metadata messages used by
// the aggregate tests.
func aggregateFixtureMessages() []message.Message {
	return []message.Message{
		&message.Audio{
			ChunkStreamID:   message.AggregateChunkStreamID,
			DTS:             1000 * time.Millisecond,
			MessageStreamID: 1,
			Codec:           message.CodecMPEG4Audio,
			Rate:            message.AudioRate44100,
			Depth:           message.AudioDepth16,
			IsStereo:        true,
			AACType:         message.AudioAACTypeAU,
			AU:              []byte{0xAA, 0xBB},
		},
		&message.Video{
			ChunkStreamID:   message.AggregateChunkStreamID,
			DTS:             1040 * time.Millisecond,
			MessageStreamID: 1,
			FrameType:       message.VideoFrameTypeKeyFrame,
			Codec:           message.CodecH264,
			PacketType:      message.VideoPacketTypeAU,
			AU:              []byte{0x11, 0x22, 0x33},
		},
		&message.DataAMF0{
			ChunkStreamID:   message.AggregateChunkStreamID,
			MessageStreamID: 1,
			Payload: amf0.Data{
				"onMetaData",
				amf0.ECMAArray{
					{Key: "duration", Value: float64(0)},
				},
			},
		},
	}
}

// aggregateFixtureExpected is what the reader must return when splitting the
// aggregate fixture: original order and absolute timestamps.
func aggregateFixtureExpected() []message.Message {
	return []message.Message{
		&message.Audio{
			ChunkStreamID:   message.AggregateChunkStreamID,
			DTS:             1000 * time.Millisecond,
			MessageStreamID: 1,
			Codec:           message.CodecMPEG4Audio,
			Rate:            message.AudioRate44100,
			Depth:           message.AudioDepth16,
			IsStereo:        true,
			AACType:         message.AudioAACTypeAU,
			AU:              []byte{0xAA, 0xBB},
		},
		&message.Video{
			ChunkStreamID:   message.AggregateChunkStreamID,
			DTS:             1040 * time.Millisecond,
			MessageStreamID: 1,
			FrameType:       message.VideoFrameTypeKeyFrame,
			IsKeyFrame:      true,
			Codec:           message.CodecH264,
			PacketType:      message.VideoPacketTypeAU,
			Type:            message.VideoPacketTypeAU,
			AU:              []byte{0x11, 0x22, 0x33},
		},
		&message.DataAMF0{
			ChunkStreamID:   message.AggregateChunkStreamID,
			MessageStreamID: 1,
			Payload: amf0.Data{
				"onMetaData",
				amf0.ECMAArray{
					{Key: "duration", Value: float64(0)},
				},
			},
		},
	}
}

// aggregateFixtureBytes is the exact RTMP chunk stream produced by writing the
// fixture as a single aggregate message: a type 0 chunk carrying message
// type 22, whose body is three FLV tags (audio, video, data) each followed by
// its PreviousTagSize.
var aggregateFixtureBytes = []byte{
	// RTMP type 0 chunk header: chunk stream 4, timestamp 1000,
	// body length 97, message type 22, message stream ID 1
	0x04, 0x00, 0x03, 0xe8, 0x00, 0x00, 0x61, 0x16,
	0x00, 0x00, 0x00, 0x01,

	// audio tag: type 8, data size 4, timestamp offset 0, stream ID 0
	0x08, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00,
	// audio data
	0xaf, 0x01, 0xaa, 0xbb,
	// previous tag size: 11 + 4
	0x00, 0x00, 0x00, 0x0f,

	// video tag: type 9, data size 8, timestamp offset 40, stream ID 0
	0x09, 0x00, 0x00, 0x08, 0x00, 0x00, 0x28, 0x00,
	0x00, 0x00, 0x00,
	// video data
	0x17, 0x01, 0x00, 0x00, 0x00, 0x11, 0x22, 0x33,
	// previous tag size: 11 + 8
	0x00, 0x00, 0x00, 0x13,

	// data (metadata) tag: type 18, data size 40, timestamp offset 40, stream ID 0
	0x12, 0x00, 0x00, 0x28, 0x00, 0x00, 0x28, 0x00,
	0x00, 0x00, 0x00,
	// AMF0 data: "onMetaData" + ECMA array {duration: 0} + object end
	0x02, 0x00, 0x0a, 0x6f, 0x6e, 0x4d, 0x65, 0x74,
	0x61, 0x44, 0x61, 0x74, 0x61, 0x08, 0x00, 0x00,
	0x00, 0x01, 0x00, 0x08, 0x64, 0x75, 0x72, 0x61,
	0x74, 0x69, 0x6f, 0x6e, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x09,
	// previous tag size: 11 + 40
	0x00, 0x00, 0x00, 0x33,
}

func writeAggregate(t *testing.T, msgs []message.Message) []byte {
	t.Helper()

	var buf bytes.Buffer
	bc := bytecounter.NewWriter(&buf)
	w := &message.Writer{BW: bc, BCW: bc}
	w.Initialize()

	err := w.Write(&message.Aggregate{
		ChunkStreamID:   message.AggregateChunkStreamID,
		MessageStreamID: 1,
		Messages:        msgs,
	})
	require.NoError(t, err)

	return buf.Bytes()
}

func newAggregateReader(t *testing.T, buf []byte) *message.Reader {
	t.Helper()

	bcr := bytecounter.NewReader(bytes.NewReader(buf))
	r := &message.Reader{
		BR:  bcr,
		BCR: bcr,
	}
	r.Initialize()
	return r
}

func TestAggregateWrite(t *testing.T) {
	buf := writeAggregate(t, aggregateFixtureMessages())
	require.Equal(t, aggregateFixtureBytes, buf)
}

func TestAggregateRead(t *testing.T) {
	r := newAggregateReader(t, aggregateFixtureBytes)

	for _, expected := range aggregateFixtureExpected() {
		msg, err := r.Read()
		require.NoError(t, err)
		require.Equal(t, expected, msg)
	}

	_, err := r.Read()
	require.ErrorIs(t, err, io.EOF)
}

func TestAggregateRoundTrip(t *testing.T) {
	buf := writeAggregate(t, aggregateFixtureMessages())
	r := newAggregateReader(t, buf)

	for _, expected := range aggregateFixtureExpected() {
		msg, err := r.Read()
		require.NoError(t, err)
		require.Equal(t, expected, msg)
	}

	_, err := r.Read()
	require.ErrorIs(t, err, io.EOF)
}

func TestAggregateReadAcrossChunks(t *testing.T) {
	// force a small chunk size so that the 97-byte aggregate is split
	// over several RTMP chunks, and append a standalone message on another
	// chunk stream to verify that the ordinary path is unaffected.
	var buf bytes.Buffer
	bc := bytecounter.NewWriter(&buf)
	w := &message.Writer{BW: bc, BCW: bc}
	w.Initialize()

	err := w.Write(&message.SetChunkSize{Value: 30})
	require.NoError(t, err)

	err = w.Write(&message.Aggregate{
		ChunkStreamID:   message.AggregateChunkStreamID,
		MessageStreamID: 1,
		Messages:        aggregateFixtureMessages(),
	})
	require.NoError(t, err)

	standalone := &message.Audio{
		ChunkStreamID:   7,
		DTS:             2000 * time.Millisecond,
		MessageStreamID: 1,
		Codec:           message.CodecMPEG4Audio,
		Rate:            message.AudioRate44100,
		Depth:           message.AudioDepth16,
		IsStereo:        true,
		AACType:         message.AudioAACTypeAU,
		AU:              []byte{0xCC, 0xDD},
	}
	err = w.Write(standalone)
	require.NoError(t, err)

	r := newAggregateReader(t, buf.Bytes())

	// the set chunk size message is returned by the ordinary message path,
	// while the reader transparently applies it to the underlying chunk reader
	msg, err := r.Read()
	require.NoError(t, err)
	require.Equal(t, &message.SetChunkSize{Value: 30}, msg)

	// the aggregate, split over several chunks, is then unpacked in order,
	// followed by the standalone message on another chunk stream
	expected := append(aggregateFixtureExpected(), &message.Audio{
		ChunkStreamID:   7,
		DTS:             2000 * time.Millisecond,
		MessageStreamID: 1,
		Codec:           message.CodecMPEG4Audio,
		Rate:            message.AudioRate44100,
		Depth:           message.AudioDepth16,
		IsStereo:        true,
		AACType:         message.AudioAACTypeAU,
		AU:              []byte{0xCC, 0xDD},
	})

	for _, exp := range expected {
		msg, err := r.Read()
		require.NoError(t, err)
		require.Equal(t, exp, msg)
	}

	_, err = r.Read()
	require.ErrorIs(t, err, io.EOF)
}

func TestAggregateExtendedTimestamp(t *testing.T) {
	// a base timestamp requiring the RTMP extended timestamp field,
	// while still fitting in the 32-bit chunk timestamp.
	base := 0xFFFFFF * time.Millisecond
	msgs := []message.Message{
		&message.Audio{
			ChunkStreamID:   message.AggregateChunkStreamID,
			DTS:             base,
			MessageStreamID: 1,
			Codec:           message.CodecMPEG1Audio,
			Rate:            message.AudioRate44100,
			Depth:           message.AudioDepth16,
			IsStereo:        true,
			AU:              []byte{1, 2},
		},
		&message.Video{
			ChunkStreamID:   message.AggregateChunkStreamID,
			DTS:             base + 40*time.Millisecond,
			MessageStreamID: 1,
			FrameType:       message.VideoFrameTypeKeyFrame,
			IsKeyFrame:      true,
			Codec:           message.CodecH264,
			PacketType:      message.VideoPacketTypeAU,
			Type:            message.VideoPacketTypeAU,
			AU:              []byte{3},
		},
	}

	buf := writeAggregate(t, msgs)

	// the RTMP header must use the extended timestamp field
	require.Equal(t, []byte{0xff, 0xff, 0xff}, buf[1:4])

	// the second FLV tag carries a 40 ms relative offset (24-bit part)
	videoTagOffset := 12 + (11 + 3 + 4) + 4
	require.Equal(t, byte(40), buf[videoTagOffset+6])

	r := newAggregateReader(t, buf)

	got1, err := r.Read()
	require.NoError(t, err)
	require.Equal(t, base, got1.(*message.Audio).DTS)

	got2, err := r.Read()
	require.NoError(t, err)
	require.Equal(t, base+40*time.Millisecond, got2.(*message.Video).DTS)
}

func TestAggregateDataFirst(t *testing.T) {
	// a metadata message without a timestamp comes first: the aggregate base
	// timestamp must then be sourced from the first media message.
	msgs := []message.Message{
		&message.DataAMF0{
			ChunkStreamID:   message.AggregateChunkStreamID,
			MessageStreamID: 1,
			Payload: amf0.Data{
				"onMetaData",
				amf0.ECMAArray{},
			},
		},
		aggregateFixtureMessages()[0],
		aggregateFixtureMessages()[1],
	}

	buf := writeAggregate(t, msgs)
	r := newAggregateReader(t, buf)

	got0, err := r.Read()
	require.NoError(t, err)
	require.IsType(t, &message.DataAMF0{}, got0)

	got1, err := r.Read()
	require.NoError(t, err)
	require.Equal(t, 1000*time.Millisecond, got1.(*message.Audio).DTS)

	got2, err := r.Read()
	require.NoError(t, err)
	require.Equal(t, 1040*time.Millisecond, got2.(*message.Video).DTS)
}

func TestAggregateWriteErrors(t *testing.T) {
	for _, ca := range []struct {
		name string
		msg  message.Message
		err  string
	}{
		{
			"unsupported sub-message type",
			&message.SetChunkSize{Value: 128},
			"aggregate messages can only contain audio, video or data messages, got *message.SetChunkSize",
		},
		{
			"mismatched stream id",
			&message.Audio{
				ChunkStreamID:   message.AggregateChunkStreamID,
				MessageStreamID: 2,
				Codec:           message.CodecMPEG4Audio,
				AU:              []byte{1, 2},
			},
			"aggregate sub-message stream ID (2) does not match aggregate stream ID (1)",
		},
		{
			"timestamp going backwards",
			nil, // built below
			"",
		},
		{
			"sub-message too large",
			nil, // built below
			"",
		},
	} {
		t.Run(ca.name, func(t *testing.T) {
			var msgs []message.Message

			switch ca.name {
			case "timestamp going backwards":
				msgs = []message.Message{
					&message.Video{
						MessageStreamID: 1,
						FrameType:       message.VideoFrameTypeKeyFrame,
						Codec:           message.CodecH264,
						PacketType:      message.VideoPacketTypeAU,
						DTS:             1000 * time.Millisecond,
						AU:              []byte{1},
					},
					&message.Video{
						MessageStreamID: 1,
						FrameType:       message.VideoFrameTypeInterFrame,
						Codec:           message.CodecH264,
						PacketType:      message.VideoPacketTypeAU,
						DTS:             900 * time.Millisecond,
						AU:              []byte{2},
					},
				}
				ca.err = "aggregate message contains a message with a timestamp going backwards"

			case "sub-message too large":
				// MPEG1 audio adds a 1-byte header, so a 0xFFFFFF AU makes
				// the tag body exactly one byte over the 24-bit limit.
				msgs = []message.Message{
					&message.Audio{
						MessageStreamID: 1,
						Codec:           message.CodecMPEG1Audio,
						AU:              make([]byte, 0xFFFFFF),
					},
				}
				ca.err = "aggregate sub-message body size (16777216) exceeds maximum (16777215)"

			default:
				msgs = aggregateFixtureMessages()
				msgs[0] = ca.msg
			}

			var out bytes.Buffer
			bc := bytecounter.NewWriter(&out)
			w := &message.Writer{BW: bc, BCW: bc}
			w.Initialize()

			err := w.Write(&message.Aggregate{
				ChunkStreamID:   message.AggregateChunkStreamID,
				MessageStreamID: 1,
				Messages:        msgs,
			})
			require.EqualError(t, err, ca.err)
			require.Empty(t, out.Bytes())
		})
	}
}

func TestAggregateWriteEmpty(t *testing.T) {
	var out bytes.Buffer
	bc := bytecounter.NewWriter(&out)
	w := &message.Writer{BW: bc, BCW: bc}
	w.Initialize()

	err := w.Write(&message.Aggregate{
		ChunkStreamID:   message.AggregateChunkStreamID,
		MessageStreamID: 1,
	})
	require.EqualError(t, err, "aggregate message must contain at least one message")
}

func TestAggregateReadErrors(t *testing.T) {
	// body offsets inside the RTMP chunk (12-byte type 0 header)
	const (
		chunkBodyLenOffset = 4
		audioTagTypeOffset = 12
		audioTSOffset      = 12 + 4
		audioPTSOffset     = 12 + 11 + 4 // first byte of the 4-byte previous tag size
	)

	// truncateChunk trims the stream and adjusts the chunk header so that the
	// declared body length matches the bytes actually present: truncation then
	// happens at aggregate parsing level rather than at chunk reading level.
	truncateChunk := func(b []byte, n int) []byte {
		b = b[:n]
		bodyLen := n - 12
		b[chunkBodyLenOffset] = byte(bodyLen >> 16)
		b[chunkBodyLenOffset+1] = byte(bodyLen >> 8)
		b[chunkBodyLenOffset+2] = byte(bodyLen)
		return b
	}

	for _, ca := range []struct {
		name string
		mut  func([]byte) []byte
		err  string
	}{
		{
			"unsupported tag type",
			func(b []byte) []byte {
				b[audioTagTypeOffset] = uint8(message.TypeSetPeerBandwidth)
				return b
			},
			"aggregate message contains unsupported tag type: 6",
		},
		{
			"invalid previous tag size",
			func(b []byte) []byte {
				b[audioPTSOffset+3] = 0x0e
				return b
			},
			"aggregate message has invalid previous tag size: expected 15, got 14",
		},
		{
			"truncated tag header",
			func(b []byte) []byte {
				// only 3 bytes of the audio tag header remain
				return truncateChunk(b, 12+3)
			},
			"aggregate message is truncated: not enough bytes for tag header",
		},
		{
			"truncated in the middle of a tag",
			func(b []byte) []byte {
				// header of the audio tag is complete but its data is missing
				return truncateChunk(b, 12+15)
			},
			"aggregate message is truncated: not enough bytes for tag data or previous tag size",
		},
		{
			"timestamp going backwards",
			func(b []byte) []byte {
				// make the audio tag timestamp (48) greater than the video one (40)
				b[audioTSOffset+2] = 0x30
				return b
			},
			"aggregate message contains a tag with a timestamp going backwards",
		},
		{
			"declared data size exceeds body",
			func(b []byte) []byte {
				b[13] = 0x10 // audio tag data size 0x100004 instead of 4
				return b
			},
			"aggregate message is truncated: not enough bytes for tag data or previous tag size",
		},
	} {
		t.Run(ca.name, func(t *testing.T) {
			buf := ca.mut(append([]byte(nil), aggregateFixtureBytes...))
			r := newAggregateReader(t, buf)

			_, err := r.Read()
			require.EqualError(t, err, ca.err)
		})
	}
}

func TestAggregateReadEmpty(t *testing.T) {
	// type 0 chunk carrying an empty aggregate message
	buf := []byte{
		0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x16,
		0x00, 0x00, 0x00, 0x01,
	}
	r := newAggregateReader(t, buf)

	_, err := r.Read()
	require.EqualError(t, err, "aggregate message is empty")
}
