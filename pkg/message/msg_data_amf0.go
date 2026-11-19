package message

import (
	"fmt"
	"time"

	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/rawmessage"
)

// MaxDataAMF0Size is the maximum size of the payload of a DataAMF0 message.
// AMF0 values bigger than this are rejected by both the reader and the writer,
// so that a malicious or broken peer cannot allocate arbitrary memory.
const MaxDataAMF0Size = 1024 * 1024

// DataAMF0 is a AMF0 data message.
type DataAMF0 struct {
	ChunkStreamID   byte
	Timestamp       time.Duration
	MessageStreamID uint32
	Payload         amf0.Data
}

func (m *DataAMF0) unmarshal(raw *rawmessage.Message) error {
	m.ChunkStreamID = raw.ChunkStreamID
	m.Timestamp = raw.Timestamp
	m.MessageStreamID = raw.MessageStreamID

	if len(raw.Body) > MaxDataAMF0Size {
		return fmt.Errorf("payload size (%d) exceeds maximum (%d)", len(raw.Body), MaxDataAMF0Size)
	}

	var err error
	m.Payload, err = amf0.Unmarshal(raw.Body)
	if err != nil {
		return err
	}

	return nil
}

func (m DataAMF0) marshal() (*rawmessage.Message, error) {
	// compute the size before allocating the buffer, so that oversized
	// values are rejected without allocating memory for them.
	size, err := m.Payload.MarshalSize()
	if err != nil {
		return nil, err
	}

	if size > MaxDataAMF0Size {
		return nil, fmt.Errorf("payload size (%d) exceeds maximum (%d)", size, MaxDataAMF0Size)
	}

	body, err := m.Payload.Marshal()
	if err != nil {
		return nil, err
	}

	return &rawmessage.Message{
		ChunkStreamID:   m.ChunkStreamID,
		Timestamp:       m.Timestamp,
		Type:            uint8(TypeDataAMF0),
		MessageStreamID: m.MessageStreamID,
		Body:            body,
	}, nil
}
