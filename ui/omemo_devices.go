package ui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// OmemoDeviceManager fetches and republishes our own account's OMEMO device
// lists (XEP-0384 PEP for v2, eu.siacs.conversations.axolotl for v1),
// implemented outside ui (main.go's adapter) so ui stays decoupled from
// xmpp/omemo. Both run as a tea.Cmd like AccountAdder.AddAccount since
// they're network I/O.
type OmemoDeviceManager interface {
	FetchOwnDeviceList(accountIdx int) tea.Msg
	PurgeOwnDeviceList(accountIdx int, keep []OmemoDevice) tea.Msg
}

// OmemoDevice identifies one device in either OMEMO protocol's device list.
// Device IDs are only unique within a protocol - the same numeric ID can
// coincidentally appear in both an account's v1 and v2 device pools, since
// each protocol maintains its own independent identity/device pool (see
// account.go) - so Protocol is part of the identity, not just a label.
type OmemoDevice struct {
	Protocol string // "v1" | "v2"
	ID       uint32
}

// OmemoDeviceListMsg reports the result of OmemoDeviceManager.FetchOwnDeviceList.
type OmemoDeviceListMsg struct {
	AccountIdx int
	Local      []OmemoDevice // this instance's own device per protocol that's ready
	Devices    []OmemoDevice // every device across both protocols (includes Local)
	Err        error
}

// OmemoDevicePurgedMsg reports the result of OmemoDeviceManager.PurgeOwnDeviceList.
type OmemoDevicePurgedMsg struct {
	AccountIdx int
	Local      []OmemoDevice
	Devices    []OmemoDevice
	Err        error
}

// deviceListState holds the state of the "view/purge OMEMO devices" popup,
// active while non-nil. There's no way to know a sibling device's last-used
// time (or anything else about it) from here — kage is just one of
// potentially several clients on the account, and that data lives on the
// device that used it, not on the server or in this instance. So all this
// shows is the device ID and protocol, and whether it's the one kage is
// currently running as.
type deviceListState struct {
	accountIdx int
	local      []OmemoDevice // this instance's own devices; never selectable for removal
	devices    []OmemoDevice
	selected   map[OmemoDevice]bool // devices marked for removal
	page       int                  // current page (of openItemsPerPage) through the removable devices
	busy       bool
	err        string
	confirming bool // true while showing the y/n "remove N device(s)?" confirmation
}

// openDeviceList opens the device-list popup for the current account and
// kicks off a fetch of its published OMEMO device lists.
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
	m.deviceList = &deviceListState{accountIdx: accountIdx, busy: true, selected: map[OmemoDevice]bool{}}
	return m, func() tea.Msg { return m.deviceManager.FetchOwnDeviceList(accountIdx) }
}

func (dl *deviceListState) isLocal(d OmemoDevice) bool {
	for _, l := range dl.local {
		if l == d {
			return true
		}
	}
	return false
}

// removableDevices returns dl.devices minus the local devices, sorted by
// protocol then ID — a stable, predictable numbering for the popup's
// paginated, selectable rows. The local devices are always shown separately,
// pinned above the page and never counted against it (they're never
// removable).
func (dl *deviceListState) removableDevices() []OmemoDevice {
	out := make([]OmemoDevice, 0, len(dl.devices))
	for _, d := range dl.devices {
		if !dl.isLocal(d) {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b OmemoDevice) int {
		if a.Protocol != b.Protocol {
			if a.Protocol < b.Protocol {
				return -1
			}
			return 1
		}
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
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

	removable := dl.removableDevices()
	if dl.confirming {
		n := 0
		for _, d := range removable {
			if dl.selected[d] {
				n++
			}
		}
		return m.styles.deletePrompt(m.deletePromptWidth(), fmt.Sprintf("Remove %d device(s) from account?", n), "This republishes the affected protocol's device list without them.")
	}

	start, end := openPageBounds(len(removable), dl.page)
	page := removable[start:end]

	rows := make([]string, 0, len(dl.local)+len(page)+2)
	for _, l := range dl.local {
		rows = append(rows, fmt.Sprintf("    Device %d (%s, this device)", l.ID, l.Protocol))
	}
	for i, d := range page {
		mark := "[ ]"
		if dl.selected[d] {
			mark = "[x]"
		}
		rows = append(rows, fmt.Sprintf("%d. %s Device %d (%s)", i+1, mark, d.ID, d.Protocol))
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
		if matchesKey(msg, m.keys.Back) || matchesKey(msg, m.keys.ConfirmNo) || matchesKey(msg, m.keys.DeviceList) {
			m.deviceList = nil
		}
		return m, nil, true
	}

	if dl.confirming {
		switch {
		case matchesKey(msg, m.keys.ConfirmYes):
			var keep []OmemoDevice
			for _, d := range dl.devices {
				if !dl.selected[d] {
					keep = append(keep, d)
				}
			}
			accountIdx := dl.accountIdx
			dl.busy = true
			dl.confirming = false
			return m, func() tea.Msg { return m.deviceManager.PurgeOwnDeviceList(accountIdx, keep) }, true
		case matchesKey(msg, m.keys.ConfirmNo):
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

	switch {
	case msg.String() == "left" || matchesLetter(msg, 'h'):
		dl.page = max(0, dl.page-1)
		return m, nil, true
	case msg.String() == "right" || matchesLetter(msg, 'l'):
		if dl.page < openPageCount(len(removable))-1 {
			dl.page++
		}
		return m, nil, true
	}

	switch {
	case matchesKey(msg, m.keys.ConfirmYes):
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
	case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.ConfirmNo), matchesKey(msg, m.keys.DeviceList):
		m.deviceList = nil
	}
	return m, nil, true
}
