//go:build cgo

package call

import (
	"testing"
	"time"
)

// useNullAudio runs the test against miniaudio's null backend - real device
// plumbing (contexts, callbacks, enumeration) without sound hardware.
func useNullAudio(t *testing.T) {
	t.Helper()
	t.Cleanup(UseNullAudio())
}

func TestAudioDevicesListsNullDevices(t *testing.T) {
	useNullAudio(t)
	inputs, outputs, err := AudioDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		t.Fatalf("inputs %v outputs %v, want at least one of each", inputs, outputs)
	}
}

// TestSpeakerPlaysAndCloses checks the device pulls queued frames (Write
// doesn't wedge once the queue's depth is exceeded) and that Close unblocks
// a pending Write.
func TestSpeakerPlaysAndCloses(t *testing.T) {
	useNullAudio(t)
	spk, err := NewSpeaker("")
	if err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		for range 20 { // more than the queue holds: only drains if playback runs
			if err := spk.Write(make([]int16, FrameSamples)); err != nil {
				written <- err
				return
			}
		}
		written <- nil
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("playback never drained the queue")
	}
	spk.Close()
	if err := spk.Write(make([]int16, FrameSamples)); err == nil {
		t.Fatal("Write after Close succeeded")
	}
	spk.Close() // idempotent
}

// TestUnknownDeviceFallsBackToDefault checks a picked device that's gone
// opens the default instead of failing the call.
func TestUnknownDeviceFallsBackToDefault(t *testing.T) {
	useNullAudio(t)
	spk, err := NewSpeaker("unplugged headset")
	if err != nil {
		t.Fatalf("speaker: %v", err)
	}
	if !spk.UsingDefaultInstead() {
		t.Error("speaker fallback not reported")
	}
	spk.Close()
	mic, err := NewMic("unplugged headset")
	if err != nil {
		t.Fatalf("mic: %v", err)
	}
	defer mic.Close()
	if !mic.UsingDefaultInstead() {
		t.Error("mic fallback not reported")
	}
	select {
	case <-mic.Frames():
	case <-time.After(5 * time.Second):
		t.Fatal("no frames captured")
	}
}

func TestNamedDeviceOpens(t *testing.T) {
	useNullAudio(t)
	_, outputs, err := AudioDevices()
	if err != nil || len(outputs) == 0 {
		t.Fatalf("listing: %v %v", outputs, err)
	}
	spk, err := NewSpeaker(outputs[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if spk.UsingDefaultInstead() {
		t.Error("a device that exists reported as a fallback")
	}
	spk.Close()
}
