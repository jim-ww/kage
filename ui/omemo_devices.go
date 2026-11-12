package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jim-ww/kage/ui/modal"
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
	// Fingerprint is the device's identity key as hex groups (see the
	// daemon's omemoFingerprint), for comparing against what the device
	// itself shows; "" if its bundle couldn't be fetched.
	Fingerprint string
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

// deviceListModalID finds the open device list, to fill it as the server
// answers.
const deviceListModalID = "omemo-devices"

// deviceListBody is the "view/purge OMEMO devices" list. There's no way to
// know a sibling device's last-used time (or anything else about it) from
// here — kage is just one of potentially several clients on the account,
// and that data lives on the device that used it, not on the server or in
// this instance. So all this shows is each device's ID, protocol and
// fingerprint, and which ones kage is currently running as (above the list,
// never removable). Picking a device (enter, its digit, a click) marks it
// for removal; y removes the marked ones, after asking.
type deviceListBody struct {
	*modal.Menu
	accountIdx int
	local      []OmemoDevice
	devices    []OmemoDevice
	removable  []OmemoDevice
	selected   map[OmemoDevice]bool
	status     string
	err        string
	styles     *uiStyles
	width      int
	height     int
}

// devicePurge is what the device list acts with on y.
type devicePurge struct{}

func (b *deviceListBody) Update(msg tea.Msg) (tea.Cmd, modal.Outcome) {
	if b.Busy() {
		return nil, modal.Continue
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && matchesLetter(k, 'y') {
		if len(b.marked()) == 0 {
			return nil, modal.Continue
		}
		return nil, modal.Act(devicePurge{})
	}
	cmd, out := b.Menu.Update(msg)
	if v, ok := out.Acted(); ok {
		d := b.removable[v.(int)]
		b.selected[d] = !b.selected[d]
		b.relist()
		return cmd, modal.Continue
	}
	return cmd, out
}

// marked is the devices marked for removal.
func (b *deviceListBody) marked() []OmemoDevice {
	var out []OmemoDevice
	for _, d := range b.removable {
		if b.selected[d] {
			out = append(out, d)
		}
	}
	return out
}

// header is this instance's own devices, shown above the list.
func (b *deviceListBody) header() []string {
	var lines []string
	for _, l := range b.local {
		lines = append(lines, fmt.Sprintf("Device %d (%s, this device)", l.ID, l.Protocol))
		lines = append(lines, fingerprintLines("  ", l.Fingerprint)...)
	}
	return lines
}

// footer is the highlighted device's fingerprint - below the list rather
// than under every row, where it'd take two lines per device - and the
// status or error.
func (b *deviceListBody) footer() []string {
	var lines []string
	if c := b.Cursor(); c >= 0 && c < len(b.removable) {
		lines = append(lines, "", fmt.Sprintf("Device %d fingerprint:", b.removable[c].ID))
		lines = append(lines, fingerprintLines("  ", b.removable[c].Fingerprint)...)
	}
	switch {
	case b.status != "":
		lines = append(lines, "", b.styles.messageMuted.Render(b.status))
	case b.err != "":
		lines = append(lines, "", b.styles.popupDanger.Render(b.err))
	}
	return lines
}

func (b *deviceListBody) View() string {
	parts := append(b.header(), b.Menu.View())
	return strings.Join(append(parts, b.footer()...), "\n")
}

func (b *deviceListBody) SetSize(width, height int) {
	b.width, b.height = width, height
	b.Menu.SetSize(width, max(1, height-len(b.header())-len(b.footer())))
}

func (b *deviceListBody) PreferredWidth() int { return 48 }

// Busy keeps the list open while a fetch or purge is with the server.
func (b *deviceListBody) Busy() bool { return b.status != "" }

func (b *deviceListBody) Hints() []modal.Hint {
	if b.Busy() || len(b.removable) == 0 {
		return nil
	}
	hints := []modal.Hint{modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyEnter}, "mark")}
	if len(b.marked()) > 0 {
		hints = append(hints, modal.KeyHint(tea.KeyPressMsg{Code: 'y', Text: "y"}, "remove marked"))
	}
	return hints
}

// setDevices shows the account's devices as the server reported them.
func (b *deviceListBody) setDevices(local, devices []OmemoDevice) {
	b.local, b.devices = local, devices
	b.removable = removableDevices(local, devices)
	b.relist()
}

