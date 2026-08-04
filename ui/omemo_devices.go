package ui

import (
	"fmt"
	"slices"

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
	page       int             // current page (of openItemsPerPage) through the removable devices
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

// removableDevices returns dl.devices minus the local device, sorted
// ascending by ID — a stable, predictable numbering for the popup's
// paginated, selectable rows. The local device is always shown separately,
// pinned above the page and never counted against it (it's never
// removable).
func (dl *deviceListState) removableDevices() []uint32 {
	out := make([]uint32, 0, len(dl.devices))
	for _, d := range dl.devices {
		if d != dl.local {
			out = append(out, d)
		}
	}
	slices.Sort(out)
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

	removable := dl.removableDevices()
	if dl.confirming {
		n := 0
		for _, d := range removable {
			if dl.selected[d] {
				n++
			}
		}
		return m.styles.deletePrompt(fmt.Sprintf("Remove %d device(s) from account?", n), "This republishes the account's device list without them.")
	}

	start, end := openPageBounds(len(removable), dl.page)
	page := removable[start:end]

	rows := []string{fmt.Sprintf("    Device %d (this device)", dl.local)}
	for i, d := range page {
		mark := "[ ]"
		if dl.selected[d] {
			mark = "[x]"
		}
		rows = append(rows, fmt.Sprintf("%d. %s Device %d", i+1, mark, d))
	}

	hint := "digit: toggle · y: remove selected"
	if pages := openPageCount(len(removable)); pages > 1 {
		hint = fmt.Sprintf("page %d/%d · left/right: page · %s", dl.page+1, pages, hint)
	}
	rows = append(rows, "", hint)

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

	removable := dl.removableDevices()
	start, end := openPageBounds(len(removable), dl.page)
	if i, ok := digitKey(msg); ok && i >= 1 && i <= end-start {
		d := removable[start+i-1]
		dl.selected[d] = !dl.selected[d]
		return m, nil, true
	}

	switch msg.String() {
	case "left", "h":
		dl.page = max(0, dl.page-1)
		return m, nil, true
	case "right", "l":
		if dl.page < openPageCount(len(removable))-1 {
			dl.page++
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
