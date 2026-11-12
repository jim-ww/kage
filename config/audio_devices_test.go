package config

import (
	"testing"

	"github.com/jim-ww/kage/ui"
)

func TestSetAudioDevices(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SaveState(path, ui.AudioDeviceChoice{Name: "USB Mic"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(path, ui.AudioDeviceChoice{Output: true, Name: "Headphones"}); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.AudioInput != "USB Mic" || st.AudioOutput != "Headphones" {
		t.Fatalf("got input %q output %q", st.AudioInput, st.AudioOutput)
	}

	// Back to the system default clears them.
	if err := SaveState(path, ui.AudioDeviceChoice{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(path, ui.AudioDeviceChoice{Output: true}); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(path); err != nil {
		t.Fatal(err)
	}
	if st.AudioInput != "" || st.AudioOutput != "" {
		t.Fatalf("defaults not cleared: input %q output %q", st.AudioInput, st.AudioOutput)
	}
}

func TestSetScreenShareAudio(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SaveState(path, ui.DesktopAudio(false)); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ScreenShareAudioDisabled {
		t.Fatal("disabling desktop audio wasn't saved")
	}
	if err := SaveState(path, ui.DesktopAudio(true)); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(path); err != nil || st.ScreenShareAudioDisabled {
		t.Fatalf("re-enabling: disabled=%v err=%v", st.ScreenShareAudioDisabled, err)
	}
}

func TestSetScreenShareAudioVolume(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SaveState(path, ui.DesktopAudioVolume(70)); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil || st.ScreenShareAudioVolume != 70 {
		t.Fatalf("volume %d err %v", st.ScreenShareAudioVolume, err)
	}
	if err := SaveState(path, ui.DesktopAudioVolume(100)); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(path); err != nil || st.ScreenShareAudioVolume != 0 {
		t.Fatalf("default not written as unset: %d %v", st.ScreenShareAudioVolume, err)
	}
}
