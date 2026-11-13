package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// callEndedDisplay is how long an ended call stays shown.
const callEndedDisplay = 3 * time.Second

// callUIState is a call as IncomingCallMsg/CallStateMsg report it. gen keeps
// an ended call's delayed clear from clearing the next call.
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
	desktopVolume       int  // ...at this volume, percent

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

// handleIncomingCallMsg shows an incoming call, ringing until answered or
// rejected.
func (m Model) handleIncomingCallMsg(msg IncomingCallMsg) (Model, tea.Cmd) {
	if m.call != nil && m.call.accountIdx != msg.AccountIdx && m.callInProgress() {
		// One call bar, a call per account: another account's call is only
		// a notice, or actions would hit the wrong call.
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
		// As in handleIncomingCallMsg, until the shown call ends.
		return m, nil
	}
	gen := 0
	startedAt := time.Time{}
	// The same connected call keeps startedAt: "connected" is re-sent with
	// every quality sample.
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
		desktopVolume:       desktopVolumeOrDefault(msg.DesktopAudioVolume),

		sas:                msg.SAS,
		fingerprintChanged: msg.FingerprintChanged,
	}
	if !wasActive {
		m.updateSizes()
	}
	m.refreshContextMenu()
	m.refreshDesktopVolume()
	var notice tea.Cmd
	if msg.Notice != "" {
		notice = m.showNotification(msg.Notice)
	}
	if msg.State == "ended" || msg.State == "failed" {
		return m, tea.Batch(notice, callClearTimer(msg.AccountIdx, gen))
	}
	return m, notice
}

// callBarActive reports whether the call bar takes a row: while a call, ended
// ones included, shows.
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
		// Mic and what we're sending are the call bar's buttons, not text.
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

// The call menu's shortcut letters - one per action, the same in every
// state, and shared with the call bar's buttons, which show them.
const (
	callKeyVoice   = 'v'
	callKeyCamera  = 'c'
	callKeyScreen  = 's'
	callKeyAnswer  = 'y'
	callKeyReject  = 'n'
	callKeyMute    = 'm'
	callKeyDesktop = 'a'
	callKeyVolume  = 'v'
	callKeyVideo   = 'o'
	callKeyDevices = 'd'
	callKeyHangup  = 'h'
)

// callBarButton is one clickable action on the call bar. priority decides
// which buttons survive a narrow terminal: lowest go first.
type callBarButton struct {
	zone     string
	label    string
	priority int
	run      func(m *Model) tea.Cmd
}

// callBarButtons is the call bar's buttons, for drawing and hit-testing
// alike. Each shows a status and the keys doing the same.
func (m Model) callBarButtons() []callBarButton {
	c := m.call
	if c == nil {
		return nil
	}
	menuKey := caretKey(shortestKey(m.keys.CallMenu))
	keyed := func(key rune, text string) string { return menuKey + " " + string(key) + " " + text }
	menu := callBarButton{zone: zoneCallMenu, label: menuKey + " menu", priority: 100, run: (*Model).openCallMenu}
	hangup := callBarButton{zone: zoneCallHangup, label: keyed(callKeyHangup, "hang up"), priority: 90, run: (*Model).hangupCurrentCall}
	switch c.state {
	case "ringing-local":
		return []callBarButton{
			{zone: zoneCallAnswer, label: keyed(callKeyAnswer, "answer"), priority: 95, run: (*Model).answerRingingCall},
			{zone: zoneCallReject, label: keyed(callKeyReject, "reject"), priority: 94, run: (*Model).rejectRingingCall},
			menu,
		}
	case "proposing", "ringing-remote", "negotiating":
		return []callBarButton{hangup, menu}
	case "connected":
	default:
		return nil
	}

	mic := keyed(callKeyMute, "🎤 on")
	if c.muted {
		mic = keyed(callKeyMute, "🔇 muted")
	}
	buttons := []callBarButton{{zone: zoneCallMute, label: mic, priority: 80, run: (*Model).toggleMuteCall}}
	starting := ""
	if c.videoPending {
		starting = " starting…"
	}
	switch c.video {
	case "screen":
		buttons = append(buttons, callBarButton{zone: zoneCallStopScreen, label: keyed(callKeyScreen, "🖥 sharing"+starting+" ✕"), priority: 70,
			run: func(m *Model) tea.Cmd { return m.stopSendingVideo(false) }})
		audio := keyed(callKeyVolume, "🔇 no desktop audio")
		if c.desktopAudioEnabled {
			audio = keyed(callKeyVolume, fmt.Sprintf("🔊 %d%%", c.desktopVolume))
		}
		buttons = append(buttons, callBarButton{zone: zoneCallDesktopAudio, label: audio, priority: 60, run: (*Model).openDesktopVolume})
	case "camera":
		buttons = append(buttons, callBarButton{zone: zoneCallStopCamera, label: keyed(callKeyCamera, "📷 on"+starting+" ✕"), priority: 70,
			run: func(m *Model) tea.Cmd { return m.stopSendingVideo(true) }})
	}
	return append(buttons, hangup, menu)
}

