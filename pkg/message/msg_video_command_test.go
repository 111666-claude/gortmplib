package message_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/bluenviron/gortmplib/pkg/message"
)

func newTestReader(buf []byte) *message.Reader {
	bcr := bytecounter.NewReader(bytes.NewReader(buf))
	r := &message.Reader{
		BR:  bcr,
		BCR: bcr,
	}
	r.Initialize()
	return r
}

func newTestWriter() (*message.Writer, *bytes.Buffer) {
	var buf bytes.Buffer
	bcw := bytecounter.NewWriter(&buf)
	w := &message.Writer{
		BW:               bcw,
		BCW:              bcw,
		CheckAcknowledge: true,
	}
	w.Initialize()
	return w, &buf
}

func TestVideoCommand(t *testing.T) {
	for _, ca := range []struct {
		name string
		dec  message.Message
		enc  []byte
	}{
		{
			"video command 0x50 0x00 (start seek, no codec)",
			&message.VideoCommand{
				ChunkStreamID:   message.VideoChunkStreamID,
				MessageStreamID: 0x1000000,
				Codec:           0,
				Command:         message.VideoCommandTypeStartSeek,
			},
			[]byte{
				0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x09,
				0x01, 0x00, 0x00, 0x00, 0x50, 0x00,
			},
		},
		{
			"video command 0x57 0x00 (start seek, h264 codec id)",
			&message.VideoCommand{
				ChunkStreamID:   message.VideoChunkStreamID,
				DTS:             500 * time.Millisecond,
				MessageStreamID: 0x1000000,
				Codec:           message.CodecH264,
				Command:         message.VideoCommandTypeStartSeek,
			},
			[]byte{
				0x06, 0x00, 0x01, 0xf4, 0x00, 0x00, 0x02, 0x09,
				0x01, 0x00, 0x00, 0x00, 0x57, 0x00,
			},
		},
		{
			"video command end seek",
			&message.VideoCommand{
				ChunkStreamID:   message.VideoChunkStreamID,
				MessageStreamID: 0x1000000,
				Codec:           0,
				Command:         message.VideoCommandTypeEndSeek,
			},
			[]byte{
				0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x09,
				0x01, 0x00, 0x00, 0x00, 0x50, 0x01,
			},
		},
	} {
		t.Run(ca.name, func(t *testing.T) {
			// decode
			dec, err := newTestReader(ca.enc).Read()
			require.NoError(t, err)
			require.Equal(t, ca.dec, dec)

			// encode
			w, buf := newTestWriter()
			err = w.Write(ca.dec)
			require.NoError(t, err)
			require.Equal(t, ca.enc, buf.Bytes())
		})
	}
}

func TestVideoCommandNotConfusedWithVideo(t *testing.T) {
	// the control frame must not be decoded as a regular Video frame
	dec, err := newTestReader([]byte{
		0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x09,
		0x01, 0x00, 0x00, 0x00, 0x57, 0x00,
	}).Read()
	require.NoError(t, err)
	require.IsType(t, &message.VideoCommand{}, dec)

	cmd := dec.(*message.VideoCommand)
	require.Equal(t, uint8(message.CodecH264), cmd.Codec)
	require.Equal(t, message.VideoCommandTypeStartSeek, cmd.Command)
}

func TestVideoCommandErrors(t *testing.T) {
	for _, ca := range []struct {
		name string
		enc  []byte
	}{
		{
			"one byte body",
			[]byte{
				0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x09,
				0x01, 0x00, 0x00, 0x00, 0x50,
			},
		},
		{
			"three byte body",
			[]byte{
				0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x03, 0x09,
				0x01, 0x00, 0x00, 0x00, 0x50, 0x00, 0x00,
			},
		},
		{
			"unknown command",
			[]byte{
				0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x09,
				0x01, 0x00, 0x00, 0x00, 0x50, 0x02,
			},
		},
	} {
		t.Run(ca.name, func(t *testing.T) {
			_, err := newTestReader(ca.enc).Read()
			require.Error(t, err)
		})
	}
}

func TestVideoCommandMarshalError(t *testing.T) {
	w, _ := newTestWriter()
	err := w.Write(&message.VideoCommand{
		ChunkStreamID: message.VideoChunkStreamID,
		Codec:         0,
		Command:       message.VideoCommandType(9),
	})
	require.Error(t, err)
}

func TestEmptyAudio(t *testing.T) {
	enc := []byte{
		0x04, 0x00, 0x01, 0xf4, 0x00, 0x00, 0x00, 0x08,
		0x01, 0x00, 0x00, 0x00,
	}

	dec, err := newTestReader(enc).Read()
	require.NoError(t, err)

	audio, ok := dec.(*message.Audio)
	require.True(t, ok)
	require.True(t, audio.IsEmpty)
	require.Equal(t, 500*time.Millisecond, audio.DTS)
	require.Empty(t, audio.AU)

	// round trip
	w, buf := newTestWriter()
	err = w.Write(audio)
	require.NoError(t, err)
	require.Equal(t, enc, buf.Bytes())
}

func TestAudioErrors(t *testing.T) {
	for _, ca := range []struct {
		name string
		enc  []byte
	}{
		{
			"one byte mpeg4 audio body",
			[]byte{
				0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x08,
				0x01, 0x00, 0x00, 0x00, 0xaf,
			},
		},
		{
			"unsupported codec",
			[]byte{
				0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x08,
				0x01, 0x00, 0x00, 0x00, 0x40, 0x00,
			},
		},
	} {
		t.Run(ca.name, func(t *testing.T) {
			_, err := newTestReader(ca.enc).Read()
			require.Error(t, err)
		})
	}
}
