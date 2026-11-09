package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// callEndedDisplay is how long a terminal call state ("ended"/"failed")
// stays shown before the banner clears itself — long enough to read, short
// enough not to linger once the call is actually over. Mirrors
// DefaultNoticeDuration's role for the notice toast.
const callEndedDisplay = 3 * time.Second

// callUIState is the local, UI-only view of one account's voice call —
// populated from IncomingCallMsg/CallStateMsg broadcasts, which originate
// from the daemon's own call state machine (callsession.go) regardless of
// which side placed the call. gen guards the delayed self-clear after a
// terminal state, the same pattern noticeClearMsg uses for the toast: a
// later CallStateMsg (a new call starting right after the last one ended)
// must invalidate any pending clear from the previous call.
type callUIState struct {
	accountIdx int
	peer       string // bare JID
	sid        string
	state      string // "proposing", "ringing-remote", "ringing-local", "negotiating", "connected", "ended", "failed"
	reason     string
	gen        int

	muted     bool
	quality   string    // "", "good", "fair", "poor"
	startedAt time.Time // set the first time state becomes "connected"

	video        string // "camera"/"screen" while sending (or about to), else ""
	videoPending bool   // video requested, not flowing yet
	remoteVideo  bool   // the peer is streaming video to us
	viewerOpen   bool   // ...and its window is up

	desktopAudio        bool // other apps' audio is being sent
	desktopAudioEnabled bool // ...whenever the screen is shared

	sas                string // short authentication string, "" until negotiated
	fingerprintChanged bool   // peer's DTLS fingerprint doesn't match the pinned one
}

// callClearMsg fires callEndedDisplay after a call reaches a terminal state.
// It only takes effect if gen still matches m.call.gen — otherwise a newer
// call already replaced the one this timer was scheduled for.
type callClearMsg struct {
	accountIdx int
	gen        int
}

func callClearTimer(accountIdx, gen int) tea.Cmd {
	return tea.Tick(callEndedDisplay, func(time.Time) tea.Msg {
		return callClearMsg{accountIdx: accountIdx, gen: gen}
	})
}

// callInProgress reports whether accountIdx currently has a call that isn't
// idle/terminal — decides whether the call menu offers to place a call or
// acts on the one in progress.
func (m Model) callInProgress() bool {
	return m.call != nil && m.call.state != "ended" && m.call.state != "failed"
}

// handleIncomingCallMsg reacts to a peer proposing a call (XEP-0353) —
// rendered as a ringing-local banner until the user answers or rejects it.
// A second incoming proposal while one is already showing replaces it (the
// daemon only tracks one call per account, same simplification).
func (m Model) handleIncomingCallMsg(msg IncomingCallMsg) (Model, tea.Cmd) {
	if m.call != nil && m.call.accountIdx != msg.AccountIdx && m.callInProgress() {
		// This UI has exactly one call bar for the whole app, but the daemon
		// tracks one call per account - a second account can start ringing
		// while the first account's call bar is already up (e.g. two
		// configured accounts calling each other). Silently replacing the
		// visible call would point every key/mouse action at the wrong
		// account's call from then on (see rejectCall's dead branch for what
		// that looks like on the wire). Surface it as a toast instead.
		return m, m.showNotification(fmt.Sprintf("incoming call from %s (another call is in progress)", msg.From))
	}
	gen := 0
	if m.call != nil {
		gen = m.call.gen + 1
	}
	wasActive := m.callBarActive()
	m.call = &callUIState{
		accountIdx: msg.AccountIdx,
		peer:       msg.From,
		sid:        msg.SID,
		state:      "ringing-local",
		gen:        gen,
	}
	if !wasActive {
		m.updateSizes()
	}
	m.refreshContextMenu()
	return m, nil
}

