package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func sharingScreenModel(t *testing.T, ctrl *fakeCallController) Model {
	t.Helper()
	m := callTestModel(ctrl)
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected", Video: "screen", Sharing: true,
		DesktopAudio: true, DesktopAudioEnabled: true, DesktopAudioVolume: 100})
	return m
}

func run(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	nonIdleCmd(cmd)
	return next.(Model)
}

func TestDesktopVolumeKeyboard(t *testing.T) {
	ctrl := &fakeCallController{}
	m := sharingScreenModel(t, ctrl)
	m = press(t, m, ctrlG)
	m = press(t, m, letter('v'))
	if !m.desktopVolumeOpen {
		t.Fatal("^G v didn't open the volume popup")
	}
	if !strings.Contains(m.View().Content, "100%") {
		t.Fatal("popup doesn't show the volume")
	}
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if got := strings.Join(ctrl.calls, "|"); got != "volume 110|volume 120|volume 110|desktopaudio false" {
		t.Fatalf("calls: %s", got)
	}
	// Bounds: never past 200 or below 10.
	for range 30 {
		m = run(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	}
	if m.call.desktopVolume != 200 {
		t.Fatalf("volume %d after holding right", m.call.desktopVolume)
	}
	for range 30 {
		m = run(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	if m.call.desktopVolume != 10 {
		t.Fatalf("volume %d after holding left", m.call.desktopVolume)
	}
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.desktopVolumeOpen {
		t.Fatal("esc didn't close it")
	}
}

func TestDesktopVolumeMouse(t *testing.T) {
	ctrl := &fakeCallController{}
	m := sharingScreenModel(t, ctrl)

	m, cmd := clickZone(t, m, zoneCallDesktopAudio)
	nonIdleCmd(cmd)
	if !m.desktopVolumeOpen {
		t.Fatal("clicking the bar's desktop audio button didn't open the popup")
	}
	m, cmd = clickZone(t, m, zoneDesktopVolumeCell(4))
	nonIdleCmd(cmd)
	m = run(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m, cmd = clickZone(t, m, zoneDesktopVolumeToggle)
	nonIdleCmd(cmd)
	if got := strings.Join(ctrl.calls, "|"); got != "volume 50|volume 60|desktopaudio false" {
		t.Fatalf("calls: %s", got)
	}
	m = run(t, m, tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if m.desktopVolumeOpen {
		t.Fatal("clicking outside didn't close it")
	}
}

// TestDesktopVolumeClosesWithTheShare checks the popup doesn't outlive the
// screen share it adjusts.
func TestDesktopVolumeClosesWithTheShare(t *testing.T) {
	m := sharingScreenModel(t, &fakeCallController{})
	m.openDesktopVolume()
	m, _ = m.handleCallStateMsg(CallStateMsg{Peer: "bob@example.test", SID: "s1", State: "connected"})
	if m.desktopVolumeOpen {
		t.Fatal("popup still open after the share stopped")
	}
}
