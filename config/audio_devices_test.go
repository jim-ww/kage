package config

import "testing"

func TestSetAudioDevices(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SetAudioDevices(path, "USB Mic", "Headphones"); err != nil {
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
	if err := SetAudioDevices(path, "", ""); err != nil {
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
	if err := SetScreenShareAudio(path, false); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ScreenShareAudioDisabled {
		t.Fatal("disabling desktop audio wasn't saved")
	}
	if err := SetScreenShareAudio(path, true); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(path); err != nil || st.ScreenShareAudioDisabled {
		t.Fatalf("re-enabling: disabled=%v err=%v", st.ScreenShareAudioDisabled, err)
	}
}

func TestSetScreenShareAudioVolume(t *testing.T) {
	path := hiddenChatsStatePath(t)
	if err := SetScreenShareAudioVolume(path, 70); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(path)
	if err != nil || st.ScreenShareAudioVolume != 70 {
		t.Fatalf("volume %d err %v", st.ScreenShareAudioVolume, err)
	}
	if err := SetScreenShareAudioVolume(path, 100); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(path); err != nil || st.ScreenShareAudioVolume != 0 {
		t.Fatalf("default not written as unset: %d %v", st.ScreenShareAudioVolume, err)
	}
}
