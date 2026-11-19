package message_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/bluenviron/gortmplib/pkg/rawmessage"
)

func TestDataAMF0TimestampRoundTrip(t *testing.T) {
	in := &message.DataAMF0{
		ChunkStreamID:   4,
		Timestamp:       1234567 * time.Millisecond,
		MessageStreamID: 0x1000000,
		Payload: amf0.Data{
			"@setDataFrame",
			"onMetaData",
			amf0.Object{
				{Key: "title", Value: "second track"},
			},
		},
	}

	var buf bytes.Buffer
	bc := bytecounter.NewReadWriter(&buf)
	mrw := message.NewReadWriter(bc, bc, true)

	err := mrw.Write(in)
	require.NoError(t, err)

	bc2 := bytecounter.NewReadWriter(&buf)
	mrw2 := message.NewReadWriter(bc2, bc2, true)

	dec, err := mrw2.Read()
	require.NoError(t, err)
	require.Equal(t, in, dec)
}

func TestDataAMF0LargeTimestamp(t *testing.T) {
	// timestamps above 0xFFFFFF require the extended timestamp field
	in := &message.DataAMF0{
		ChunkStreamID:   4,
		Timestamp:       0x1000000 * time.Millisecond,
		MessageStreamID: 0x1000000,
		Payload:         amf0.Data{"onMetaData", amf0.Object{}},
	}

	var buf bytes.Buffer
	bc := bytecounter.NewReadWriter(&buf)
	mrw := message.NewReadWriter(bc, bc, true)

	err := mrw.Write(in)
	require.NoError(t, err)

	bc2 := bytecounter.NewReadWriter(&buf)
	mrw2 := message.NewReadWriter(bc2, bc2, true)

	dec, err := mrw2.Read()
	require.NoError(t, err)
	require.Equal(t, in, dec)
}

func TestDataAMF0WriteTooBig(t *testing.T) {
	in := &message.DataAMF0{
		ChunkStreamID:   4,
		MessageStreamID: 0x1000000,
		Payload:         amf0.Data{strings.Repeat("a", message.MaxDataAMF0Size)},
	}

	var buf bytes.Buffer
	bc := bytecounter.NewReadWriter(&buf)
	mrw := message.NewReadWriter(bc, bc, true)

	err := mrw.Write(in)
	require.EqualError(t, err, "payload size (1048581) exceeds maximum (1048576)")
}

func TestDataAMF0ReadTooBig(t *testing.T) {
	// craft a raw data message whose body exceeds the maximum size,
	// without going through the encoder.
	body := make([]byte, 0, 17*(3+0xFFFF))
	for range 17 {
		body = append(body, 0x02, 0xFF, 0xFF)
		body = append(body, bytes.Repeat([]byte{'a'}, 0xFFFF)...)
	}

	var buf bytes.Buffer
	bc := bytecounter.NewReadWriter(&buf)
	rw := &rawmessage.Writer{
		BW:  bc.Writer,
		BCW: bc.Writer,
	}
	rw.Initialize()

	err := rw.Write(&rawmessage.Message{
		ChunkStreamID:   4,
		Type:            uint8(message.TypeDataAMF0),
		MessageStreamID: 0x1000000,
		Body:            body,
	})
	require.NoError(t, err)

	bc2 := bytecounter.NewReadWriter(&buf)
	mrw2 := message.NewReadWriter(bc2, bc2, true)

	_, err = mrw2.Read()
	require.EqualError(t, err, "payload size (1114146) exceeds maximum (1048576)")
}

func TestDataAMF0InvalidAMF(t *testing.T) {
	var buf bytes.Buffer
	bc := bytecounter.NewReadWriter(&buf)
	rw := &rawmessage.Writer{
		BW:  bc.Writer,
		BCW: bc.Writer,
	}
	rw.Initialize()

	err := rw.Write(&rawmessage.Message{
		ChunkStreamID:   4,
		Type:            uint8(message.TypeDataAMF0),
		MessageStreamID: 0x1000000,
		Body:            []byte{0xFF},
	})
	require.NoError(t, err)

	bc2 := bytecounter.NewReadWriter(&buf)
	mrw2 := message.NewReadWriter(bc2, bc2, true)

	_, err = mrw2.Read()
	require.EqualError(t, err, "unsupported marker 0xff")
}

func TestVideoExMetadataTimestampRoundTrip(t *testing.T) {
	in := &message.VideoExMetadata{
		ChunkStreamID:   6,
		DTS:             5 * time.Second,
		MessageStreamID: 0x1000000,
		FourCC:          message.FourCCHEVC,
		Payload: amf0.Data{
			"colorInfo",
			amf0.Object{{Key: "colorConfig", Value: amf0.Object{}}},
		},
	}

	var buf bytes.Buffer
	bc := bytecounter.NewReadWriter(&buf)
	mrw := message.NewReadWriter(bc, bc, true)

	err := mrw.Write(in)
	require.NoError(t, err)

	bc2 := bytecounter.NewReadWriter(&buf)
	mrw2 := message.NewReadWriter(bc2, bc2, true)

	dec, err := mrw2.Read()
	require.NoError(t, err)
	require.Equal(t, in, dec)
}
