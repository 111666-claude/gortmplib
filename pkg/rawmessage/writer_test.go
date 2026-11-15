package rawmessage_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/bluenviron/gortmplib/pkg/chunk"
	"github.com/bluenviron/gortmplib/pkg/rawmessage"
)

func chunkBodySize(ch chunk.Chunk) uint32 {
	switch ch := ch.(type) {
	case *chunk.Chunk0:
		return uint32(len(ch.Body))
	case *chunk.Chunk1:
		return uint32(len(ch.Body))
	case *chunk.Chunk2:
		return uint32(len(ch.Body))
	case *chunk.Chunk3:
		return uint32(len(ch.Body))
	}
	return 0
}

func chunkHasExtendedTimestamp(ch chunk.Chunk) bool {
	switch ch := ch.(type) {
	case *chunk.Chunk0:
		return ch.Timestamp >= 0xFFFFFF
	case *chunk.Chunk1:
		return ch.TimestampDelta >= 0xFFFFFF
	case *chunk.Chunk2:
		return ch.TimestampDelta >= 0xFFFFFF
	case *chunk.Chunk3:
		return ch.ExtendedTimestamp != 0
	}
	return false
}

func TestWriter(t *testing.T) {
	for _, ca := range cases {
		t.Run(ca.name, func(t *testing.T) {
			var buf bytes.Buffer
			bc := bytecounter.NewWriter(&buf)
			w := &rawmessage.Writer{
				BW:               bc,
				BCW:              bc,
				CheckAcknowledge: true,
			}
			w.Initialize()

			for _, msg := range ca.messages {
				err := w.Write(msg)
				require.NoError(t, err)
			}

			hasExtendedTimestamp := false

			for _, cach := range ca.chunks {
				ch := reflect.New(reflect.TypeOf(cach).Elem()).Interface().(chunk.Chunk)
				err := ch.Read(&buf, chunkBodySize(cach), hasExtendedTimestamp)
				require.NoError(t, err)
				require.Equal(t, cach, ch)
				hasExtendedTimestamp = chunkHasExtendedTimestamp(cach)
			}

			require.Zero(t, buf.Len())
		})
	}
}

func TestWriterAcknowledge(t *testing.T) {
	for _, ca := range []string{"standard", "overflow"} {
		t.Run(ca, func(t *testing.T) {
			var buf bytes.Buffer
			bc := bytecounter.NewWriter(&buf)
			w := &rawmessage.Writer{
				BW:               bc,
				BCW:              bc,
				CheckAcknowledge: true,
			}
			w.Initialize()

			if ca == "overflow" {
				bc.SetCount(4294967096)
				w.AckValue = 4294967096
			}

			w.SetChunkSize(65536)
			w.SetWindowAckSize(100)

			err := w.Write(&rawmessage.Message{
				ChunkStreamID:   27,
				Timestamp:       18576 * time.Millisecond,
				Type:            6,
				MessageStreamID: 3123,
				Body:            bytes.Repeat([]byte{0x03}, 200),
			})
			require.NoError(t, err)

			err = w.Write(&rawmessage.Message{
				ChunkStreamID:   27,
				Timestamp:       18576 * time.Millisecond,
				Type:            6,
				MessageStreamID: 3123,
				Body:            bytes.Repeat([]byte{0x03}, 200),
			})
			require.EqualError(t, err, "no acknowledge received within window")
		})
	}
}

