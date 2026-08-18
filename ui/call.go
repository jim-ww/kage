package ui

import (
	"fmt"
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
	sharing   bool      // true while we're actively sending our own screen
	startedAt time.Time // set the first time state becomes "connected"

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
// idle/terminal — used to disable starting a second call and to decide
// whether CallToggle should hang up instead of dial.
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
	wasSharing := m.call != nil && m.call.sharing
	sameStream := false
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
			sameStream = true
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
		sharing:    msg.Sharing,
		startedAt:  startedAt,

		sas:                msg.SAS,
		fingerprintChanged: msg.FingerprintChanged,
	}
	if !wasActive {
		m.updateSizes()
	}
	// Sharing having just started, or the call ending, makes a stale "camera
	// or screen?" prompt meaningless - clear it then. But a redundant
	// rebroadcast of the same still-connected state (sameStream, e.g. every
	// qualitySampleInterval tick from callSession.sampleQuality) must NOT
	// clear it: doing so unconditionally here used to close the prompt
	// within a few seconds of opening it, often before the user had time to
	// press c/s - see qualitySampleInterval in callsession.go.
	if !sameStream || (msg.Sharing && !wasSharing) || msg.State == "ended" || msg.State == "failed" {
		m.videoSourcePrompt = false
		m.videoDialPrompt = false
	}
	if msg.State == "ended" || msg.State == "failed" {
		return m, callClearTimer(msg.AccountIdx, gen)
	}
	return m, nil
}

// callBarActive reports whether the persistent call bar should occupy a row
// right now — any non-idle call, including the terminal ended/failed state
// while it's still shown (see callClearMsg), so the bar doesn't disappear
// and reappear (and the layout doesn't jump) right as a call ends.
func (m Model) callBarActive() bool {
	return m.call != nil || m.videoDialPrompt
}

// callBarLine renders the current call state as a single line for the
// persistent call bar, or "" when there's nothing to show. Unlike the old
// toast-based callBannerText, this is meant to occupy a real, fixed layout
// row (see renderCallBar in view.go) rather than a self-clearing overlay —
// the "[key] label" hint segments follow the same plain style used
// elsewhere for static hints (e.g. deletePrompt's "[y] yes    [n] no").
func (m Model) callBarLine() string {
	if m.call == nil {
		return ""
	}
	who := m.call.peer
	switch m.call.state {
	case "ringing-local":
		return "📞 incoming call: " + who + "·[ctrl+y] answer·[ctrl+n] reject"
	case "proposing", "ringing-remote":
		return "📞 calling " + who + "...·[ctrl+h] hang up"
	case "negotiating":
		line := "📞 connecting to " + who + "...·[ctrl+h] hang up"
		if m.call.fingerprintChanged {
			line += "·⚠ peer's call key changed since last time!"
		}
		if m.call.sas != "" {
			line += "·🔑 " + m.call.sas
		}
		return line
	case "connected":
		dur := "00:00"
		if !m.call.startedAt.IsZero() {
			dur = formatCallDuration(time.Since(m.call.startedAt))
		}
		mic := "🎤 unmuted"
		if m.call.muted {
			mic = "🎤 muted"
		}
		quality := "📶 …"
		if m.call.quality != "" {
			quality = "📶 " + m.call.quality
		}
		share := "[ctrl+s] share screen"
		if m.call.sharing {
			share = "🖥 sharing·[ctrl+s] stop"
		}
		line := "📞 " + who + "·" + dur + "·" + mic + "·" + quality + "·[ctrl+m] mute·" + share + "·[ctrl+r] reopen video·[ctrl+h] hang up"
		if m.call.fingerprintChanged {
			// Loud and separate from the SAS itself - this is the automatic
			// half of the MITM mitigation (TOFU), worth noticing even by
			// someone who never checks the SAS.
			line += "·⚠ peer's call key changed since last time!"
		}
		if m.call.sas != "" {
			line += "·🔑 " + m.call.sas
		}
		return line
	case "ended":
		if m.call.reason != "" {
			return "call ended: " + m.call.reason
		}
		return "call ended"
	case "failed":
		if m.call.reason != "" {
			return "call failed: " + m.call.reason
		}
		return "call failed"
	default:
		return ""
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

// startCallToCurrentChat places a call to the currently open chat, or hangs
// up/rejects whatever call is already in progress on this account — the
// single action bound to CallToggle. No-ops (returning nil) when there's
// nothing sensible to do (no call controller wired, no chat open while
// idle).
func (m Model) startCallToCurrentChat() tea.Cmd {
	if m.callController == nil {
		return m.showNotification("calling unavailable")
	}
	accountIdx := m.currentAccount
	if m.call != nil && m.callInProgress() {
		// A call is already up (ours or theirs, answered or not) — Toggle
		// hangs it up. Ringing-local is handled separately via
		// ConfirmYes/ConfirmNo (answer/reject), not this key.
		return func() tea.Msg { return m.callController.HangupCall(accountIdx) }
	}
	chat, ok := m.currentChat()
	if !ok || chat.Address == "" {
		return m.showNotification("no chat selected")
	}
	return func() tea.Msg { return m.callController.StartCall(accountIdx, chat.Address) }
}

// startVideoCallToCurrentChat opens the pre-dial "camera or screen?" prompt
// for VideoCallToggle, or hangs up whatever call is already in progress
// (mirroring startCallToCurrentChat) — the actual StartVideoCall RPC only
// fires once the user picks a source, via startVideoCall below.
func (m Model) startVideoCallToCurrentChat() (Model, tea.Cmd) {
	if m.callController == nil {
		cmd := m.showNotification("calling unavailable")
		return m, cmd
	}
	if m.call != nil && m.callInProgress() {
		accountIdx := m.currentAccount
		return m, func() tea.Msg { return m.callController.HangupCall(accountIdx) }
	}
	if _, ok := m.currentChat(); !ok {
		cmd := m.showNotification("no chat selected")
		return m, cmd
	}
	m.videoDialPrompt = true
	return m, nil
}

// startVideoCall answers the pre-dial "camera or screen?" prompt, closing it
// and placing a video call to the currently open chat with the chosen
// source.
func (m Model) startVideoCall(useCamera bool) (Model, tea.Cmd) {
	m.videoDialPrompt = false
	if m.callController == nil {
		return m, nil
	}
	chat, ok := m.currentChat()
	if !ok || chat.Address == "" {
		cmd := m.showNotification("no chat selected")
		return m, cmd
	}
	accountIdx := m.currentAccount
	return m, func() tea.Msg { return m.callController.StartVideoCall(accountIdx, chat.Address, useCamera) }
}

// answerRingingCall/rejectRingingCall/hangupCurrentCall/toggleMuteCall are
// the call-bar action helpers — shared by both the keybind handlers
// (update_keys.go) and the mouse-click handlers (mouse.go) so the RPC-calling
// logic lives in exactly one place, mirroring startCallToCurrentChat above.
// Each is a no-op (returning nil) if there's no call controller wired or no
// call to act on.

func (m Model) answerRingingCall() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	accountIdx := m.call.accountIdx
	return func() tea.Msg { return m.callController.AnswerCall(accountIdx) }
}

func (m Model) rejectRingingCall() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	accountIdx := m.call.accountIdx
	return func() tea.Msg { return m.callController.RejectCall(accountIdx) }
}