// fitCallBarButtons drops the lowest-priority buttons until what's left,
// rendered by width, fits in room - the menu, which reaches everything the
// others do, goes last.
func fitCallBarButtons(buttons []callBarButton, room int, width func([]callBarButton) int) []callBarButton {
	for len(buttons) > 0 && width(buttons) > room {
		low := 0
		for i, b := range buttons {
			if b.priority < buttons[low].priority {
				low = i
			}
		}
		buttons = append(buttons[:low:low], buttons[low+1:]...)
	}
	return buttons
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

// openCallMenu opens the call menu, which has every call action and follows
// the call's state while open (see contextMenuBody.refresh).
func (m *Model) openCallMenu() tea.Cmd {
	title, items := m.callMenuItems()
	if len(items) == 0 {
		return m.showNotification("no chat selected")
	}
	m.openContextMenu(title, items).refresh = (*Model).callMenuItems
	return nil
}

// callMenuItems is the call menu for the call's state, or for calling the
// selected chat. An action keeps its letter in every state.
func (m *Model) callMenuItems() (string, []contextMenuItem) {
	ctrl := m.daemon
	if ctrl == nil {
		return "", nil
	}
	var devices []contextMenuItem
	devices = []contextMenuItem{{label: "Audio devices", key: callKeyDevices, run: (*Model).requestAudioDevices}}
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
			{label: "Voice call", key: callKeyVoice, run: func(*Model) tea.Cmd {
				return func() tea.Msg { return ctrl.CallAction(accountIdx, CallStart{To: to}) }
			}},
			{label: "Video call", key: callKeyCamera, run: func(*Model) tea.Cmd {
				return func() tea.Msg { return ctrl.CallAction(accountIdx, CallStart{To: to, Video: true, UseCamera: true}) }
			}},
			{label: "Call and share screen", key: callKeyScreen, run: func(*Model) tea.Cmd {
				return func() tea.Msg { return ctrl.CallAction(accountIdx, CallStart{To: to, Video: true}) }
			}},
		}, devices...)
	}

	who := m.callPeerName()
	hangup := contextMenuItem{label: "Hang up", key: callKeyHangup, run: (*Model).hangupCurrentCall}
	switch c.state {
	case "ringing-local":
		return who + " is calling", append([]contextMenuItem{
			{label: "Answer", key: callKeyAnswer, run: (*Model).answerRingingCall},
			{label: "Reject", key: callKeyReject, run: (*Model).rejectRingingCall},
		}, devices...)
	case "connected":
	default:
		return "Calling " + who, append([]contextMenuItem{hangup}, devices...)
	}

	mute := "Mute"
	if c.muted {
		mute = "Unmute"
	}
	items := []contextMenuItem{{label: mute, key: callKeyMute, run: (*Model).toggleMuteCall}}
	items = append(items,
		videoMenuItem(c.video, "camera", "Start camera", "Stop camera", "Switch to camera", callKeyCamera),
		videoMenuItem(c.video, "screen", "Share screen", "Stop sharing screen", "Switch to screen", callKeyScreen),
	)
	if c.video == "screen" {
		label := "Share desktop audio"
		if c.desktopAudioEnabled {
			label = "Stop desktop audio"
		}
		items = append(items, contextMenuItem{label: label, key: callKeyDesktop, run: (*Model).toggleDesktopAudio}, contextMenuItem{label: fmt.Sprintf("Desktop audio volume (%d%%)", c.desktopVolume), key: callKeyVolume, run: (*Model).openDesktopVolume})
	}
	if c.remoteVideo && !c.viewerOpen {
		items = append(items, contextMenuItem{label: "Show their video", key: callKeyVideo, run: func(m *Model) tea.Cmd {
			return m.callCmd(CallReopenVideo{})
		}})
	}
	items = append(items, devices...)
	return "Call with " + who, append(items, hangup)
}

