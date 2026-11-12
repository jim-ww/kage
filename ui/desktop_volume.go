package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// The desktop audio volume modal: a slider for how loud the audio shared
// along with the screen is, plus its on/off toggle. Opened from the call
// bar's desktop audio button or the call menu, driven by either keyboard
// (←/→, space) or mouse (click a cell, scroll, the hints). Every change
// goes straight to the daemon, which persists it and applies it to the
// share in progress.

// Slider range and step, in percent - matching the daemon's bounds.
const (
	desktopVolumeMin  = 10
	desktopVolumeMax  = 200
	desktopVolumeStep = 10
	// desktopVolumeCells is the slider's width: one cell per step.
	desktopVolumeCells = desktopVolumeMax / desktopVolumeStep
)

// desktopVolumeModalID finds the open volume modal, to follow the call.
const desktopVolumeModalID = "desktop-volume"

// desktopVolumeOrDefault reads an unset (0) volume as 100%.
func desktopVolumeOrDefault(percent int) int {
	if percent == 0 {
		return 100
	}
	return percent
}

// What the volume body acts with (modal.Act): a volume to set, or the
// toggle.
type (
	desktopVolumeSet    int
	desktopAudioToggled struct{}
)

// desktopVolumeBody is the volume modal's body. It shows volume and enabled
// as the model last told it (see refreshDesktopVolume) and acts with
// desktopVolumeSet/desktopAudioToggled.
type desktopVolumeBody struct {
	volume  int
	enabled bool
	accent  lipgloss.Style
	hover   int // slider cell under the pointer, or -1
	zone    *zone.Manager
	id      string
}

func (b *desktopVolumeBody) cellID(i int) string { return fmt.Sprintf("%s-cell-%d", b.id, i) }

func (b *desktopVolumeBody) cellAt(msg tea.MouseMsg) int {
	if b.zone == nil {
		return -1
	}
	for i := range desktopVolumeCells {
		if b.zone.Get(b.cellID(i)).InBounds(msg) {
			return i
		}
	}
	return -1
}

func (b *desktopVolumeBody) step(n int) (tea.Cmd, modal.Outcome) {
	return nil, modal.Act(desktopVolumeSet(b.volume + n*desktopVolumeStep))
}

func (b *desktopVolumeBody) Update(msg tea.Msg) (tea.Cmd, modal.Outcome) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case msg.String() == "left" || matchesLetter(msg, 'h'):
			return b.step(-1)
		case msg.String() == "right" || matchesLetter(msg, 'l'):
			return b.step(1)
		case msg.String() == "space" || matchesLetter(msg, callKeyDesktop):
			return nil, modal.Act(desktopAudioToggled{})
		}
	case tea.MouseClickMsg:
		if i := b.cellAt(msg); i >= 0 && msg.Button == tea.MouseLeft {
			return nil, modal.Act(desktopVolumeSet((i + 1) * desktopVolumeStep))
		}
	case tea.MouseMotionMsg:
		b.hover = b.cellAt(msg)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp, tea.MouseWheelRight:
			return b.step(1)
		case tea.MouseWheelDown, tea.MouseWheelLeft:
			return b.step(-1)
		}
	}
	return nil, modal.Continue
}

func (b *desktopVolumeBody) View() string {
	cells := make([]string, desktopVolumeCells)
	filled := b.volume / desktopVolumeStep
	for i := range cells {
		glyph := "░"
		if i < filled {
			glyph = "█"
		}
		if i == b.hover {
			glyph = b.accent.Render("▓")
		}
		if b.zone != nil {
			glyph = b.zone.Mark(b.cellID(i), glyph)
		}
		cells[i] = glyph
	}
	state := "off - only your mic is sent"
	if b.enabled {
		state = "on"
	}
	return fmt.Sprintf("%s %d%%\nDesktop audio: %s", strings.Join(cells, ""), b.volume, state)
}

func (b *desktopVolumeBody) PreferredWidth() int { return desktopVolumeCells + 6 }

func (b *desktopVolumeBody) Bind(z *zone.Manager, id string) { b.zone, b.id = z, id }

func (b *desktopVolumeBody) Hints() []modal.Hint {
	toggle := "turn on"
	if b.enabled {
		toggle = "turn off"
	}
	// The toggle first, so it's the last to give way on a narrow screen:
	// quieter/louder can be clicked and scrolled on the slider too.
	return []modal.Hint{
		modal.KeyHint(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, toggle),
		modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyLeft}, "quieter"),
		modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyRight}, "louder"),
	}
}

// openDesktopVolume opens the volume modal, if there's a screen share whose
// audio it could adjust.
func (m *Model) openDesktopVolume() tea.Cmd {
	if m.call == nil || m.call.video != "screen" || m.callController == nil {
		return m.showNotification("not sharing the screen")
	}
	body := &desktopVolumeBody{hover: -1, accent: lipgloss.NewStyle().Foreground(m.styles.colors.accentCyan)}
	cmd := m.openModal(&modal.Modal[*Model]{
		ID:    desktopVolumeModalID,
		Title: "Desktop audio volume",
		Body:  body,
		OnAct: func(m *Model, v any) tea.Cmd {
			switch v := v.(type) {
			case desktopVolumeSet:
				return m.setDesktopVolume(int(v))
			case desktopAudioToggled:
				return m.toggleDesktopAudio()
			}
			return nil
		},
	})
	m.refreshDesktopVolume()
	return cmd
}

// refreshDesktopVolume shows the call's current volume and toggle in the
// open volume modal, or closes it once there's no screen share to adjust.
func (m *Model) refreshDesktopVolume() {
	md := m.modals.Find(desktopVolumeModalID)
	if md == nil {
		return
	}
	if m.call == nil || m.call.video != "screen" {
		m.modals.Remove(desktopVolumeModalID)
		return
	}
	body := md.Body.(*desktopVolumeBody)
	body.volume, body.enabled = desktopVolumeOrDefault(m.call.desktopVolume), m.call.desktopAudioEnabled
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
	m.refreshDesktopVolume()
	ctrl := m.callController
	return func() tea.Msg { return ctrl.SetDesktopAudioVolume(percent) }
}