// handleCallStateMsg reacts to every transition of an account's current
// call. On a terminal state ("ended"/"failed") the banner stays up briefly
// (callEndedDisplay) showing Reason, then self-clears via callClearMsg.
func (m Model) handleCallStateMsg(msg CallStateMsg) (Model, tea.Cmd) {
	if m.call != nil && m.call.accountIdx != msg.AccountIdx && m.callInProgress() {
		// Same guard as handleIncomingCallMsg: don't let a different
		// account's call transitions clobber the one already showing. Once
		// the visible call reaches a terminal state, callInProgress() goes
		// false and the next message for any account is free to take over.
		return m, nil
	}
	gen := 0
	startedAt := time.Time{}
	// A CallStateMsg for the same call (same gen) that's already connected
	// keeps its startedAt — every quality-sampler tick rebroadcasts
	// "connected" (see callSession.sampleQuality), and each one replacing
	// the whole callUIState would otherwise reset the duration to zero every
	// few seconds.
	if m.call != nil {
		gen = m.call.gen + 1
		if m.call.sid == msg.SID && !m.call.startedAt.IsZero() {
			startedAt = m.call.startedAt
			gen = m.call.gen // same logical call state stream, not a new call
		}
	}
	if !msg.StartedAt.IsZero() {
		startedAt = msg.StartedAt
	} else if msg.State == "connected" && startedAt.IsZero() {
		startedAt = time.Now()
	}
	wasActive := m.callBarActive()
	m.call = &callUIState{
		accountIdx: msg.AccountIdx,
		peer:       msg.Peer,
		sid:        msg.SID,
		state:      msg.State,
		reason:     msg.Reason,
		gen:        gen,
		muted:      msg.Muted,
		quality:    msg.Quality,
		startedAt:  startedAt,

		video:        msg.Video,
		videoPending: msg.VideoPending,
		remoteVideo:  msg.RemoteVideo,
		viewerOpen:   msg.ViewerOpen,

		desktopAudio:        msg.DesktopAudio,
		desktopAudioEnabled: msg.DesktopAudioEnabled,

		sas:                msg.SAS,
		fingerprintChanged: msg.FingerprintChanged,
	}
	if !wasActive {
		m.updateSizes()
	}
	m.refreshContextMenu()
	var notice tea.Cmd
	if msg.Notice != "" {
		notice = m.showNotification(msg.Notice)
	}
	if msg.State == "ended" || msg.State == "failed" {
		return m, tea.Batch(notice, callClearTimer(msg.AccountIdx, gen))
	}
	return m, notice
}

// callBarActive reports whether the persistent call bar should occupy a row
// right now — any non-idle call, including the terminal ended/failed state
// while it's still shown (see callClearMsg), so the bar doesn't disappear
// and reappear (and the layout doesn't jump) right as a call ends.
func (m Model) callBarActive() bool {
	return m.call != nil
}

// callPeerName is how the call's peer is shown: the chat's name if the
// call's account has a chat with them, else their bare JID.
func (m Model) callPeerName() string {
	if m.call == nil {
		return ""
	}
	if m.call.accountIdx == m.currentAccount {
		if idx := m.chatIndexByAddress(m.call.accountIdx, m.call.peer); idx >= 0 {
			if chat, ok := m.chats.Items()[idx].(Chat); ok && chat.Name != "" {
				return chat.Name
			}
		}
	}
	return m.call.peer
}

