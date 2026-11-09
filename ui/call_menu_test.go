package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// fakeCallController records every CallController call as a string, e.g.
// "share true camera".
type fakeCallController struct {
	calls []string
}

func (f *fakeCallController) record(format string, args ...any) tea.Msg {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
	return nil
}

func (f *fakeCallController) StartCall(_ int, to string) tea.Msg { return f.record("start %s", to) }
func (f *fakeCallController) StartVideoCall(_ int, to string, camera bool) tea.Msg {
	return f.record("startvideo %s camera=%v", to, camera)
}
func (f *fakeCallController) AnswerCall(int) tea.Msg         { return f.record("answer") }
func (f *fakeCallController) HangupCall(int) tea.Msg         { return f.record("hangup") }
func (f *fakeCallController) RejectCall(int) tea.Msg         { return f.record("reject") }
func (f *fakeCallController) MuteCall(_ int, m bool) tea.Msg { return f.record("mute %v", m) }
func (f *fakeCallController) ReopenVideo(int) tea.Msg        { return f.record("reopen") }
func (f *fakeCallController) SetDesktopAudioVolume(percent int) tea.Msg {
	return f.record("volume %d", percent)
}
func (f *fakeCallController) SetDesktopAudio(on bool) tea.Msg {
	return f.record("desktopaudio %v", on)
}
func (f *fakeCallController) ScreenShare(_ int, sharing, camera bool) tea.Msg {
	return f.record("share %v camera=%v", sharing, camera)
}

// callTestModel is a model with one open chat with bob and ctrl wired up.
func callTestModel(ctrl CallController) Model {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.callController = ctrl
	chat := Chat{Name: "Bob", Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{}}}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChat
	return m
}

// press sends key through Update and runs whatever command came back, so
// the fake controller sees the RPC the key triggered.
func press(t *testing.T, m Model, key tea.KeyPressMsg) Model {
	t.Helper()
	next, cmd := m.Update(key)
	nonIdleCmd(cmd)
	return next.(Model)
}

var (
	ctrlG = tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}
	ctrlH = tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}
)

func letter(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func callMenuLabels(m Model) []string {
	if m.contextMenu == nil {
		return nil
	}
	var out []string
	for _, it := range m.contextMenu.items {
		out = append(out, it.label)
	}
	return out
}

func TestCallMenuPlacesCalls(t *testing.T) {
	for _, tc := range []struct {
		key  rune
		want string
	}{
		{'v', "start bob@example.test"},
		{'c', "startvideo bob@example.test camera=true"},
		{'s', "startvideo bob@example.test camera=false"},
	} {
		ctrl := &fakeCallController{}
		m := press(t, callTestModel(ctrl), ctrlG)
		if m.contextMenu == nil || m.contextMenu.title != "Call Bob" {
			t.Fatalf("ctrl+g opened %+v", m.contextMenu)
		}
		m = press(t, m, letter(tc.key))
		if len(ctrl.calls) != 1 || ctrl.calls[0] != tc.want {
			t.Fatalf("%c: calls %v, want [%s]", tc.key, ctrl.calls, tc.want)
		}
		if m.contextMenu != nil {
			t.Fatalf("%c: menu still open", tc.key)
		}
	}
}

// TestCallMenuEnterPlacesVoiceCall checks the quickest path, ctrl+g enter,
// is a plain voice call.
func TestCallMenuEnterPlacesVoiceCall(t *testing.T) {
	ctrl := &fakeCallController{}
	m := press(t, callTestModel(ctrl), ctrlG)
	press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(ctrl.calls) != 1 || ctrl.calls[0] != "start bob@example.test" {
		t.Fatalf("calls %v", ctrl.calls)
	}
}

func TestCallMenuAnswerReject(t *testing.T) {
	for _, tc := range []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{letter('y'), "answer"},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, "answer"},
		{letter('n'), "reject"},
	} {
		ctrl := &fakeCallController{}
		m := callTestModel(ctrl)
		m, _ = m.handleIncomingCallMsg(IncomingCallMsg{From: "bob@example.test", SID: "s1"})
		m = press(t, m, ctrlG)
		if m.contextMenu == nil || m.contextMenu.title != "Bob is calling" {
			t.Fatalf("menu while ringing: %+v", m.contextMenu)
		}
		press(t, m, tc.key)
		if len(ctrl.calls) != 1 || ctrl.calls[0] != tc.want {
			t.Fatalf("%q: calls %v, want [%s]", tc.key.String(), ctrl.calls, tc.want)
		}
	}
}

