package message

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/bluenviron/gortmplib/pkg/rawmessage"
)

const (
	// AggregateChunkStreamID is the chunk stream ID that is usually used to send Aggregate{}.
	AggregateChunkStreamID = 4

	// flvTagHeaderSize is the size of an FLV tag header embedded in an aggregate message.
	flvTagHeaderSize = 11

	// flvPreviousTagSizeSize is the size of the PreviousTagSize field following an FLV tag.
	flvPreviousTagSizeSize = 4

	// flvMaxTagDataSize is the maximum size of an FLV tag data field (24-bit length).
	flvMaxTagDataSize = 0xFFFFFF
)

// Aggregate is a RTMP aggregate message (message type 22).
// It contains a sequence of media messages sharing the same message stream ID,
// encoded as back-to-back FLV tags.
//
// On reading, aggregate messages are transparently split: Reader.Read() returns
// the contained messages one by one, in their original order and with absolute
// timestamps, and never returns an Aggregate.
type Aggregate struct {
	ChunkStreamID   byte
	MessageStreamID uint32

	// Messages is the list of contained messages.
	// Only Audio, Video and DataAMF0 messages are supported, they must all
	// share MessageStreamID with the aggregate, and their timestamps must
	// not go backwards.
	// The ChunkStreamID of the contained messages is ignored:
	// the aggregate one is used instead.
	Messages []Message
}

func aggregateTagType(msg Message) (uint8, error) {
	switch msg.(type) {
	case *Audio:
		return uint8(TypeAudio), nil

	case *Video:
		return uint8(TypeVideo), nil

	case *DataAMF0:
		return uint8(TypeDataAMF0), nil

	default:
		return 0, fmt.Errorf("aggregate messages can only contain audio, video or data messages, got %T", msg)
	}
}

// aggregateMessageTimestamp returns the timestamp of a message that can be
// embedded in an aggregate. DataAMF0 messages do not carry a timestamp.
func aggregateMessageTimestamp(msg Message) (time.Duration, bool) {
	switch m := msg.(type) {
	case *Audio:
		return m.DTS, true

	case *Video:
		return m.DTS, true

	default:
		return 0, false
	}
}

// aggregateMessageStreamID returns the message stream ID of an embeddable message.
func aggregateMessageStreamID(msg Message) uint32 {
	switch m := msg.(type) {
	case *Audio:
		return m.MessageStreamID

	case *Video:
		return m.MessageStreamID

	default: // *DataAMF0
		return m.(*DataAMF0).MessageStreamID
	}
}

func (m *Aggregate) unmarshal(raw *rawmessage.Message) error {
	m.ChunkStreamID = raw.ChunkStreamID
	m.MessageStreamID = raw.MessageStreamID

	body := raw.Body
	var lastTimestampMS int64
	first := true

	for len(body) != 0 {
		if len(body) < flvTagHeaderSize {
			return fmt.Errorf("aggregate message is truncated: not enough bytes for tag header")
		}

		tagType := body[0]
		switch Type(tagType) {
		case TypeAudio, TypeVideo, TypeDataAMF0:
		default:
			return fmt.Errorf("aggregate message contains unsupported tag type: %d", tagType)
		}

		dataSize := int(uint32(body[1])<<16 | uint32(body[2])<<8 | uint32(body[3]))
		timestamp := uint32(body[7])<<24 | uint32(body[4])<<16 |
			uint32(body[5])<<8 | uint32(body[6])

		tagEnd := flvTagHeaderSize + dataSize
		if len(body) < tagEnd+flvPreviousTagSizeSize {
			return fmt.Errorf("aggregate message is truncated: not enough bytes for tag data or previous tag size")
		}

		previousTagSize := binary.BigEndian.Uint32(body[tagEnd : tagEnd+flvPreviousTagSizeSize])
		if previousTagSize != uint32(flvTagHeaderSize+dataSize) {
			return fmt.Errorf(
				"aggregate message has invalid previous tag size: expected %d, got %d",
				flvTagHeaderSize+dataSize, previousTagSize)
		}

		if !first && int64(timestamp) < lastTimestampMS {
			return fmt.Errorf("aggregate message contains a tag with a timestamp going backwards")
		}
		lastTimestampMS = int64(timestamp)
		first = false

		subRaw := &rawmessage.Message{
			ChunkStreamID:   raw.ChunkStreamID,
			Timestamp:       raw.Timestamp + time.Duration(timestamp)*time.Millisecond,
			Type:            tagType,
			MessageStreamID: raw.MessageStreamID,
			Body:            body[flvTagHeaderSize:tagEnd],
		}

		subMsg, err := allocateMessage(subRaw)
		if err != nil {
			return err
		}

		err = subMsg.unmarshal(subRaw)
		if err != nil {
			return err
		}

		m.Messages = append(m.Messages, subMsg)
		body = body[tagEnd+flvPreviousTagSizeSize:]
	}

	if len(m.Messages) == 0 {
		return fmt.Errorf("aggregate message is empty")
	}

	return nil
}

