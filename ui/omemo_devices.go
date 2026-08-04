package ui

import (
	"fmt"
	"sort"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// OmemoDeviceManager fetches and republishes our own account's OMEMO device
// list (XEP-0384 PEP), implemented outside ui (main.go's adapter) so ui
// stays decoupled from xmpp/omemo. Both run as a tea.Cmd like
// AccountAdder.AddAccount since they're network I/O.
type OmemoDeviceManager interface {
	FetchOwnDeviceList(accountIdx int) tea.Msg
	PurgeOwnDeviceList(accountIdx int, keep []uint32) tea.Msg
}

// OmemoDeviceListMsg reports the result of OmemoDeviceManager.FetchOwnDeviceList.
type OmemoDeviceListMsg struct {
	AccountIdx int
	Local      uint32
	Devices    []uint32
	Err        error
}

// OmemoDevicePurgedMsg reports the result of OmemoDeviceManager.PurgeOwnDeviceList.
type OmemoDevicePurgedMsg struct {
	AccountIdx int
	Local      uint32
	Devices    []uint32
	Err        error
}

// deviceListState holds the state of the "view/purge OMEMO devices" popup,
// active while non-nil. There's no way to know a sibling device's last-used
// time (or anything else about it) from here — kage is just one of
// potentially several clients on the account, and that data lives on the
// device that used it, not on the server or in this instance. So all this
// shows is the device ID itself and whether it's the one kage is currently
// running as.
type deviceListState struct {
	accountIdx int
	local      uint32 // this instance's own device ID; never selectable for removal
	devices    []uint32
	selected   map[uint32]bool // devices marked for removal
	busy       bool
	err        string
	confirming bool // true while showing the y/n "remove N device(s)?" confirmation
}

// openDeviceList opens the device-list popup for the current account and
// kicks off a fetch of its published OMEMO device list.
func (m Model) openDeviceList() (Model, tea.Cmd) {
	if m.deviceManager == nil {
		return m, m.showNotification("omemo device management unavailable")
	}
	accountIdx := m.currentAccount
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return m, nil
	}
	if m.accounts[accountIdx].Connecting {
		return m, m.showNotification("account is still connecting, try again shortly")
	}
	m.deviceList = &deviceListState{accountIdx: accountIdx, busy: true, selected: map[uint32]bool{}}
	return m, func() tea.Msg { return m.deviceManager.FetchOwnDeviceList(accountIdx) }
}

// sortedDevices returns dl.devices sorted with the local device first, then
// ascending by ID — a stable, predictable numbering for the popup.
func (dl *deviceListState) sortedDevices() []uint32 {
	out := append([]uint32(nil), dl.devices...)
	sort.Slice(out, func(i, j int) bool {
		if out[i] == dl.local {
			return true
		}
		if out[j] == dl.local {
			return false
		}
		return out[i] < out[j]
	})
	return out
}

func (m Model) renderDeviceListPopup() string {
	cw := m.chatAreaWidth()
	vh := m.height - m.inputAreaHeight()

	popup := m.styles.popupDialog(m.styles.colors.borderA, m.deviceListPrompt())
	return lipgloss.Place(cw, vh, lipgloss.Center, lipgloss.Center, popup)
}

func (m Model) deviceListPrompt() string {
	dl := m.deviceList
	if dl == nil {
		return ""
	}
	closeKey := m.keys.DeviceList.Help().Key
	if dl.err != "" {
		return m.styles.infoPopup("OMEMO devices", []string{"Error: " + dl.err}, closeKey)
	}
	if dl.busy {
		return m.styles.infoPopup("OMEMO devices", []string{"Loading…"}, closeKey)
	}

	devices := dl.sortedDevices()
	if dl.confirming {
		n := 0
		for _, d := range devices {
			if dl.selected[d] {
				n++
			}
		}
		return m.styles.deletePrompt(fmt.Sprintf("Remove %d device(s) from account?", n), "This republishes the account's device list without them.")
	}

	rows := make([]string, len(devices))
	n := 0
	for i, d := range devices {
		label := fmt.Sprintf("Device %d", d)
		if d == dl.local {
			rows[i] = fmt.Sprintf("    %s (this device)", label)
			continue
		}
		n++
		mark := "[ ]"
		if dl.selected[d] {
			mark = "[x]"
		}
		rows[i] = fmt.Sprintf("%d. %s %s", n, mark, label)
	}
	rows = append(rows, "", "digit: toggle · y: remove selected")

	return m.styles.infoPopup("OMEMO devices", rows, closeKey)
}

// updateDeviceListKey handles all input while the device-list popup
// (m.deviceList != nil) is open.
func (m Model) updateDeviceListKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	dl := m.deviceList

	if dl.busy {
		return m, nil, true
	}

	if dl.err != "" {
		if key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.ConfirmNo) || key.Matches(msg, m.keys.DeviceList) {
			m.deviceList = nil
		}
		return m, nil, true
	}

	if dl.confirming {
		switch {
		case key.Matches(msg, m.keys.ConfirmYes):
			var keep []uint32
			for _, d := range dl.devices {
				if !dl.selected[d] {
					keep = append(keep, d)
				}
			}
			accountIdx := dl.accountIdx
			dl.busy = true
			dl.confirming = false
			return m, func() tea.Msg { return m.deviceManager.PurgeOwnDeviceList(accountIdx, keep) }, true
		case key.Matches(msg, m.keys.ConfirmNo):
			dl.confirming = false
		}
		return m, nil, true
	}

	devices := dl.sortedDevices()
	if i, ok := digitKey(msg); ok {
		// Row numbering skips the local device (never removable), so map
		// the 1-based digit back through sortedDevices accounting for that.
		idx := 0
		for _, d := range devices {
			if d == dl.local {
				continue
			}
			idx++
			if idx == i {
				dl.selected[d] = !dl.selected[d]
				break
			}
		}
		return m, nil, true
	}

	switch {
	case key.Matches(msg, m.keys.ConfirmYes):
		anySelected := false
		for _, v := range dl.selected {
			if v {
				anySelected = true
				break
			}
		}
		if anySelected {
			dl.confirming = true
		}
	case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.ConfirmNo), key.Matches(msg, m.keys.DeviceList):
		m.deviceList = nil
	}
	return m, nil, true
}