// TestCallKeysDontShadowChatKeys checks that with a call up, keys the app
// binds elsewhere keep doing their own job and never reach the call: the
// old ctrl+h hang-up shadowed help, ctrl+r reply, ctrl+s save.
func TestCallKeysDontShadowChatKeys(t *testing.T) {
	ctrl := &fakeCallController{}
	m := callTestModel(ctrl)
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected"})

	for _, k := range []tea.KeyPressMsg{
		{Code: 'r', Mod: tea.ModCtrl}, {Code: 's', Mod: tea.ModCtrl}, {Code: 'm', Mod: tea.ModCtrl},
		{Code: 'v', Mod: tea.ModCtrl}, letter('y'), letter('h'), letter('m'),
	} {
		m = press(t, m, k)
	}
	if len(ctrl.calls) != 0 {
		t.Fatalf("chat keys reached the call controller: %v", ctrl.calls)
	}
	m = press(t, m, ctrlH)
	if !m.showHelp {
		t.Fatal("ctrl+h during a call didn't open help")
	}
}

func TestCallMenuConnectedVideoLabels(t *testing.T) {
	cases := []struct {
		video      string
		wantCamera string
		wantScreen string
		key        rune
		wantCall   string
	}{
		{"", "Start camera", "Share screen", 'c', "share true camera=true"},
		{"camera", "Stop camera", "Switch to screen", 'c', "share false camera=true"},
		{"camera", "Stop camera", "Switch to screen", 's', "share true camera=false"},
		{"screen", "Switch to camera", "Stop sharing screen", 's', "share false camera=false"},
	}
	for _, tc := range cases {
		ctrl := &fakeCallController{}
		m := callTestModel(ctrl)
		m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", Video: tc.video, Sharing: tc.video != ""})
		m = press(t, m, ctrlG)
		labels := strings.Join(callMenuLabels(m), "|")
		if !strings.Contains(labels, tc.wantCamera) || !strings.Contains(labels, tc.wantScreen) {
			t.Fatalf("video=%q: menu %s", tc.video, labels)
		}
		press(t, m, letter(tc.key))
		if len(ctrl.calls) != 1 || ctrl.calls[0] != tc.wantCall {
			t.Fatalf("video=%q key %c: calls %v, want [%s]", tc.video, tc.key, ctrl.calls, tc.wantCall)
		}
	}
}

func TestCallMenuShowTheirVideoOnlyWhenHidden(t *testing.T) {
	m := callTestModel(&fakeCallController{})
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", RemoteVideo: true, ViewerOpen: true})
	m = press(t, m, ctrlG)
	if strings.Contains(strings.Join(callMenuLabels(m), "|"), "Show their video") {
		t.Fatal("offered to show their video while its window is open")
	}
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", RemoteVideo: true})
	if !strings.Contains(strings.Join(callMenuLabels(m), "|"), "Show their video") {
		t.Fatalf("open menu didn't pick up the closed viewer: %v", callMenuLabels(m))
	}
}

// TestCallMenuFollowsCallState checks an open menu is rebuilt as the call
// moves on, so its actions never target a state that's gone.
func TestCallMenuFollowsCallState(t *testing.T) {
	m := callTestModel(&fakeCallController{})
	m, _ = m.handleIncomingCallMsg(IncomingCallMsg{From: "bob@example.test", SID: "s1"})
	m = press(t, m, ctrlG)
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected"})
	if got := callMenuLabels(m); len(got) == 0 || got[0] != "Mute" {
		t.Fatalf("after connecting: %v", got)
	}
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "ended"})
	if got := callMenuLabels(m); len(got) == 0 || got[0] != "Voice call" {
		t.Fatalf("after ending: %v", got)
	}
}

