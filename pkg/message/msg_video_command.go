package message

import (
	"fmt"
	"time"

	"github.com/bluenviron/gortmplib/pkg/rawmessage"
)

// videoFrameTypeCommand is the FLV FrameType (upper nibble of the first
// body byte) of video info/command frames.
const videoFrameTypeCommand = 5

// VideoCommandType is the command of a VideoCommand message.
type VideoCommandType uint8

// VideoCommandType values.
const (
	VideoCommandTypeStartSeek VideoCommandType = 0
	VideoCommandTypeEndSeek   VideoCommandType = 1
)

// VideoCommand is a video info/command frame (FLV FrameType = 5).
// It does not contain any media data and must not be confused with a
// key frame or any other video frame.
type VideoCommand struct {
	ChunkStreamID   byte
	DTS             time.Duration
	MessageStreamID uint32

	// Codec is the low nibble of the first body byte (CodecID).
	// It is 0 for a plain video command frame, but some senders set it
	// to the codec of the stream the command refers to (e.g. 7 for H264).
	Codec uint8

	// Command is the video command (second body byte).
	Command VideoCommandType
}

func (m *VideoCommand) unmarshal(raw *rawmessage.Message) error {
	m.ChunkStreamID = raw.ChunkStreamID
	m.DTS = raw.Timestamp
	m.MessageStreamID = raw.MessageStreamID

	if len(raw.Body) != 2 {
		return fmt.Errorf("invalid body size")
	}

	if raw.Body[0]>>4 != videoFrameTypeCommand {
		return fmt.Errorf("not a video command frame")
	}

	m.Codec = raw.Body[0] & 0x0F
	m.Command = VideoCommandType(raw.Body[1])

	switch m.Command {
	case VideoCommandTypeStartSeek, VideoCommandTypeEndSeek:
	default:
		return fmt.Errorf("unsupported video command: %d", m.Command)
	}

	return nil
}

func (m VideoCommand) marshal() (*rawmessage.Message, error) {
	switch m.Command {
	case VideoCommandTypeStartSeek, VideoCommandTypeEndSeek:
	default:
		return nil, fmt.Errorf("unsupported video command: %d", m.Command)
	}

	body := []byte{
		videoFrameTypeCommand<<4 | (m.Codec & 0x0F),
		uint8(m.Command),
	}

	return &rawmessage.Message{
		ChunkStreamID:   m.ChunkStreamID,
		Timestamp:       m.DTS,
		Type:            uint8(TypeVideo),
		MessageStreamID: m.MessageStreamID,
		Body:            body,
	}, nil
}
