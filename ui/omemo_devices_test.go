package ui

import (
	"strings"
	"testing"
)

// TestRemovableDevicesExcludesLocalPerProtocol guards the core correctness
// property of keying by (Protocol, ID) rather than ID alone: the same
// numeric device ID can legitimately appear in both an account's v1 and v2
// device pools (they're independent identities - see account.go), so a
// device must only be treated as "local, never removable" when both its
// protocol and ID match one of this instance's own devices.
func TestRemovableDevicesExcludesLocalPerProtocol(t *testing.T) {
	dl := &deviceListState{
		local: []OmemoDevice{
			{Protocol: "v1", ID: 42},
			{Protocol: "v2", ID: 99},
		},
		devices: []OmemoDevice{
			{Protocol: "v1", ID: 42}, // local v1 device - excluded
			{Protocol: "v2", ID: 42}, // same numeric ID, but v2 - NOT local, must remain removable
			{Protocol: "v2", ID: 99}, // local v2 device - excluded
			{Protocol: "v1", ID: 7},
		},
	}

	got := dl.removableDevices()
	want := []OmemoDevice{{Protocol: "v1", ID: 7}, {Protocol: "v2", ID: 42}}
	if len(got) != len(want) {
		t.Fatalf("removableDevices() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("removableDevices()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestLocalDeviceMatchesWhateverItsFingerprint checks a local device whose
// fingerprint couldn't be fetched (or differs) in one of the two listings
// still counts as local - never offered for removal.
func TestLocalDeviceMatchesWhateverItsFingerprint(t *testing.T) {
	dl := &deviceListState{
		local:   []OmemoDevice{{Protocol: "v2", ID: 99, Fingerprint: ""}},
		devices: []OmemoDevice{{Protocol: "v2", ID: 99, Fingerprint: "aaaaaaaa"}, {Protocol: "v2", ID: 7}},
	}
	if got := dl.removableDevices(); len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("removable: %+v", got)
	}
}

func TestDeviceListShowsFingerprints(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 40
	m.updateSizes()
	m.deviceList = &deviceListState{
		local: []OmemoDevice{{Protocol: "v2", ID: 99, Fingerprint: "11111111 22222222 33333333 44444444 55555555 66666666 77777777 88888888"}},
		devices: []OmemoDevice{
			{Protocol: "v2", ID: 99},
			{Protocol: "v2", ID: 7, Fingerprint: "aaaaaaaa bbbbbbbb cccccccc dddddddd eeeeeeee ffffffff 00000000 99999999"},
		},
		selected: map[OmemoDevice]bool{},
	}
	prompt := m.deviceListPrompt()
	for _, want := range []string{
		"11111111 22222222 33333333 44444444", "55555555 66666666 77777777 88888888", // local, two lines
		"Device 7 fingerprint:", "aaaaaaaa bbbbbbbb cccccccc dddddddd", // highlighted device
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("device list missing %q:\n%s", want, prompt)
		}
	}

	m.deviceList.local[0].Fingerprint = ""
	if !strings.Contains(m.deviceListPrompt(), "fingerprint unavailable") {
		t.Fatal("no placeholder for a fingerprint that couldn't be fetched")
	}
}
