package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestRemovableDevicesExcludesLocalPerProtocol guards the core correctness
// property of keying by (Protocol, ID) rather than ID alone: the same
// numeric device ID can legitimately appear in both an account's v1 and v2
// device pools (they're independent identities - see account.go), so a
// device must only be treated as "local, never removable" when both its
// protocol and ID match one of this instance's own devices.
func TestRemovableDevicesExcludesLocalPerProtocol(t *testing.T) {
	local := []OmemoDevice{
		{Protocol: "v1", ID: 42},
		{Protocol: "v2", ID: 99},
	}
	devices := []OmemoDevice{
		{Protocol: "v1", ID: 42}, // local v1 device - excluded
		{Protocol: "v2", ID: 42}, // same numeric ID, but v2 - NOT local, must remain removable
		{Protocol: "v2", ID: 99}, // local v2 device - excluded
		{Protocol: "v1", ID: 7},
	}

	got := removableDevices(local, devices)
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
	local := []OmemoDevice{{Protocol: "v2", ID: 99, Fingerprint: ""}}
	devices := []OmemoDevice{{Protocol: "v2", ID: 99, Fingerprint: "aaaaaaaa"}, {Protocol: "v2", ID: 7}}
	if got := removableDevices(local, devices); len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("removable: %+v", got)
	}
}

// fakeDeviceManager answers device-list fetches with a fixed list and
// records purges.
type fakeDeviceManager struct {
	local, devices []OmemoDevice
	purged         [][]OmemoDevice
}

func (f *fakeDeviceManager) FetchOwnDeviceList(accountIdx int) tea.Msg {
	return OmemoDeviceListMsg{AccountIdx: accountIdx, Local: f.local, Devices: f.devices}
}

func (f *fakeDeviceManager) PurgeOwnDeviceList(accountIdx int, keep []OmemoDevice) tea.Msg {
	f.purged = append(f.purged, keep)
	return OmemoDevicePurgedMsg{AccountIdx: accountIdx, Local: f.local, Devices: keep}
}

// openDevices opens the device list for account 0 and delivers manager's
// answer to its fetch.
func openDevices(t *testing.T, manager *fakeDeviceManager) Model {
	t.Helper()
	m := newTestModel(nil)
	m.width, m.height, m.termHeight = 120, 40, 40
	m.accounts = []Account{{}}
	m.updateSizes()
	m.deviceManager = manager
	cmd := m.openDeviceList()
	list, ok := findMsg[OmemoDeviceListMsg](cmd)
	if !ok {
		t.Fatal("opening the device list didn't fetch it")
	}
	m, _, _ = m.handleEventMsg(list)
	return m
}

func TestDeviceListShowsFingerprints(t *testing.T) {
	manager := &fakeDeviceManager{
		local: []OmemoDevice{{Protocol: "v2", ID: 99, Fingerprint: "11111111 22222222 33333333 44444444 55555555 66666666 77777777 88888888"}},
		devices: []OmemoDevice{
			{Protocol: "v2", ID: 99},
			{Protocol: "v2", ID: 7, Fingerprint: "aaaaaaaa bbbbbbbb cccccccc dddddddd eeeeeeee ffffffff 00000000 99999999"},
		},
	}
	m := openDevices(t, manager)
	prompt := ansi.Strip(m.modals.View())
	for _, want := range []string{
		"11111111 22222222 33333333 44444444", "55555555 66666666 77777777 88888888", // local, two lines
		"Device 7 fingerprint:", "aaaaaaaa bbbbbbbb cccccccc dddddddd", // highlighted device
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("device list missing %q:\n%s", want, prompt)
		}
	}

	manager.local[0].Fingerprint = ""
	m = openDevices(t, manager)
	if !strings.Contains(ansi.Strip(m.modals.View()), "fingerprint unavailable") {
		t.Fatal("no placeholder for a fingerprint that couldn't be fetched")
	}
}

// TestDeviceListMarkAndRemove: marking a device (its digit) and pressing y
// asks first, and yes republishes without it.
func TestDeviceListMarkAndRemove(t *testing.T) {
	manager := &fakeDeviceManager{
		local:   []OmemoDevice{{Protocol: "v2", ID: 99}},
		devices: []OmemoDevice{{Protocol: "v2", ID: 99}, {Protocol: "v2", ID: 7}, {Protocol: "v2", ID: 8}},
	}
	m := openDevices(t, manager)
	m = run(t, m, keyText("1"))
	if !strings.Contains(ansi.Strip(m.modals.View()), "[x] Device 7") {
		t.Fatalf("1 didn't mark device 7:\n%s", ansi.Strip(m.modals.View()))
	}
	m = run(t, m, keyText("y"))
	if modalTitle(m) != "Remove 1 device(s) from account?" {
		t.Fatalf("y opened %q, want the question", modalTitle(m))
	}
	next, cmd := m.Update(keyText("y"))
	m = next.(Model)
	purged, ok := findMsg[OmemoDevicePurgedMsg](cmd)
	if !ok {
		t.Fatal("yes didn't republish the device list")
	}
	m, _, _ = m.handleEventMsg(purged)
	if len(manager.purged) != 1 || len(manager.purged[0]) != 2 {
		t.Fatalf("republished %v, want devices 99 and 8 kept", manager.purged)
	}
	if strings.Contains(ansi.Strip(m.modals.View()), "Device 7") {
		t.Fatal("device 7 still listed after removing it")
	}
}
