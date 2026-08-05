package ui

import "testing"

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