// callStatus is the call bar's status text: who, how long, and everything
// worth knowing about the call right now, most important first so a narrow
// terminal truncates the least important. "" when there's no call.
func (m Model) callStatus() string {
	c := m.call
	if c == nil {
		return ""
	}
	who := m.callPeerName()
	var parts []string
	switch c.state {
	case "ringing-local":
		parts = append(parts, "📞 "+who+" is calling")
	case "proposing", "ringing-remote":
		parts = append(parts, "📞 calling "+who+"…")
	case "negotiating":
		parts = append(parts, "📞 connecting to "+who+"…")
	case "connected":
		dur := "00:00"
		if !c.startedAt.IsZero() {
			dur = formatCallDuration(time.Since(c.startedAt))
		}
		parts = append(parts, "📞 "+who, dur)
	case "ended", "failed":
		text := "📞 call " + c.state
		if c.reason != "" {
			text += ": " + c.reason
		}
		return text
	default:
		return ""
	}
	if c.fingerprintChanged {
		// Loud and early, so truncation never hides it - this is the
		// automatic half of the MITM mitigation (TOFU), worth noticing even
		// by someone who never checks the SAS.
		parts = append(parts, "⚠ peer's call key changed since last time!")
	}
	if c.state == "connected" {
		if c.muted {
			parts = append(parts, "muted")
		}
		switch {
		case c.video == "camera" && c.videoPending:
			parts = append(parts, "camera starting…")
		case c.video == "camera":
			parts = append(parts, "camera on")
		case c.video == "screen" && c.videoPending:
			parts = append(parts, "screen share starting…")
		case c.video == "screen" && c.desktopAudio:
			parts = append(parts, "sharing screen + audio")
		case c.video == "screen":
			parts = append(parts, "sharing screen")
		}
		if c.remoteVideo && !c.viewerOpen {
			parts = append(parts, "their video hidden")
		} else if c.remoteVideo {
			parts = append(parts, "their video")
		}
		switch c.quality {
		case "fair":
			parts = append(parts, "weak connection")
		case "poor":
			parts = append(parts, "poor connection")
		}
	}
	if c.sas != "" {
		parts = append(parts, "🔑 "+c.sas)
	}
	return strings.Join(parts, " · ")
}

// callBarButton is one clickable action on the call bar.
type callBarButton struct {
	zone  string
	label string
	run   func(m *Model) tea.Cmd
}

// callBarButtons lists the call bar's buttons for the current call state,
// in display order - the one list renderCallBar draws and the mouse
// handlers hit-test, so a button that isn't shown can't be clicked through
// a stale zone. The keyboard reaches all of these (and more) through the
// call menu, which the last button opens.
func (m Model) callBarButtons() []callBarButton {
	if m.call == nil {
		return nil
	}
	menu := callBarButton{zone: zoneCallMenu, label: caretKey(shortestKey(m.keys.CallMenu)) + " menu", run: (*Model).openCallMenu}
	hangup := callBarButton{zone: zoneCallHangup, label: "hang up", run: (*Model).hangupCurrentCall}
	switch m.call.state {
	case "ringing-local":
		return []callBarButton{
			{zone: zoneCallAnswer, label: "answer", run: (*Model).answerRingingCall},
			{zone: zoneCallReject, label: "reject", run: (*Model).rejectRingingCall},
			menu,
		}
	case "proposing", "ringing-remote", "negotiating":
		return []callBarButton{hangup, menu}
	case "connected":
		mute := "mute"
		if m.call.muted {
			mute = "unmute"
		}
		return []callBarButton{{zone: zoneCallMute, label: mute, run: (*Model).toggleMuteCall}, hangup, menu}
	default:
		return nil
	}
}

