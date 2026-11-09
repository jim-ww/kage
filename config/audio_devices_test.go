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
