package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/jim-ww/kage/ui/filepicker"
	"github.com/jim-ww/kage/ui/modal"
)

// filePickerModalID finds the open file picker, to close it with the
// avatar preview it led to.
const filePickerModalID = "file-picker"

// What the file picker acts with: a file picked (it stays open, to attach
// more), its sort changed (to persist), a file it won't take.
type (
	filePicked         string
	filePickerSorted   struct{}
	filePickerDisabled struct{}
)

// filePickerBody is the file picker (ui/filepicker) as a modal body: the
// directory on top with ‹ › to go back and forward, its rows under it. A
// directory opens on a click, a file is picked on a second click; the
// keyboard drives the picker itself, plus the sort key and . for hidden
// files. The picker is the Model's, kept between openings so it reopens
// where it was.
type filePickerBody struct {
	picker    *filepicker.Model
	keys      *KeyMap
	colors    uiColors
	zone      *zone.Manager
	id        string
	lastRow   int
	lastClick time.Time
	width     int
}

func (b *filePickerBody) rowID(i int) string { return fmt.Sprintf("%s-row-%d", b.id, i) }

func (b *filePickerBody) rowAt(msg tea.MouseMsg) int {
	if b.zone == nil {
		return -1
	}
	for i := range b.picker.Height() {
		if b.zone.Get(b.rowID(i)).InBounds(msg) {
			return i
		}
	}
	return -1
}

func (b *filePickerBody) Update(msg tea.Msg) (tea.Cmd, modal.Outcome) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case matchesKey(msg, b.keys.AttachFile):
			return nil, modal.Cancel()
		case matchesKey(msg, b.keys.SortFilePicker):
			return b.picker.CycleSort(), modal.Act(filePickerSorted{})
		case matchesLetter(msg, '.'):
			return b.picker.ToggleHidden(), modal.Continue
		}
		cmd := b.picker.Update(msg)
		if selected, path := b.picker.DidSelectFile(msg); selected {
			return cmd, modal.Act(filePicked(path))
		}
		if disabled, _ := b.picker.DidSelectDisabledFile(msg); disabled {
			return cmd, modal.Act(filePickerDisabled{})
		}
		return cmd, modal.Continue
	case tea.MouseMotionMsg:
		if row := b.rowAt(msg); row >= 0 {
			b.picker.SelectRow(row)
		}
		return nil, modal.Continue
	case tea.MouseWheelMsg:
		key := tea.KeyPressMsg{Code: tea.KeyDown}
		if msg.Button == tea.MouseWheelUp {
			key = tea.KeyPressMsg{Code: tea.KeyUp}
		}
		return b.picker.Update(key), modal.Continue
	case tea.MouseClickMsg:
		return b.click(msg)
	case tea.MouseMsg:
		return nil, modal.Continue
	}
	// Directory reads arrive as messages of the picker's own.
	return b.picker.Update(msg), modal.Continue
}