// relist rebuilds the list's rows from the devices and their marks.
func (b *deviceListBody) relist() {
	items := make([]modal.MenuItem, len(b.removable))
	for i, d := range b.removable {
		mark := "[ ]"
		if b.selected[d] {
			mark = "[x]"
		}
		var key rune
		if i < 9 {
			key = rune('1' + i)
		}
		items[i] = modal.MenuItem{Label: fmt.Sprintf("%s Device %d (%s)", mark, d.ID, d.Protocol), Key: key}
	}
	b.SetItems(items...)
	if b.width > 0 {
		b.SetSize(b.width, b.height)
	}
}

// openDeviceList opens the device list for the current account and kicks
// off a fetch of its published OMEMO device lists.
func (m *Model) openDeviceList() tea.Cmd {
	if m.deviceManager == nil {
		return m.showNotification("omemo device management unavailable")
	}
	accountIdx := m.currentAccount
	if accountIdx < 0 || accountIdx >= len(m.accounts) {
		return nil
	}
	if m.accounts[accountIdx].Connecting {
		return m.showNotification("account is still connecting, try again shortly")
	}
	menu := modal.NewMenu()
	menu.ActOnPick = true
	menu.Empty = "no other devices"
	menu.MatchKey = matchesLetter
	menu.Styles.Cursor = onAccent(m.styles.colors.accentCyan)
	menu.Styles.Hover = lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan)
	body := &deviceListBody{Menu: menu, accountIdx: accountIdx, selected: map[OmemoDevice]bool{}, status: "loading…", styles: m.styles}
	cmd := m.openModal(&modal.Modal[*Model]{
		ID:    deviceListModalID,
		Title: "OMEMO devices",
		Body:  body,
		OnAct: func(m *Model, _ any) tea.Cmd {
			marked := body.marked()
			return m.confirm(fmt.Sprintf("Remove %d device(s) from account?", len(marked)),
				"This republishes the affected protocol's device list without them.",
				func(m *Model) tea.Cmd { return m.purgeDevices(body) })
		},
	})
	manager := m.deviceManager
	return tea.Batch(cmd, func() tea.Msg { return manager.FetchOwnDeviceList(accountIdx) })
}

// purgeDevices republishes body's account's device lists without its
// marked devices.
func (m *Model) purgeDevices(body *deviceListBody) tea.Cmd {
	var keep []OmemoDevice
	for _, d := range body.devices {
		if !body.selected[d] {
			keep = append(keep, d)
		}
	}
	body.status, body.err = "removing…", ""
	accountIdx, manager := body.accountIdx, m.deviceManager
	return func() tea.Msg { return manager.PurgeOwnDeviceList(accountIdx, keep) }
}

// deviceListResult applies a fetch's or purge's answer to the open device
// list for accountIdx, reporting whether it was one and succeeded.
func (m *Model) deviceListResult(accountIdx int, local, devices []OmemoDevice, err error) bool {
	md := m.modals.Find(deviceListModalID)
	if md == nil {
		return false
	}
	body := md.Body.(*deviceListBody)
	if body.accountIdx != accountIdx {
		return false
	}
	body.status = ""
	if err != nil {
		body.err = err.Error()
		return false
	}
	body.err = ""
	body.selected = map[OmemoDevice]bool{}
	body.setDevices(local, devices)
	return true
}

func isLocalDevice(local []OmemoDevice, d OmemoDevice) bool {
	for _, l := range local {
		if l.Protocol == d.Protocol && l.ID == d.ID {
			return true
		}
	}
	return false
}

// removableDevices returns devices minus the local ones, sorted by
// protocol then ID — a stable, predictable numbering for the list's
// selectable rows. The local devices are shown separately, above the list,
// and are never removable.
func removableDevices(local, devices []OmemoDevice) []OmemoDevice {
	out := make([]OmemoDevice, 0, len(devices))
	for _, d := range devices {
		if !isLocalDevice(local, d) {
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

// fingerprintLines splits a fingerprint's eight hex groups over two lines
// of four, each prefixed by indent - one 71-column line wouldn't fit the
// popup on most terminals.
func fingerprintLines(indent, fingerprint string) []string {
	if fingerprint == "" {
		return []string{indent + "fingerprint unavailable"}
	}
	groups := strings.Fields(fingerprint)
	half := (len(groups) + 1) / 2
	return []string{indent + strings.Join(groups[:half], " "), indent + strings.Join(groups[half:], " ")}
}