func TestWriterConcurrent(t *testing.T) {
	const writers = 8
	const messagesPerWriter = 24

	var buf bytes.Buffer
	bcw := bytecounter.NewWriter(&buf)
	w := rawmessage.NewWriter(bcw, bcw, true)
	w.SetWindowAckSize(1 << 30)

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	start := make(chan struct{})

	for id := range writers {
		wg.Go(func() {
			<-start

			for i := range messagesPerWriter {
				err := w.Write(&rawmessage.Message{
					ChunkStreamID:   byte(id%2 + 3),
					Timestamp:       0,
					Type:            9,
					MessageStreamID: uint32(id + 1),
					Body:            bytes.Repeat([]byte{byte(id), byte(i)}, 1024),
				})
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}

	wg.Go(func() {
		<-start

		for range writers * messagesPerWriter {
			w.SetChunkSize(128)
			w.SetWindowAckSize(1 << 30)
			w.SetAcknowledgeValue(0)
		}
	})

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	bcr := bytecounter.NewReader(bytes.NewReader(buf.Bytes()))
	r := rawmessage.NewReader(bcr, bcr, nil)
	seen := make(map[[2]int]bool)

	for range writers * messagesPerWriter {
		msg, err := r.Read()
		require.NoError(t, err)

		id := int(msg.MessageStreamID) - 1
		require.Len(t, msg.Body, 2048)
		i := int(msg.Body[1])
		require.GreaterOrEqual(t, id, 0)
		require.Less(t, id, writers)
		require.GreaterOrEqual(t, i, 0)
		require.Less(t, i, messagesPerWriter)
		require.Equal(t, byte(id%2+3), msg.ChunkStreamID)
		require.False(t, seen[[2]int{id, i}])
		seen[[2]int{id, i}] = true

		require.Zero(t, msg.Timestamp)
		require.Equal(t, uint8(9), msg.Type)
		require.Equal(t, uint32(id+1), msg.MessageStreamID)
		require.Equal(t, bytes.Repeat([]byte{byte(id), byte(i)}, 1024), msg.Body)
	}

	_, err := r.Read()
	require.ErrorIs(t, err, io.EOF)
}

func TestWriterExtendedTimestampContinuation(t *testing.T) {
	// A 260-byte message with a timestamp of 0xFFFFFF milliseconds is split
	// into 3 chunks (128 + 128 + 4 bytes). Both continuation (type 3) chunks
	// must carry the full 0xFFFFFF value in their extended timestamp field,
	// instead of four zero bytes.
	const (
		chunkStreamID   = 27
		msgType         = 9
		messageStreamID = uint32(3123)
		bodyLen         = 260
		timestamp       = uint32(0xFFFFFF)
	)

	body := make([]byte, bodyLen)
	for i := range body {
		body[i] = byte(i)
	}

	msg := &rawmessage.Message{
		ChunkStreamID:   chunkStreamID,
		Timestamp:       time.Duration(timestamp) * time.Millisecond,
		Type:            msgType,
		MessageStreamID: messageStreamID,
		Body:            body,
	}

	// expected wire layout of one message
	expected := func() []byte {
		var b bytes.Buffer

		// chunk0: basic header (1) + message header (11) + extended (4) + body (128)
		b.Write([]byte{chunkStreamID, 0xFF, 0xFF, 0xFF, 0x00, 0x01, 0x04, msgType})
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, messageStreamID)
		b.Write(hdr)
		b.Write([]byte{0x00, 0xFF, 0xFF, 0xFF})
		b.Write(body[:128])

		// first chunk3: basic header (1) + extended timestamp (5) + body (128)
		b.Write([]byte{3<<6 | chunkStreamID, 0x00, 0xFF, 0xFF, 0xFF})
		b.Write(body[128:256])

		// second chunk3: basic header (1) + extended timestamp (5) + body (4)
		b.Write([]byte{3<<6 | chunkStreamID, 0x00, 0xFF, 0xFF, 0xFF})
		b.Write(body[256:])

		return b.Bytes()
	}()

	for round := 0; round < 100; round++ {
		t.Run(fmt.Sprintf("round%d", round), func(t *testing.T) {
			var buf bytes.Buffer
			bc := bytecounter.NewWriter(&buf)
			w := &rawmessage.Writer{
				BW:               bc,
				BCW:              bc,
				CheckAcknowledge: true,
			}
			w.Initialize()

			err := w.Write(msg)
			require.NoError(t, err)

			// the written bytes must be byte-exact, round after round
			require.Equal(t, expected, buf.Bytes())

			// the two continuation headers must be byte-exact
			require.Equal(t,
				[]byte{3<<6 | chunkStreamID, 0x00, 0xFF, 0xFF, 0xFF},
				buf.Bytes()[12+4+128:12+4+128+5])
			require.Equal(t,
				[]byte{3<<6 | chunkStreamID, 0x00, 0xFF, 0xFF, 0xFF},
				buf.Bytes()[12+4+128+5+128:12+4+128+5+128+5])

			// reading the written bytes back must restore the original message
			bcr := bytecounter.NewReader(bytes.NewReader(buf.Bytes()))
			r := rawmessage.NewReader(bcr, bcr, nil)
			dec, err := r.Read()
			require.NoError(t, err)
			require.Equal(t, msg, dec)

			_, err = r.Read()
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestWriterExtendedTimestampBoundary(t *testing.T) {
	// timestamps below the boundary use the regular 3-byte field, while the
	// boundary value and values above it switch to the extended field; the
	// continuation chunks of a split message must repeat that value.
	for _, ca := range []struct {
		name      string
		timestamp uint32
		extended  bool
	}{
		{"just before boundary", 0xFFFFFE, false},
		{"at boundary", 0xFFFFFF, true},
		{"just after boundary", 0x1000000, true},
	} {
		t.Run(ca.name, func(t *testing.T) {
			var buf bytes.Buffer
			bc := bytecounter.NewWriter(&buf)
			w := &rawmessage.Writer{
				BW:               bc,
				BCW:              bc,
				CheckAcknowledge: true,
			}
			w.Initialize()

			err := w.Write(&rawmessage.Message{
				ChunkStreamID:   27,
				Timestamp:       time.Duration(ca.timestamp) * time.Millisecond,
				Type:            9,
				MessageStreamID: 3123,
				Body:            bytes.Repeat([]byte{0x01}, 260),
			})
			require.NoError(t, err)

			raw := buf.Bytes()

			if ca.extended {
				// type 0 chunk marker and extended marker
				require.Equal(t, byte(0x1B), raw[0])
				require.Equal(t, []byte{0xFF, 0xFF, 0xFF}, raw[1:4])
				require.Equal(t,
					[]byte{byte(ca.timestamp >> 24), byte(ca.timestamp >> 16),
						byte(ca.timestamp >> 8), byte(ca.timestamp)},
					raw[12:16])

				// both continuation chunks carry the same extended timestamp
				off := 16 + 128
				require.Equal(t, byte(3<<6|27), raw[off])
				require.Equal(t,
					[]byte{byte(ca.timestamp >> 24), byte(ca.timestamp >> 16),
						byte(ca.timestamp >> 8), byte(ca.timestamp)},
					raw[off+1:off+5])

				off += 5 + 128
				require.Equal(t, byte(3<<6|27), raw[off])
				require.Equal(t,
					[]byte{byte(ca.timestamp >> 24), byte(ca.timestamp >> 16),
						byte(ca.timestamp >> 8), byte(ca.timestamp)},
					raw[off+1:off+5])
			} else {
				require.Equal(t, byte(0x1B), raw[0])
				require.Equal(t,
					[]byte{byte(ca.timestamp >> 16), byte(ca.timestamp >> 8), byte(ca.timestamp)},
					raw[1:4])

				// no extended field: continuation header is a single byte
				off := 12 + 128
				require.Equal(t, byte(3<<6|27), raw[off])
				require.Equal(t, byte(3<<6|27), raw[off+128+1])
			}

			// the message must survive a write/read round trip
			bcr := bytecounter.NewReader(bytes.NewReader(raw))
			r := rawmessage.NewReader(bcr, bcr, nil)
			dec, err := r.Read()
			require.NoError(t, err)
			require.Equal(t, time.Duration(ca.timestamp)*time.Millisecond, dec.Timestamp)
			require.Len(t, dec.Body, 260)
		})
	}
}

func TestWriterExtendedTimestampProgression(t *testing.T) {
	// consecutive split messages across the boundary must keep advancing
	// their timestamps correctly, in both type 1 (delta) and type 2 (delta)
	// header forms, with correct extended values on continuation chunks.
	var buf bytes.Buffer
	bc := bytecounter.NewWriter(&buf)
	w := &rawmessage.Writer{
		BW:               bc,
		BCW:              bc,
		CheckAcknowledge: true,
	}
	w.Initialize()

	messages := []*rawmessage.Message{
		{ // type 0, absolute timestamp at boundary
			ChunkStreamID: 27,
			Timestamp:     0xFFFFFF * time.Millisecond,
			Type:          9,
			Body:          bytes.Repeat([]byte{0x01}, 200),
		},
		{ // type 1 (different body length), delta of 0xFFFFFF
			ChunkStreamID: 27,
			Timestamp:     (0xFFFFFF + 0xFFFFFF) * time.Millisecond,
			Type:          9,
			Body:          bytes.Repeat([]byte{0x02}, 201),
		},
		{ // type 2 (same length), delta of 1
			ChunkStreamID: 27,
			Timestamp:     (0xFFFFFF + 0xFFFFFF + 1) * time.Millisecond,
			Type:          9,
			Body:          bytes.Repeat([]byte{0x03}, 201),
		},
		{ // type 2 (same length), extended delta of 0xFFFFFF
			ChunkStreamID: 27,
			Timestamp:     (0xFFFFFF + 0xFFFFFF + 1 + 0xFFFFFF) * time.Millisecond,
			Type:          9,
			Body:          bytes.Repeat([]byte{0x04}, 201),
		},
	}

	for _, msg := range messages {
		err := w.Write(msg)
		require.NoError(t, err)
	}

	bcr := bytecounter.NewReader(bytes.NewReader(buf.Bytes()))
	r := rawmessage.NewReader(bcr, bcr, nil)

	for _, msg := range messages {
		dec, err := r.Read()
		require.NoError(t, err)
		require.Equal(t, msg.Timestamp, dec.Timestamp)
		require.Equal(t, msg.Body, dec.Body)
	}

	_, err := r.Read()
	require.ErrorIs(t, err, io.EOF)

	// byte-level checks: each continuation chunk must repeat the extended
	// timestamp/delta of the message's first chunk instead of zero bytes.
	raw := buf.Bytes()

	// first message (type 0, len 200): 16 (header) + 128 + 5 (chunk3 header) + 72
	firstEnd := 16 + 128 + 5 + 72
	// second message starts with a type 1: 12 (header) + 128
	contOff := firstEnd + 12 + 128
	require.Equal(t, byte(3<<6|27), raw[contOff])
	require.Equal(t,
		[]byte{0x00, 0xFF, 0xFF, 0xFF},
		raw[contOff+1:contOff+5])

	// second message (len 201): ... + 5 (chunk3 header) + 73
	secondEnd := firstEnd + 12 + 128 + 5 + 73
	// third message (type 2, small delta): 4 (header) + 128 + 1 (chunk3 header) + 73
	thirdEnd := secondEnd + 4 + 128 + 1 + 73
	// fourth message (type 2, extended delta): 8 (header) + 128
	contOff = thirdEnd + 8 + 128
	require.Equal(t, byte(3<<6|27), raw[contOff])
	require.Equal(t,
		[]byte{0x00, 0xFF, 0xFF, 0xFF},
		raw[contOff+1:contOff+5])

	// the type 2 header itself carries the extended delta
	require.Equal(t,
		[]byte{2<<6 | 27, 0xFF, 0xFF, 0xFF, 0x00, 0xFF, 0xFF, 0xFF},
		raw[thirdEnd:thirdEnd+8])
}

func BenchmarkWriter(b *testing.B) {
	bc := bytecounter.NewWriter(io.Discard)
	w := &rawmessage.Writer{
		BW:  bc,
		BCW: bc,
	}
	w.Initialize()
	w.SetChunkSize(4096)

	msg := &rawmessage.Message{
		ChunkStreamID:   6,
		Type:            9,
		MessageStreamID: 1,
		Body:            make([]byte, 20000),
	}

	for b.Loop() {
		msg.Timestamp += 33 * time.Millisecond
		w.Write(msg) //nolint:errcheck
	}
}