// formatCallDuration renders d as mm:ss — calls in this slice are never
// long enough to need an hours field.
func formatCallDuration(d time.Duration) string {
	total := int(d.Seconds())
	if total < 0 {
		total = 0
	}
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

// openCallMenu opens the call menu — every call action lives there, reached
// by CallMenu from any view and by the call bar's menu button. While it's
// open it follows the call (see contextMenu.refresh): a call ringing in,
// connecting or ending swaps what it offers.
func (m *Model) openCallMenu() tea.Cmd {
	if m.callController == nil {
		return m.showNotification("calling unavailable")
	}
	title, items := m.callMenuItems()
	if len(items) == 0 {
		return m.showNotification("no chat selected")
	}
	m.openContextMenu(title, items)
	m.contextMenu.refresh = (*Model).callMenuItems
	return nil
}

// callMenuItems builds the call menu for the current call's state, or for
// placing a call to the selected chat when there's none. Each item's
// shortcut letter is fixed per action across states (c is always the
// camera, s the screen, h hang up), and y/n answer/reject like every other
// yes/no prompt in the app.
func (m *Model) callMenuItems() (string, []contextMenuItem) {
	ctrl := m.callController
	if ctrl == nil {
		return "", nil
	}
	var devices []contextMenuItem
	if m.audioDevices != nil {
		devices = []contextMenuItem{{label: "Audio devices", key: 'd', run: (*Model).requestAudioDevices}}
	}
	c := m.call
	if c == nil || !m.callInProgress() {
		chat, ok := m.currentChat()
		if !ok || chat.Address == "" || (m.selectedView != viewChat && m.selectedView != viewChats) {
			return "Calls", devices
		}
		accountIdx, to := m.currentAccount, chat.Address
		name := chat.Name
		if name == "" {
			name = to
		}
		return "Call " + name, append([]contextMenuItem{
			{label: "Voice call", key: 'v', run: func(*Model) tea.Cmd {
				return func() tea.Msg { return ctrl.StartCall(accountIdx, to) }
			}},
			{label: "Video call", key: 'c', run: func(*Model) tea.Cmd {
				return func() tea.Msg { return ctrl.StartVideoCall(accountIdx, to, true) }
			}},
			{label: "Call and share screen", key: 's', run: func(*Model) tea.Cmd {
				return func() tea.Msg { return ctrl.StartVideoCall(accountIdx, to, false) }
			}},
		}, devices...)
	}

	accountIdx, who := c.accountIdx, m.callPeerName()
	hangup := contextMenuItem{label: "Hang up", key: 'h', run: func(*Model) tea.Cmd {
		return func() tea.Msg { return ctrl.HangupCall(accountIdx) }
	}}
	switch c.state {
	case "ringing-local":
		return who + " is calling", append([]contextMenuItem{
			{label: "Answer", key: 'y', run: (*Model).answerRingingCall},
			{label: "Reject", key: 'n', run: (*Model).rejectRingingCall},
		}, devices...)
	case "connected":
	default:
		return "Calling " + who, append([]contextMenuItem{hangup}, devices...)
	}

	mute := "Mute"
	if c.muted {
		mute = "Unmute"
	}
	items := []contextMenuItem{{label: mute, key: 'm', run: (*Model).toggleMuteCall}}
	items = append(items,
		videoMenuItem(ctrl, accountIdx, c.video, "camera", "Start camera", "Stop camera", "Switch to camera", 'c'),
		videoMenuItem(ctrl, accountIdx, c.video, "screen", "Share screen", "Stop sharing screen", "Switch to screen", 's'),
	)
	if c.video == "screen" {
		label, enable := "Share desktop audio", true
		if c.desktopAudioEnabled {
			label, enable = "Stop desktop audio", false
		}
		items = append(items, contextMenuItem{label: label, key: 'a', run: func(*Model) tea.Cmd {
			return func() tea.Msg { return ctrl.SetDesktopAudio(enable) }
		}})
	}
	if c.remoteVideo && !c.viewerOpen {
		items = append(items, contextMenuItem{label: "Show their video", key: 'o', run: func(*Model) tea.Cmd {
			return func() tea.Msg { return ctrl.ReopenVideo(accountIdx) }
		}})
	}
	items = append(items, devices...)
	return "Call with " + who, append(items, hangup)
}

// requestAudioDevices asks for the device listing the "Audio devices" menu
// is built from - enumerating devices is a round trip to the daemon's audio
// backend, so the menu opens once handleAudioDevicesMsg has the answer.
func (m *Model) requestAudioDevices() tea.Cmd {
	if m.audioDevices == nil {
		return nil
	}
	m.audioDevicesPending = true
	ctrl := m.audioDevices
	return ctrl.ListAudioDevices
}

// handleAudioDevicesMsg opens the "Audio devices" menu from a listing
// requestAudioDevices asked for: the microphone and speaker in use, each
// opening a picker of every device that kind.
func (m *Model) handleAudioDevicesMsg(msg AudioDevicesMsg) tea.Cmd {
	if !m.audioDevicesPending {
		return nil
	}
	m.audioDevicesPending = false
	if msg.Err != nil {
		return m.showNotification("audio devices: " + msg.Err.Error())
	}
	m.openContextMenu("Audio devices", []contextMenuItem{
		{label: "Microphone: " + audioDeviceLabel(msg.Input), key: 'm', run: func(m *Model) tea.Cmd {
			m.openAudioDevicePicker("Microphone", false, msg.Inputs, msg.Input)
			return nil
		}},
		{label: "Speaker: " + audioDeviceLabel(msg.Output), key: 's', run: func(m *Model) tea.Cmd {
			m.openAudioDevicePicker("Speaker", true, msg.Outputs, msg.Output)
			return nil
		}},
	})
	return nil
}

// openAudioDevicePicker lists "System default" plus every device, the
// current choice checked - mirrors the account status picker. A chosen
// device that isn't connected right now is still listed (calls fall back to
// the default while it's gone) so it's clear why the default is playing.
func (m *Model) openAudioDevicePicker(title string, output bool, devices []AudioDevice, current string) {
	ctrl := m.audioDevices
	pick := func(label, name string) contextMenuItem {
		label = audioDeviceLabel(label)
		if name == current {
			label = "✓ " + label
		}
		return contextMenuItem{label: label, run: func(*Model) tea.Cmd {
			return func() tea.Msg { return ctrl.SetAudioDevice(output, name) }
		}}
	}
	items := []contextMenuItem{pick("System default", "")}
	found := current == ""
	for _, d := range devices {
		items = append(items, pick(d.Name, d.Name))
		found = found || d.Name == current
	}
	if !found {
		items = append(items, pick(current+" (not connected)", current))
	}
	m.openContextMenu(title, items)
}

// audioDeviceLabel is how a device name is shown: as is, but with its
// whitespace collapsed - ALSA's names span several lines and carry tabs,
// which would break the menu's layout.
func audioDeviceLabel(name string) string {
	if name == "" {
		return "System default"
	}
	return strings.Join(strings.Fields(name), " ")
}

// videoMenuItem is the call menu's toggle for one video source: start it,
// stop it if it's what we're already sending, or switch to it from the
// other one (the daemon swaps the capture in place - see
// callSession.switchVideoSource).
func videoMenuItem(ctrl CallController, accountIdx int, current, source, start, stop, switchTo string, key rune) contextMenuItem {
	useCamera := source == "camera"
	label, sharing := start, true
	switch current {
	case source:
		label, sharing = stop, false
	case "":
	default:
		label = switchTo
	}
	return contextMenuItem{label: label, key: key, run: func(*Model) tea.Cmd {
		return func() tea.Msg { return ctrl.ScreenShare(accountIdx, sharing, useCamera) }
	}}
}

// answerRingingCall/rejectRingingCall/hangupCurrentCall/toggleMuteCall are
// shared by the call menu and the call bar's buttons (mouse.go). Each is a
// no-op (returning nil) if there's no call controller wired or no call to
// act on.

func (m *Model) answerRingingCall() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	ctrl, accountIdx := m.callController, m.call.accountIdx
	return func() tea.Msg { return ctrl.AnswerCall(accountIdx) }
}

func (m *Model) rejectRingingCall() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	ctrl, accountIdx := m.callController, m.call.accountIdx
	return func() tea.Msg { return ctrl.RejectCall(accountIdx) }
}

func (m *Model) hangupCurrentCall() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	ctrl, accountIdx := m.callController, m.call.accountIdx
	return func() tea.Msg { return ctrl.HangupCall(accountIdx) }
}

// toggleMuteCall flips the mic mute on the current (connected) call.
func (m *Model) toggleMuteCall() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	ctrl, accountIdx, muted := m.callController, m.call.accountIdx, !m.call.muted
	return func() tea.Msg { return ctrl.MuteCall(accountIdx, muted) }
}