// requestAudioDevices asks for the device listing the "Audio devices" menu
// is built from - enumerating devices is a round trip to the daemon's audio
// backend, so the menu opens once handleAudioDevicesMsg has the answer.
func (m *Model) requestAudioDevices() tea.Cmd {
	m.audioDevicesPending = true
	ctrl := m.daemon
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

// openAudioDevicePicker lists the system default and every device, the
// current one checked, listed even when unplugged.
func (m *Model) openAudioDevicePicker(title string, output bool, devices []AudioDevice, current string) {
	ctrl := m.daemon
	pick := func(label, name string) contextMenuItem {
		label = audioDeviceLabel(label)
		if name == current {
			label = "✓ " + label
		}
		return contextMenuItem{label: label, run: func(*Model) tea.Cmd {
			return saveStateCmd(ctrl, AudioDeviceChoice{Output: output, Name: name}, "audio device")
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

// videoMenuItem starts, stops or switches to a video source.
func videoMenuItem(current, source, start, stop, switchTo string, key rune) contextMenuItem {
	useCamera := source == "camera"
	label, sharing := start, true
	switch current {
	case source:
		label, sharing = stop, false
	case "":
	default:
		label = switchTo
	}
	return contextMenuItem{label: label, key: key, run: func(m *Model) tea.Cmd {
		return m.callCmd(CallShareVideo{Sharing: sharing, UseCamera: useCamera})
	}}
}

// The call menu's and call bar's actions, no-ops without a call.

func (m *Model) answerRingingCall() tea.Cmd { return m.callCmd(CallAnswer{}) }
func (m *Model) rejectRingingCall() tea.Cmd { return m.callCmd(CallReject{}) }
func (m *Model) hangupCurrentCall() tea.Cmd { return m.callCmd(CallHangup{}) }

// stopSendingVideo stops sending our camera (or screen) on the current call.
func (m *Model) stopSendingVideo(camera bool) tea.Cmd {
	return m.callCmd(CallShareVideo{UseCamera: camera})
}

// toggleDesktopAudio flips whether sharing the screen also sends desktop
// audio.
func (m *Model) toggleDesktopAudio() tea.Cmd {
	if m.call == nil {
		return nil
	}
	return saveStateCmd(m.daemon, DesktopAudio(!m.call.desktopAudioEnabled), "desktop audio")
}

// toggleMuteCall flips the mic mute on the current (connected) call.
func (m *Model) toggleMuteCall() tea.Cmd {
	if m.call == nil {
		return nil
	}
	return m.callCmd(CallMute{Muted: !m.call.muted})
}

// callCmd does action to the current call, if any.
func (m *Model) callCmd(action CallAction) tea.Cmd {
	if m.call == nil {
		return nil
	}
	ctrl, accountIdx := m.daemon, m.call.accountIdx
	return func() tea.Msg { return ctrl.CallAction(accountIdx, action) }
}
