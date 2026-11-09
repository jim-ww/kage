package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The desktop audio volume popup: a slider for how loud the audio shared
// along with the screen is, plus its on/off toggle. Opened from the call
// bar's desktop audio button or the call menu, driven by either keyboard
// (←/→, space, esc) or mouse (click a cell, scroll, click the toggle, click
// outside to close). Every change goes straight to the daemon, which
// persists it and applies it to the share in progress.

// Slider range and step, in percent - matching the daemon's bounds.
const (
	desktopVolumeMin  = 10
	desktopVolumeMax  = 200
	desktopVolumeStep = 10
	// desktopVolumeCells is the slider's width: one cell per step.
	desktopVolumeCells = desktopVolumeMax / desktopVolumeStep
)

const (
	zoneDesktopVolumePopup  = "desktop-volume-popup"
	zoneDesktopVolumeToggle = "desktop-volume-toggle"
)

func zoneDesktopVolumeCell(i int) string { return fmt.Sprintf("desktop-volume-cell-%d", i) }

// desktopVolumeOrDefault reads an unset (0) volume as 100%.
func desktopVolumeOrDefault(percent int) int {
	if percent == 0 {
		return 100
	}
	return percent
}

// openDesktopVolume opens the popup, if there's a screen share whose audio
// it could adjust.
func (m *Model) openDesktopVolume() tea.Cmd {
	if m.call == nil || m.call.video != "screen" || m.callController == nil {
		return m.showNotification("not sharing the screen")
	}
	m.desktopVolumeOpen = true
	return nil
}

// setDesktopVolume sets the volume to percent (clamped and snapped to a
// step), showing it at once rather than waiting for the daemon's echo.
func (m *Model) setDesktopVolume(percent int) tea.Cmd {
	if m.call == nil || m.callController == nil {
		return nil
	}
	percent = max(desktopVolumeMin, min(desktopVolumeMax, percent/desktopVolumeStep*desktopVolumeStep))
	if percent == m.call.desktopVolume {
		return nil
	}
	m.call.desktopVolume = percent
	ctrl := m.callController
	return func() tea.Msg { return ctrl.SetDesktopAudioVolume(percent) }
}

// updateDesktopVolumeKey handles every key while the popup is open.
func (m Model) updateDesktopVolumeKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch {
	case msg.String() == "left" || matchesLetter(msg, 'h'):
		return m, m.setDesktopVolume(m.call.desktopVolume - desktopVolumeStep), true
	case msg.String() == "right" || matchesLetter(msg, 'l'):
		return m, m.setDesktopVolume(m.call.desktopVolume + desktopVolumeStep), true
	case msg.String() == "space" || matchesLetter(msg, callKeyDesktop):
		return m, m.toggleDesktopAudio(), true
	case matchesKey(msg, m.keys.Back), matchesKey(msg, m.keys.SelectSend), matchesKey(msg, m.keys.ConfirmNo):
		m.desktopVolumeOpen = false
	}
	return m, nil, true
}

// handleDesktopVolumeClick: a slider cell sets the volume to it, the
// toggle flips desktop audio, anything outside the popup closes it.
func (m Model) handleDesktopVolumeClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if msg.Mouse().Button != tea.MouseLeft {
		return m, nil
	}
	for i := range desktopVolumeCells {
		if m.zone.Get(zoneDesktopVolumeCell(i)).InBounds(msg) {
			return m, m.setDesktopVolume((i + 1) * desktopVolumeStep)
		}
	}
	if m.zone.Get(zoneDesktopVolumeToggle).InBounds(msg) {
		return m, m.toggleDesktopAudio()
	}
	if !m.zone.Get(zoneDesktopVolumePopup).InBounds(msg) {
		m.desktopVolumeOpen = false
	}
	return m, nil
}

// handleDesktopVolumeWheel steps the volume, wheel up for louder.
func (m Model) handleDesktopVolumeWheel(msg tea.MouseWheelMsg) (Model, tea.Cmd) {
	switch msg.Mouse().Button {
	case tea.MouseWheelUp, tea.MouseWheelRight:
		return m, m.setDesktopVolume(m.call.desktopVolume + desktopVolumeStep)
	case tea.MouseWheelDown, tea.MouseWheelLeft:
		return m, m.setDesktopVolume(m.call.desktopVolume - desktopVolumeStep)
	}
	return m, nil
}

// desktopVolumeZoneUnderMouse is the hoverable part of the popup under the
// pointer, if any.
func (m Model) desktopVolumeZoneUnderMouse(mouse tea.MouseMsg) string {
	for i := range desktopVolumeCells {
		if id := zoneDesktopVolumeCell(i); m.zone.Get(id).InBounds(mouse) {
			return id
		}
	}
	if m.zone.Get(zoneDesktopVolumeToggle).InBounds(mouse) {
		return zoneDesktopVolumeToggle
	}
	return ""
}

func (m Model) renderDesktopVolumePopup() string {
	c := m.call
	cells := make([]string, desktopVolumeCells)
	filled := c.desktopVolume / desktopVolumeStep
	for i := range cells {
		glyph := "░"
		if i < filled {
			glyph = "█"
		}
		if m.isHovered(zoneDesktopVolumeCell(i)) {
			glyph = lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan).Render("▓")
		}
		cells[i] = m.zone.Mark(zoneDesktopVolumeCell(i), glyph)
	}
	slider := fmt.Sprintf("  %s %d%%", strings.Join(cells, ""), c.desktopVolume)

	toggle := "[space] on"
	if c.desktopAudioEnabled {
		toggle = "[space] off"
	}
	state := "off - only your mic is sent"
	if c.desktopAudioEnabled {
		state = "on"
	}
	rows := []string{
		"",
		slider,
		"",
		"  Desktop audio: " + state + "    " + m.zone.Mark(zoneDesktopVolumeToggle, m.styles.renderConfirmButton(toggle, m.isHovered(zoneDesktopVolumeToggle))),
		"",
		"  ←/→ or scroll: volume · click: set",
	}
	body := m.styles.listPopup("Desktop audio volume", rows, "[esc] close")
	popup := m.zone.Mark(zoneDesktopVolumePopup, m.styles.popupDialog(m.styles.colors.borderA, m.popupWidth(), body))
	return lipgloss.Place(m.chatAreaWidth(), m.popupAreaHeight(), lipgloss.Center, lipgloss.Center, popup)
}
