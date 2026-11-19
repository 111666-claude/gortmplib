package gortmplib_test

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortmplib/pkg/message"
)

// acceptTimedMetadata accepts connections on ln and sends the resulting
// ServerConn to publishers or players, depending on the request.
func acceptTimedMetadata(
	t *testing.T,
	ln net.Listener,
	publishers chan *gortmplib.ServerConn,
	players chan *gortmplib.ServerConn,
) {
	t.Helper()

	go func() {
		for {
			nconn, err := ln.Accept()
			if err != nil {
				return
			}

			sc := &gortmplib.ServerConn{RW: nconn}
			err = sc.Initialize()
			if err != nil {
				return
			}

			err = sc.AcceptConn()
			if err != nil {
				return
			}

			err = sc.AcceptAction()
			if err != nil {
				return
			}

			if sc.Publish {
				publishers <- sc
			} else {
				players <- sc
			}
		}
	}()
}

// TestTimedMetadataPublishPlay publishes and plays through two local
// connections and verifies that:
//   - two timed metadata updates are delivered to the player in order,
//     without losing content;
//   - audio and video frames are passed through unchanged;
//   - the initial onMetaData is still sent.
func TestTimedMetadataPublishPlay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:9124")
	require.NoError(t, err)
	defer ln.Close()

	publishers := make(chan *gortmplib.ServerConn, 1)
	players := make(chan *gortmplib.ServerConn, 1)

	acceptTimedMetadata(t, ln, publishers, players)

	// publisher
	pubURL, err := url.Parse("rtmp://127.0.0.1:9124/stream")
	require.NoError(t, err)

	pub := &gortmplib.Client{URL: pubURL, Publish: true}
	err = pub.Initialize(context.Background())
	require.NoError(t, err)
	defer pub.Close()

	var pubServer *gortmplib.ServerConn
	select {
	case pubServer = <-publishers:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher connection not accepted")
	}

	pubTracks := []*gortmplib.Track{
		{Codec: testCodecH264},
		{Codec: &codecs.MPEG4Audio{
			Config: &mpeg4audio.AudioSpecificConfig{
				Type:          mpeg4audio.ObjectTypeAACLC,
				SampleRate:    44100,
				ChannelConfig: 2,
				ChannelCount:  2, //nolint:staticcheck
			},
		}},
	}

	pubWriter := &gortmplib.Writer{Conn: pub, Tracks: pubTracks}
	err = pubWriter.Initialize()
	require.NoError(t, err)

	// batch 1: enough media to let Reader.Initialize() complete.
	batch1 := func() error {
		for i := range 3 {
			pts := time.Duration(i) * time.Second

			err2 := pubWriter.WriteH264(pubTracks[0], pts, pts,
				[][]byte{{h264NALTypeForTest(i), 0x01}})
			if err2 != nil {
				return err2
			}

			err2 = pubWriter.WriteMPEG4Audio(pubTracks[1], pts, []byte{byte(i), 0x02})
			if err2 != nil {
				return err2
			}
		}
		return nil
	}
	require.NoError(t, batch1())

	// reader on the server side
	reader := &gortmplib.Reader{Conn: pubServer}
	err = reader.Initialize()
	require.NoError(t, err)

	tracks := reader.Tracks()
	require.Len(t, tracks, 2)

	// player
	playURL, err := url.Parse("rtmp://127.0.0.1:9124/stream")
	require.NoError(t, err)

	play := &gortmplib.Client{URL: playURL}
	err = play.Initialize(context.Background())
	require.NoError(t, err)
	defer play.Close()

	var playServer *gortmplib.ServerConn
	select {
	case playServer = <-players:
	case <-time.After(5 * time.Second):
		t.Fatal("player connection not accepted")
	}

	playWriter := &gortmplib.Writer{Conn: playServer, Tracks: tracks}
	err = playWriter.Initialize()
	require.NoError(t, err)

	// forward the stream to the player, including data events.
	var videoPTS []time.Duration
	var audioPayloads [][]byte

	reader.OnDataAMF0Raw(func(timestamp time.Duration, payload amf0.Data) {
		// the initial metadata has timestamp 0 and is replayed by
		// Initialize(); the writer already sends its own initial metadata,
		// so forward only events that belong to the live stream.
		if timestamp == 0 {
			return
		}

		playWriter.WriteDataAMF0Raw(timestamp, payload) //nolint:errcheck
	})
	reader.OnDataH264(tracks[0], func(pts time.Duration, dts time.Duration, au [][]byte) {
		if dts >= 3*time.Second {
			videoPTS = append(videoPTS, dts)
		}
		playWriter.WriteH264(tracks[0], pts, dts, au) //nolint:errcheck
	})
	reader.OnDataMPEG4Audio(tracks[1], func(pts time.Duration, au []byte) {
		if pts >= 3*time.Second {
			audioPayloads = append(audioPayloads, append([]byte(nil), au...))
		}
		playWriter.WriteMPEG4Audio(tracks[1], pts, au) //nolint:errcheck
	})

	readDone := make(chan error, 1)
	go func() {
		for {
			err2 := reader.Read()
			if err2 != nil {
				readDone <- err2
				return
			}
		}
	}()

	// batch 2: two metadata updates interleaved with audio and video.
	type update struct {
		timestamp time.Duration
		metadata  amf0.Object
	}

	updates := []update{
		{
			3500 * time.Millisecond,
			amf0.Object{
				{Key: "title", Value: "second track"},
				{Key: "artist", Value: "artist 2"},
			},
		},
		{
			4500 * time.Millisecond,
			amf0.Object{
				{Key: "title", Value: "third track"},
				{Key: "tags", Value: amf0.StrictArray{"a", "b"}},
			},
		},
	}

	batch2 := func() error {
		for i, upd := range updates {
			base := time.Duration(3+i) * time.Second

			err2 := pubWriter.WriteH264(pubTracks[0], base, base,
				[][]byte{{h264NALTypeForTest(i), 0x03}})
			if err2 != nil {
				return err2
			}

			err2 = pubWriter.WriteMetadata(base+500*time.Millisecond, upd.metadata)
			if err2 != nil {
				return err2
			}

			err2 = pubWriter.WriteMPEG4Audio(pubTracks[1], base+500*time.Millisecond,
				[]byte{byte(i), 0x04})
			if err2 != nil {
				return err2
			}
		}
		return nil
	}
	require.NoError(t, batch2())

	// player side: read messages from the connection.
	var initialMetadata *message.DataAMF0
	var receivedUpdates []*message.DataAMF0
	var videoCount, audioCount int
	var videoAUs [][]byte
	var audioAUs [][]byte

	// sequence records the order of data events and media frames with
	// their timestamps, to verify that interleaving is preserved.
	var sequence []sequenceEntry

	deadline := time.Now().Add(10 * time.Second)
	err = play.NetConn().SetReadDeadline(deadline)
	require.NoError(t, err)

	// keep reading until both updates and the last audio frame have
	// been received, since they are written in this order.
	for len(receivedUpdates) < len(updates) ||
		indexOfSequence(sequence, "audio", updates[len(updates)-1].timestamp) < 0 {
		msg, err2 := play.Read()
		if err2 != nil {
			t.Fatalf("player read failed after %d updates: %v", len(receivedUpdates), err2)
		}

		switch msg := msg.(type) {
		case *message.DataAMF0:
			if initialMetadata == nil {
				initialMetadata = msg
				continue
			}

			receivedUpdates = append(receivedUpdates, msg)
			sequence = append(sequence, sequenceEntry{"data", msg.Timestamp})

		case *message.Video:
			videoCount++

			if msg.PacketType == message.VideoPacketTypeAU {
				videoAUs = append(videoAUs, append([]byte(nil), msg.AU...))
				sequence = append(sequence, sequenceEntry{"video", msg.DTS})
			}

		case *message.Audio:
			if msg.Codec != 0 && msg.AACType == message.AudioAACTypeAU {
				audioCount++
				audioAUs = append(audioAUs, append([]byte(nil), msg.AU...))
				sequence = append(sequence, sequenceEntry{"audio", msg.DTS})
			}
		}
	}

	// the initial onMetaData is still sent by the writer.
	require.NotNil(t, initialMetadata)
	require.Equal(t, amf0.Data{"@setDataFrame", "onMetaData", amf0.Object{
		{Key: "videocodecid", Value: float64(message.CodecH264)},
		{Key: "videodatarate", Value: float64(0)},
		{Key: "audiocodecid", Value: float64(message.CodecMPEG4Audio)},
		{Key: "audiodatarate", Value: float64(0)},
	}}, initialMetadata.Payload)

	// both updates are received in order, with the original content.
	require.Len(t, receivedUpdates, 2)

	for i, upd := range updates {
		require.Equal(t, upd.timestamp, receivedUpdates[i].Timestamp)
		require.Equal(t, amf0.Data{"@setDataFrame", "onMetaData", upd.metadata}, receivedUpdates[i].Payload)
	}

	// audio and video are passed through byte by byte.
	require.GreaterOrEqual(t, videoCount, 2)
	require.GreaterOrEqual(t, audioCount, 2)

	require.Contains(t, videoAUs, []byte{
		0x00, 0x00, 0x00, 0x02, h264NALTypeForTest(0), 0x03,
	})
	require.Contains(t, videoAUs, []byte{
		0x00, 0x00, 0x00, 0x02, h264NALTypeForTest(1), 0x03,
	})

	for i := range updates {
		require.Contains(t, audioAUs, []byte{byte(i), 0x04})
	}

	// interleaving is preserved: each update lies between the video frame
	// that precedes it and the audio frame that follows it.
	for i, upd := range updates {
		base := time.Duration(3+i) * time.Second

		videoIdx := indexOfSequence(sequence, "video", base)
		dataIdx := indexOfSequence(sequence, "data", upd.timestamp)
		audioIdx := indexOfSequence(sequence, "audio", base+500*time.Millisecond)

		require.GreaterOrEqual(t, videoIdx, 0)
		require.GreaterOrEqual(t, dataIdx, 0)
		require.GreaterOrEqual(t, audioIdx, 0)

		require.Less(t, videoIdx, dataIdx)
		require.Less(t, dataIdx, audioIdx)
	}

	// close the publisher and let the server detect the disconnection.
	pub.Close()

	select {
	case err = <-readDone:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not detect the disconnection")
	}

	require.NotEmpty(t, videoPTS)
	require.NotEmpty(t, audioPayloads)
}

func h264NALTypeForTest(i int) byte {
	if i == 0 {
		return 5
	}
	return 1
}

// sequenceEntry is an entry of the media event sequence.
type sequenceEntry struct {
	kind      string
	timestamp time.Duration
}

func indexOfSequence(entries []sequenceEntry, kind string, timestamp time.Duration) int {
	for i, entry := range entries {
		if entry.kind == kind && entry.timestamp == timestamp {
			return i
		}
	}
	return -1
}
