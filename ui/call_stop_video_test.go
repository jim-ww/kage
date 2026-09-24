package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fakeCallController records the ScreenShare calls the call bar makes; every
// other CallController method is an unused stub.
type fakeCallController struct {
	shareCalls []bool // one entry per ScreenShare, the requested "sharing"
}

func (f *fakeCallController) StartCall(int, string) tea.Msg            { return nil }
func (f *fakeCallController) StartVideoCall(int, string, bool) tea.Msg { return nil }
func (f *fakeCallController) AnswerCall(int) tea.Msg                   { return nil }
func (f *fakeCallController) HangupCall(int) tea.Msg                   { return nil }
func (f *fakeCallController) RejectCall(int) tea.Msg                   { return nil }
func (f *fakeCallController) MuteCall(int, bool) tea.Msg               { return nil }
func (f *fakeCallController) ReopenVideo(int) tea.Msg                  { return nil }
func (f *fakeCallController) ScreenShare(_ int, sharing, _ bool) tea.Msg {
	f.shareCalls = append(f.shareCalls, sharing)
	return nil
}

// sharingModel builds a model sitting on a connected call that's currently
// sending our own video.
func sharingModel(ctrl CallController) Model {
	m := newTestModel(nil)
	m.callController = ctrl
	m.call = &callUIState{state: "connected", peer: "bob@localhost", sharing: true}
	return m
}

// TestCallBarOffersStopWhileSharing checks the rendered call bar carries a
// stop control while we're sharing — the keybind existed long before any
// visible way to discover it, which read as "there's no way to stop".
func TestCallBarOffersStopWhileSharing(t *testing.T) {
	m := sharingModel(&fakeCallController{})
	bar := m.renderCallBar(m.width)
	if !strings.Contains(bar, "stop video") {
		t.Fatalf("call bar while sharing has no stop control:\n%s", bar)
	}

	m.call.sharing = false
	bar = m.renderCallBar(m.width)
	if strings.Contains(bar, "stop video") {
		t.Fatalf("call bar offers a stop control while not sharing:\n%s", bar)
	}
}

// TestStopVideoKeyStopsSharing checks ctrl+v on a sharing call asks the
// daemon to stop, rather than reopening the camera/screen prompt.
func TestStopVideoKeyStopsSharing(t *testing.T) {
	ctrl := &fakeCallController{}
	m := sharingModel(ctrl)

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'v', BaseCode: 'v', Mod: tea.ModCtrl})
	m = next.(Model)
	if m.videoSourcePrompt {
		t.Fatal("ctrl+v while sharing opened the video source prompt instead of stopping")
	}
	if cmd == nil {
		t.Fatal("ctrl+v while sharing produced no command")
	}
	nonIdleCmd(cmd)

	if len(ctrl.shareCalls) != 1 || ctrl.shareCalls[0] {
		t.Fatalf("expected one ScreenShare(false) call, got %v", ctrl.shareCalls)
	}
}