func (m Aggregate) marshal() (*rawmessage.Message, error) {
	if len(m.Messages) == 0 {
		return nil, fmt.Errorf("aggregate message must contain at least one message")
	}

	type subEntry struct {
		tagType uint8
		raw     *rawmessage.Message
		offset  uint32
	}

	entries := make([]subEntry, len(m.Messages))
	var baseTimestampMS int64
	var lastTimestampMS int64
	totalSize := 0

	// validate and marshal all messages first, in order to fail before writing
	for i, msg := range m.Messages {
		tagType, err := aggregateTagType(msg)
		if err != nil {
			return nil, err
		}

		if subStreamID := aggregateMessageStreamID(msg); subStreamID != m.MessageStreamID {
			return nil, fmt.Errorf(
				"aggregate sub-message stream ID (%d) does not match aggregate stream ID (%d)",
				subStreamID, m.MessageStreamID)
		}

		raw, err := msg.marshal()
		if err != nil {
			return nil, err
		}

		if len(raw.Body) > flvMaxTagDataSize {
			return nil, fmt.Errorf("aggregate sub-message body size (%d) exceeds maximum (%d)",
				len(raw.Body), flvMaxTagDataSize)
		}

		if i == 0 {
			if ts, ok := aggregateMessageTimestamp(msg); ok {
				baseTimestampMS = int64(ts / time.Millisecond)
			}
			lastTimestampMS = baseTimestampMS
		} else if ts, ok := aggregateMessageTimestamp(msg); ok {
			tsMS := int64(ts / time.Millisecond)
			if tsMS < lastTimestampMS {
				return nil, fmt.Errorf("aggregate message contains a message with a timestamp going backwards")
			}
			lastTimestampMS = tsMS
		}

		entries[i] = subEntry{
			tagType: tagType,
			raw:     raw,
			offset:  uint32(lastTimestampMS - baseTimestampMS),
		}
		totalSize += flvTagHeaderSize + len(raw.Body) + flvPreviousTagSizeSize
	}

	body := make([]byte, totalSize)
	pos := 0

	for _, entry := range entries {
		dataSize := len(entry.raw.Body)

		body[pos] = entry.tagType
		body[pos+1] = byte(dataSize >> 16)
		body[pos+2] = byte(dataSize >> 8)
		body[pos+3] = byte(dataSize)
		body[pos+4] = byte(entry.offset >> 16)
		body[pos+5] = byte(entry.offset >> 8)
		body[pos+6] = byte(entry.offset)
		body[pos+7] = byte(entry.offset >> 24)
		// bytes 8..10 are the stream ID, always 0 in an FLV tag
		copy(body[pos+flvTagHeaderSize:], entry.raw.Body)

		binary.BigEndian.PutUint32(
			body[pos+flvTagHeaderSize+dataSize:],
			uint32(flvTagHeaderSize+dataSize))

		pos += flvTagHeaderSize + dataSize + flvPreviousTagSizeSize
	}

	return &rawmessage.Message{
		ChunkStreamID:   m.ChunkStreamID,
		Timestamp:       time.Duration(baseTimestampMS) * time.Millisecond,
		Type:            uint8(TypeAggregate),
		MessageStreamID: m.MessageStreamID,
		Body:            body,
	}, nil
}