// TestCallBarIsOneRow checks the bar never wraps, whatever the state or
// width - it has exactly one row reserved in the layout.
func TestCallBarIsOneRow(t *testing.T) {
	states := []CallStateMsg{
		{Peer: "bob@example.test", SID: "s1", State: "ringing-remote"},
		{Peer: "bob@example.test", SID: "s1", State: "negotiating", SAS: "1234 5678", FingerprintChanged: true},
		{Peer: "bob@example.test", SID: "s1", State: "connected", Muted: true, Video: "screen", RemoteVideo: true, Quality: "poor", SAS: "1234 5678", FingerprintChanged: true},
		{Peer: "bob@example.test", SID: "s1", State: "failed", Reason: strings.Repeat("very long reason ", 10)},
	}
	for _, st := range states {
		for _, w := range []int{8, 20, 40, 60, 80, 200} {
			m := callTestModel(&fakeCallController{})
			m, _ = m.handleCallStateMsg(st)
			bar := m.renderCallBar(w)
			if h := lipgloss.Height(bar); h != 1 {
				t.Fatalf("%s at width %d: %d rows\n%s", st.State, w, h, bar)
			}
			if got := lipgloss.Width(bar); got != w {
				t.Fatalf("%s at width %d: rendered %d wide", st.State, w, got)
			}
		}
	}
}

func TestCallBarButtons(t *testing.T) {
	m := callTestModel(&fakeCallController{})
	m, _ = m.handleIncomingCallMsg(IncomingCallMsg{From: "bob@example.test", SID: "s1"})
	bar := m.renderCallBar(100)
	for _, want := range []string{"Bob is calling", "[^G y answer]", "[^G n reject]", "[^G menu]"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("ringing bar missing %q:\n%s", want, bar)
		}
	}
	// Narrow: only the menu button survives.
	bar = m.renderCallBar(16)
	if strings.Contains(bar, "answer") || !strings.Contains(bar, "[^G menu]") {
		t.Fatalf("narrow ringing bar:\n%s", bar)
	}
	// In between: the lowest-priority button (reject) goes first.
	bar = m.renderCallBar(lipgloss.Width("[^G y answer] [^G menu]") + 4)
	if !strings.Contains(bar, "[^G y answer]") || strings.Contains(bar, "reject") {
		t.Fatalf("medium ringing bar:\n%s", bar)
	}
}

func TestCallMenuDesktopAudioToggle(t *testing.T) {
	ctrl := &fakeCallController{}
	m := callTestModel(ctrl)
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", Video: "screen", Sharing: true, DesktopAudio: true, DesktopAudioEnabled: true})
	if bar := m.renderCallBar(200); !strings.Contains(bar, "[^G v 🔊 100%]") {
		t.Fatalf("bar:\n%s", bar)
	}
	m = press(t, m, ctrlG)
	if !strings.Contains(strings.Join(callMenuLabels(m), "|"), "Stop desktop audio") {
		t.Fatalf("menu: %v", callMenuLabels(m))
	}
	press(t, m, letter('a'))
	if len(ctrl.calls) != 1 || ctrl.calls[0] != "desktopaudio false" {
		t.Fatalf("calls %v", ctrl.calls)
	}

	// Not offered for the camera: there's no desktop to hear.
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", Video: "camera", Sharing: true, DesktopAudioEnabled: true})
	m = press(t, m, ctrlG)
	if strings.Contains(strings.Join(callMenuLabels(m), "|"), "desktop audio") {
		t.Fatalf("desktop audio offered for the camera: %v", callMenuLabels(m))
	}
}

// TestCallBarShowsWhatWeSend checks the mic and whatever we're sending show
// as buttons that switch them off, with their keys on them.
func TestCallBarShowsWhatWeSend(t *testing.T) {
	ctrl := &fakeCallController{}
	m := callTestModel(ctrl)
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", Video: "screen", Sharing: true})
	bar := m.renderCallBar(200)
	for _, want := range []string{"[^G m 🎤 on]", "[^G s 🖥 sharing ✕]", "[^G v 🔇 no desktop audio]", "[^G h hang up]"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("bar missing %q:\n%s", want, bar)
		}
	}
	m, cmd := clickZone(t, m, zoneCallStopScreen)
	nonIdleCmd(cmd)
	if len(ctrl.calls) != 1 || ctrl.calls[0] != "share false camera=false" {
		t.Fatalf("clicking the screen button: %v", ctrl.calls)
	}

	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", Muted: true, Video: "camera", Sharing: true})
	bar = m.renderCallBar(200)
	if !strings.Contains(bar, "[^G m 🔇 muted]") || !strings.Contains(bar, "[^G c 📷 on ✕]") || strings.Contains(bar, "desktop audio") {
		t.Fatalf("muted, camera bar:\n%s", bar)
	}
}