// click goes back or forward on ‹ ›, and on a row selects it - opening it
// right away if it's a directory (a click there can never be a final
// pick), and on a second click within the double-click window otherwise.
// Opening goes through the same Enter the keyboard uses.
func (b *filePickerBody) click(msg tea.MouseClickMsg) (tea.Cmd, modal.Outcome) {
	if msg.Button != tea.MouseLeft || b.zone == nil {
		return nil, modal.Continue
	}
	switch {
	case b.zone.Get(b.id+"-back").InBounds(msg) && b.picker.CanGoBack():
		return b.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	case b.zone.Get(b.id+"-forward").InBounds(msg) && b.picker.CanGoForward():
		return b.picker.GoForward(), modal.Continue
	}
	row := b.rowAt(msg)
	if row < 0 {
		return nil, modal.Continue
	}
	now := time.Now()
	double := b.lastRow == row && now.Sub(b.lastClick) < 500*time.Millisecond
	b.lastRow, b.lastClick = row, now
	b.picker.SelectRow(row)
	if double || b.picker.IsDirRow(row) {
		return b.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	return nil, modal.Continue
}

func (b *filePickerBody) navButton(glyph, id string, enabled bool) string {
	if !enabled {
		return lipgloss.NewStyle().Foreground(b.colors.time).Render(glyph)
	}
	out := lipgloss.NewStyle().Foreground(b.colors.themFg).Bold(true).Render(glyph)
	if b.zone != nil {
		out = b.zone.Mark(id, out)
	}
	return out
}

func (b *filePickerBody) View() string {
	nav := b.navButton("‹", b.id+"-back", b.picker.CanGoBack()) + " " + b.navButton("›", b.id+"-forward", b.picker.CanGoForward()) + "  "
	dir := b.picker.CurrentDirectory
	if avail := b.width - lipgloss.Width(nav); avail > 1 && lipgloss.Width(dir) > avail {
		// Cut from the left, keeping the directory's most specific end.
		dir = "…" + ansi.TruncateLeft(dir, lipgloss.Width(dir)-avail+1, "")
	}
	lines := strings.Split(b.picker.View(), "\n")
	for i := range lines {
		lines[i] = padLinesToWidth(lines[i], b.width)
		if b.zone != nil {
			lines[i] = b.zone.Mark(b.rowID(i), lines[i])
		}
	}
	return nav + dir + "\n" + strings.Join(lines, "\n")
}

func (b *filePickerBody) SetSize(width, height int) {
	b.width = width
	b.picker.SetHeight(max(1, height-1))
	b.picker.SetMaxWidth(width)
	b.picker.SetWidth(width)
}

func (b *filePickerBody) PreferredWidth() int { return 80 }

func (b *filePickerBody) Bind(z *zone.Manager, id string) { b.zone, b.id = z, id }

func (b *filePickerBody) Hints() []modal.Hint {
	sort := tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	if keys := b.keys.SortFilePicker.Keys(); len(keys) > 0 && keys[0] != "ctrl+s" {
		sort = tea.KeyPressMsg{Text: keys[0]}
	}
	return []modal.Hint{
		modal.KeyHint(tea.KeyPressMsg{Code: tea.KeyEnter}, "open/pick"),
		modal.KeyHint(sort, "sort: "+b.picker.SortLabel()),
		modal.KeyHint(tea.KeyPressMsg{Code: '.', Text: "."}, "hidden files"),
	}
}

// openFilePicker opens the file picker: to attach files to the message
// being written (staged, the picker staying open for more), or with avatar
// set, to pick the account's avatar (shown in a preview first, see
// startAvatarPreview).
func (m *Model) openFilePicker(avatar bool) tea.Cmd {
	title := "Attach file"
	if avatar {
		title = "Choose an avatar"
	}
	body := &filePickerBody{picker: m.filePicker, keys: m.keys, colors: m.styles.colors, lastRow: -1}
	cmd := m.openModal(&modal.Modal[*Model]{
		ID:    filePickerModalID,
		Title: title,
		Body:  body,
		OnAct: func(m *Model, v any) tea.Cmd {
			switch v := v.(type) {
			case filePicked:
				if avatar {
					return m.startAvatarPreview(m.currentAccount, string(v))
				}
				// Staged, not uploaded: nothing touches the network until
				// the message is sent (see sendCurrentInput/startAttachedSend).
				m.stageAttachment(string(v))
				m.updateSizes()
				return nil
			case filePickerSorted:
				if err := m.daemon.SetFilePickerSort(m.filePicker.SortField.String(), m.filePicker.SortAscending); err != nil {
					return m.showNotification("saving file picker sort: " + err.Error())
				}
			case filePickerDisabled:
				return m.showNotification("that file type cannot be selected")
			}
			return nil
		},
	})
	return tea.Batch(cmd, m.filePicker.Init())
}
