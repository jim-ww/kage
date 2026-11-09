package call

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"
)

// pwDump is a trimmed pw-dump snapshot: our capture node (id 50, mono input
// port 51), a music player's stream (id 60, ports 61/62 + a monitor port
// 63), kage's own PulseAudio playback (id 70, port 71, pid 4242 on the
// node), a native stream of ours too (id 72, port 73, pid only on its
// client 92), a sink device (id 80) and one link that already exists
// (61 -> 51).
const pwDump = `[
 {"id": 50, "type": "PipeWire:Interface:Node", "info": {"props": {"node.name": "kage-desktop-audio-x", "media.class": "Stream/Input/Audio"}}},
 {"id": 51, "type": "PipeWire:Interface:Port", "info": {"direction": "input", "props": {"node.id": 50, "port.name": "input_MONO"}}},
 {"id": 60, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "application.process.id": "1000"}}},
 {"id": 61, "type": "PipeWire:Interface:Port", "info": {"direction": "output", "props": {"node.id": 60, "port.name": "output_FL"}}},
 {"id": 62, "type": "PipeWire:Interface:Port", "info": {"direction": "output", "props": {"node.id": "60", "port.name": "output_FR"}}},
 {"id": 63, "type": "PipeWire:Interface:Port", "info": {"direction": "output", "props": {"node.id": 60, "port.monitor": true}}},
 {"id": 70, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "application.process.id": 4242, "application.name": "kage"}}},
 {"id": 71, "type": "PipeWire:Interface:Port", "info": {"direction": "output", "props": {"node.id": 70}}},
 {"id": 72, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Stream/Output/Audio", "client.id": 92}}},
 {"id": 73, "type": "PipeWire:Interface:Port", "info": {"direction": "output", "props": {"node.id": 72}}},
 {"id": 92, "type": "PipeWire:Interface:Client", "info": {"props": {"application.process.id": 4242}}},
 {"id": 80, "type": "PipeWire:Interface:Node", "info": {"props": {"media.class": "Audio/Sink"}}},
 {"id": 81, "type": "PipeWire:Interface:Port", "info": {"direction": "output", "props": {"node.id": 80}}},
 {"id": 90, "type": "PipeWire:Interface:Link", "info": {"output-port-id": 61, "input-port-id": 51}},
 {"id": 91, "type": "PipeWire:Interface:Client", "info": {"props": {}}}
]`

func TestDesktopAudioLinks(t *testing.T) {
	links, err := desktopAudioLinks([]byte(pwDump), "kage-desktop-audio-x", 4242)
	if err != nil {
		t.Fatal(err)
	}
	// Only the player's FR is missing: FL is linked already, its monitor
	// port, our own streams and the sink device are never sources.
	if want := []portLink{{62, 51}}; !slices.Equal(links, want) {
		t.Fatalf("links %v, want %v", links, want)
	}
}

func TestDesktopAudioLinksBeforeCaptureNodeExists(t *testing.T) {
	if _, err := desktopAudioLinks([]byte(pwDump), "kage-desktop-audio-other", 4242); err == nil {
		t.Fatal("no error for a capture node that isn't in the graph yet")
	}
}

func TestReadPCMFrames(t *testing.T) {
	var raw bytes.Buffer
	for i := range FrameSamples*2 + 10 { // two whole frames and a partial one
		binary.Write(&raw, binary.LittleEndian, int16(i))
	}
	out := make(chan []int16, 4)
	readPCMFrames(&raw, out)
	var frames [][]int16
	for f := range out { // closed once the reader ends
		frames = append(frames, f)
	}
	if len(frames) != 2 || frames[1][0] != FrameSamples || frames[1][FrameSamples-1] != 2*FrameSamples-1 {
		t.Fatalf("got %d frames", len(frames))
	}
}

func TestMixFrameClips(t *testing.T) {
	dst := []int16{30000, -30000, 100}
	MixFrame(dst, []int16{10000, -10000, 23})
	if want := []int16{32767, -32768, 123}; !slices.Equal(dst, want) {
		t.Fatalf("mixed %v, want %v", dst, want)
	}
}

func TestEncoderSetMusic(t *testing.T) {
	enc, err := NewEncoder()
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, FrameSamples)
	for _, music := range []bool{true, false} {
		if err := enc.SetMusic(music); err != nil {
			t.Fatal(err)
		}
		if _, err := enc.Encode(frame); err != nil {
			t.Fatalf("encoding with music=%v: %v", music, err)
		}
	}
}