func (m Model) hangupCurrentCall() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	accountIdx := m.call.accountIdx
	return func() tea.Msg { return m.callController.HangupCall(accountIdx) }
}

// toggleMuteCall flips the mic mute on the current (connected) call.
func (m Model) toggleMuteCall() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	accountIdx, muted := m.call.accountIdx, !m.call.muted
	return func() tea.Msg { return m.callController.MuteCall(accountIdx, muted) }
}

// toggleScreenShare flips whether we're sending our own screen on the
// current (connected) call. Starting always means the screen (useCamera
// false) — for a camera prompt, see startVideoPrompt/startVideo.
func (m Model) toggleScreenShare() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	accountIdx, sharing := m.call.accountIdx, !m.call.sharing
	return func() tea.Msg { return m.callController.ScreenShare(accountIdx, sharing, false) }
}

// reopenRemoteVideo re-requests the peer's video keyframe, reopening the
// mpv viewer if it was closed by accident (the [r] hint in callBarLine) -
// a no-op server-side if there's no incoming video to reopen.
func (m Model) reopenRemoteVideo() tea.Cmd {
	if m.callController == nil || m.call == nil {
		return nil
	}
	accountIdx := m.call.accountIdx
	return func() tea.Msg { return m.callController.ReopenVideo(accountIdx) }
}

// startVideoPrompt opens (or, if already sharing, is a no-op for) the
// "camera or screen?" call-bar prompt that VideoToggle triggers - the actual
// RPC only fires once the user picks a source, via startVideo. Returning the
// mutated Model directly (not a tea.Cmd) since this only changes local UI
// state, the same pattern confirmTarget uses for its popups.
func (m Model) startVideoPrompt() Model {
	if m.call == nil || m.call.state != "connected" || m.call.sharing {
		return m
	}
	m.videoSourcePrompt = true
	return m
}

// startVideo answers the "camera or screen?" prompt, closing it and sending
// our own video from the chosen source on the current (connected) call.
func (m Model) startVideo(useCamera bool) (Model, tea.Cmd) {
	m.videoSourcePrompt = false
	if m.callController == nil || m.call == nil {
		return m, nil
	}
	accountIdx := m.call.accountIdx
	return m, func() tea.Msg { return m.callController.ScreenShare(accountIdx, true, useCamera) }
}
