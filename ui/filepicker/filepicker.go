// Package filepicker provides a file picker component for Bubble Tea
// applications. It is a fork of charm.land/bubbles/v2/filepicker with
// support for sorting by creation/modification time (ascending/descending),
// toggled with a keybinding, since upstream hardcodes name sorting and
// keeps sort state in unexported fields we can't override from outside.
package filepicker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustin/go-humanize"
)

var lastID int64

func nextID() int {
	return int(atomic.AddInt64(&lastID, 1))
}

// New returns a new filepicker model with default styling and key bindings.
func New() Model {
	return Model{
		id:               nextID(),
		CurrentDirectory: ".",
		Cursor:           ">",
		AllowedTypes:     []string{},
		selected:         0,
		ShowPermissions:  true,
		ShowSize:         true,
		ShowHidden:       false,
		DirAllowed:       false,
		FileAllowed:      true,
		AutoHeight:       true,
		height:           0,
		maxIdx:           0,
		minIdx:           0,
		selectedStack:    newStack(),
		minStack:         newStack(),
		maxStack:         newStack(),
		KeyMap:           DefaultKeyMap(),
		Styles:           DefaultStyles(),
		SortField:        SortByUpdated,
		SortAscending:    false,
		DirsFirst:        true,
		ShowDate:         true,
	}
}

type errorMsg struct {
	err error
}

type readDirMsg struct {
	id      int
	entries []os.DirEntry
}

const (
	marginBottom = 5
	// permissionWidth is the rendered width of a Go os.FileMode.String(),
	// e.g. "-rw-r--r--" — always exactly 10 characters.
	permissionWidth = 10
	fileSizeWidth   = 7
	// dateWidth fits the longest humanize.Time output in common use, e.g.
	// "23 minutes ago" (15 chars).
	dateWidth   = 15
	paddingLeft = 2
)

// SortField selects which timestamp file entries are sorted by.
type SortField int

const (
	// SortByUpdated sorts by modification time (mtime).
	SortByUpdated SortField = iota
	// SortByCreated sorts by creation time (ctime on Linux; the closest
	// approximation available without platform-specific birth-time APIs).
	SortByCreated
)

// String returns a short label for the sort field, e.g. for status lines.
func (f SortField) String() string {
	switch f {
	case SortByCreated:
		return "created"
	default:
		return "updated"
	}
}

// ParseSortField is String's inverse, for restoring a persisted sort field
// from config. Anything other than "created" (including "", "updated", or
// an unrecognized value) resolves to SortByUpdated.
func ParseSortField(s string) SortField {
	if s == "created" {
		return SortByCreated
	}
	return SortByUpdated
}

// KeyMap defines key bindings for each user action. Sort cycling isn't
// bound here — the host app owns that keybinding (config-overridable, and
// matched by key code rather than the layout-dependent typed character) and
// drives it through CycleSort instead.
type KeyMap struct {
	GoToTop  key.Binding
	GoToLast key.Binding
	Down     key.Binding
	Up       key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Back     key.Binding
	Open     key.Binding
	Select   key.Binding
}

// DefaultKeyMap defines the default keybindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		GoToTop:  key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "first")),
		GoToLast: key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "last")),
		Down:     key.NewBinding(key.WithKeys("j", "down", "ctrl+n"), key.WithHelp("j", "down")),
		Up:       key.NewBinding(key.WithKeys("k", "up", "ctrl+p"), key.WithHelp("k", "up")),
		PageUp:   key.NewBinding(key.WithKeys("K", "pgup"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("J", "pgdown"), key.WithHelp("pgdown", "page down")),
		Back:     key.NewBinding(key.WithKeys("h", "backspace", "left", "esc"), key.WithHelp("h", "back")),
		Open:     key.NewBinding(key.WithKeys("l", "right", "enter"), key.WithHelp("l", "open")),
		Select:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	}
}

// Styles defines the possible customizations for styles in the file picker.
type Styles struct {
	DisabledCursor   lipgloss.Style
	Cursor           lipgloss.Style
	Symlink          lipgloss.Style
	Directory        lipgloss.Style
	File             lipgloss.Style
	DisabledFile     lipgloss.Style
	Permission       lipgloss.Style
	Selected         lipgloss.Style
	DisabledSelected lipgloss.Style
	FileSize         lipgloss.Style
	Date             lipgloss.Style
	EmptyDirectory   lipgloss.Style
}

// DefaultStyles defines the default styling for the file picker.
func DefaultStyles() Styles {
	return Styles{
		DisabledCursor:   lipgloss.NewStyle().Foreground(lipgloss.Color("247")),
		Cursor:           lipgloss.NewStyle().Foreground(lipgloss.Color("212")),
		Symlink:          lipgloss.NewStyle().Foreground(lipgloss.Color("36")),
		Directory:        lipgloss.NewStyle().Foreground(lipgloss.Color("99")),
		File:             lipgloss.NewStyle(),
		DisabledFile:     lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
		DisabledSelected: lipgloss.NewStyle().Foreground(lipgloss.Color("247")),
		Permission:       lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		Selected:         lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true),
		FileSize:         lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Date:             lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		EmptyDirectory:   lipgloss.NewStyle().Foreground(lipgloss.Color("240")).PaddingLeft(paddingLeft).SetString("Bummer. No Files Found."),
	}
}

// Model represents a file picker.
type Model struct {
	id int

	// Path is the path which the user has selected with the file picker.
	Path string

	// CurrentDirectory is the directory that the user is currently in.
	CurrentDirectory string

	// AllowedTypes specifies which file types the user may select.
	// If empty the user may select any file.
	AllowedTypes []string

	KeyMap          KeyMap
	files           []os.DirEntry
	ShowPermissions bool
	ShowSize        bool
	ShowHidden      bool
	DirAllowed      bool
	FileAllowed     bool

	// SortField selects whether entries are ordered by creation or
	// modification time. Toggled via CycleSort.
	SortField SortField
	// SortAscending selects sort direction. Toggled via CycleSort.
	SortAscending bool
	// DirsFirst groups directories before files regardless of sort order,
	// each group internally sorted by SortField/SortAscending. When false,
	// directories and files are interleaved purely by SortField.
	DirsFirst bool
	// ShowDate renders the timestamp matching the active SortField (created
	// or updated, whichever is currently sorted on) alongside each entry.
	ShowDate bool

	// Width, when > 0, is the minimum rendered width of each row: the name
	// column is padded to fill it (beyond whatever the longest visible name
	// already needs), so the size/date columns land flush against the same
	// right edge on every row regardless of name length, instead of
	// trailing right after the shortest name in view. 0 falls back to
	// padding names only to the widest name currently on screen.
	Width int

	FileSelected  string
	selected      int
	selectedStack stack

	minIdx   int
	maxIdx   int
	maxStack stack
	minStack stack

	height     int
	AutoHeight bool

	Cursor string
	Styles Styles
}

type stack struct {
	Push   func(int)
	Pop    func() int
	Length func() int
}

func newStack() stack {
	slice := make([]int, 0)
	return stack{
		Push: func(i int) {
			slice = append(slice, i)
		},
		Pop: func() int {
			res := slice[len(slice)-1]
			slice = slice[:len(slice)-1]
			return res
		},
		Length: func() int {
			return len(slice)
		},
	}
}

func (m *Model) pushView(selected, minimum, maximum int) {
	m.selectedStack.Push(selected)
	m.minStack.Push(minimum)
	m.maxStack.Push(maximum)
}

func (m *Model) popView() (int, int, int) {
	return m.selectedStack.Pop(), m.minStack.Pop(), m.maxStack.Pop()
}

// entryTime returns the timestamp used for sorting/displaying a directory
// entry, according to field. Creation time falls back to mtime on
// platforms/filesystems where ctime isn't available via syscall.Stat_t.
func entryTime(field SortField, info os.FileInfo) time.Time {
	if field == SortByUpdated {
		return info.ModTime()
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(sys.Ctim.Sec, sys.Ctim.Nsec)
	}
	return info.ModTime()
}

func sortEntries(dirEntries []os.DirEntry, field SortField, ascending, dirsFirst bool) {
	sort.Slice(dirEntries, func(i, j int) bool {
		if dirsFirst && dirEntries[i].IsDir() != dirEntries[j].IsDir() {
			return dirEntries[i].IsDir()
		}
		infoI, errI := dirEntries[i].Info()
		infoJ, errJ := dirEntries[j].Info()
		if errI != nil || errJ != nil {
			return dirEntries[i].Name() < dirEntries[j].Name()
		}
		ti := entryTime(field, infoI)
		tj := entryTime(field, infoJ)
		if ti.Equal(tj) {
			return dirEntries[i].Name() < dirEntries[j].Name()
		}
		if ascending {
			return ti.Before(tj)
		}
		return ti.After(tj)
	})
}

func (m Model) readDir(path string, showHidden bool) tea.Cmd {
	sortField, sortAscending, dirsFirst := m.SortField, m.SortAscending, m.DirsFirst
	return func() tea.Msg {
		dirEntries, err := os.ReadDir(path)
		if err != nil {
			return errorMsg{err}
		}

		sortEntries(dirEntries, sortField, sortAscending, dirsFirst)

		if showHidden {
			return readDirMsg{id: m.id, entries: dirEntries}
		}

		var sanitizedDirEntries []os.DirEntry
		for _, dirEntry := range dirEntries {
			isHidden, _ := IsHidden(dirEntry.Name())
			if isHidden {
				continue
			}
			sanitizedDirEntries = append(sanitizedDirEntries, dirEntry)
		}
		return readDirMsg{id: m.id, entries: sanitizedDirEntries}
	}
}

// SetHeight sets the height of the file picker.
func (m *Model) SetHeight(h int) {
	m.height = h
	if m.maxIdx > m.height-1 {
		m.maxIdx = m.minIdx + m.height - 1
	}
}

// SetWidth sets the minimum rendered row width; see the Width field doc.
func (m *Model) SetWidth(w int) {
	m.Width = w
}

// Height returns the height of the file picker.
func (m Model) Height() int {
	return m.height
}

// Init initializes the file picker model.
func (m Model) Init() tea.Cmd {
	return m.readDir(m.CurrentDirectory, m.ShowHidden)
}

// Update handles user interactions within the file picker model.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case readDirMsg:
		if msg.id != m.id {
			break
		}
		m.files = msg.entries
		m.maxIdx = max(m.maxIdx, m.Height()-1)
	case tea.WindowSizeMsg:
		if m.AutoHeight {
			m.SetHeight(msg.Height - marginBottom)
		}
		m.maxIdx = m.Height() - 1
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.KeyMap.GoToTop):
			m.selected = 0
			m.minIdx = 0
			m.maxIdx = m.Height() - 1
		case key.Matches(msg, m.KeyMap.GoToLast):
			m.selected = len(m.files) - 1
			m.minIdx = len(m.files) - m.Height()
			m.maxIdx = len(m.files) - 1
		case key.Matches(msg, m.KeyMap.Down):
			m.selected++
			if m.selected >= len(m.files) {
				m.selected = len(m.files) - 1
			}
			if m.selected > m.maxIdx {
				m.minIdx++
				m.maxIdx++
			}
		case key.Matches(msg, m.KeyMap.Up):
			m.selected--
			if m.selected < 0 {
				m.selected = 0
			}
			if m.selected < m.minIdx {
				m.minIdx--
				m.maxIdx--
			}
		case key.Matches(msg, m.KeyMap.PageDown):
			m.selected += m.Height()
			if m.selected >= len(m.files) {
				m.selected = len(m.files) - 1
			}
			m.minIdx += m.Height()
			m.maxIdx += m.Height()

			if m.maxIdx >= len(m.files) {
				m.maxIdx = len(m.files) - 1
				m.minIdx = m.maxIdx - m.Height()
			}
		case key.Matches(msg, m.KeyMap.PageUp):
			m.selected -= m.Height()
			if m.selected < 0 {
				m.selected = 0
			}
			m.minIdx -= m.Height()
			m.maxIdx -= m.Height()

			if m.minIdx < 0 {
				m.minIdx = 0
				m.maxIdx = m.minIdx + m.Height()
			}
		case key.Matches(msg, m.KeyMap.Back):
			m.CurrentDirectory = filepath.Dir(m.CurrentDirectory)
			if m.selectedStack.Length() > 0 {
				m.selected, m.minIdx, m.maxIdx = m.popView()
			} else {
				m.selected = 0
				m.minIdx = 0
				m.maxIdx = m.Height() - 1
			}
			return m, m.readDir(m.CurrentDirectory, m.ShowHidden)
		case key.Matches(msg, m.KeyMap.Open):
			if len(m.files) == 0 {
				break
			}

			f := m.files[m.selected]
			info, err := f.Info()
			if err != nil {
				break
			}
			isSymlink := info.Mode()&os.ModeSymlink != 0
			isDir := f.IsDir()

			if isSymlink {
				symlinkPath, _ := filepath.EvalSymlinks(filepath.Join(m.CurrentDirectory, f.Name()))
				info, err := os.Stat(symlinkPath)
				if err != nil {
					break
				}
				if info.IsDir() {
					isDir = true
				}
			}

			if (!isDir && m.FileAllowed) || (isDir && m.DirAllowed) {
				if key.Matches(msg, m.KeyMap.Select) {
					// Select the current path as the selection
					m.Path = filepath.Join(m.CurrentDirectory, f.Name())
				}
			}

			if !isDir {
				break
			}

			m.CurrentDirectory = filepath.Join(m.CurrentDirectory, f.Name())
			m.pushView(m.selected, m.minIdx, m.maxIdx)
			m.selected = 0
			m.minIdx = 0
			m.maxIdx = m.Height() - 1
			return m, m.readDir(m.CurrentDirectory, m.ShowHidden)
		}
	}
	return m, nil
}

// CycleSort advances to the next of four sort states in a fixed order:
// updated-desc (default) → updated-asc → created-desc → created-asc → back,
// and returns a command to re-read the current directory under it. The host
// app calls this from its own keybinding rather than the picker owning one
// itself, so the binding stays config-overridable and matched by key code.
func (m Model) CycleSort() (Model, tea.Cmd) {
	switch {
	case m.SortField == SortByUpdated && !m.SortAscending:
		m.SortField, m.SortAscending = SortByUpdated, true
	case m.SortField == SortByUpdated && m.SortAscending:
		m.SortField, m.SortAscending = SortByCreated, false
	case m.SortField == SortByCreated && !m.SortAscending:
		m.SortField, m.SortAscending = SortByCreated, true
	default:
		m.SortField, m.SortAscending = SortByUpdated, false
	}
	m.selected = 0
	m.minIdx = 0
	m.maxIdx = m.Height() - 1
	return m, m.readDir(m.CurrentDirectory, m.ShowHidden)
}

// SortLabel returns a short human-readable description of the current sort,
// e.g. "updated desc", suitable for a status line.
func (m Model) SortLabel() string {
	dir := "desc"
	if m.SortAscending {
		dir = "asc"
	}
	return m.SortField.String() + " " + dir
}

// fileRow holds everything View needs to render one visible entry, computed
// up front so the name column can be left-padded to a common width (letting
// size/date land in a straight column at the end of the line, not wrapped
// mid-value the way a lipgloss Width-constrained style would).
type fileRow struct {
	idx         int
	entry       os.DirEntry
	info        os.FileInfo
	isSymlink   bool
	symlinkPath string
	disabled    bool
	nameDisplay string // name, plus " → target" for symlinks
	size        string
	date        string
}

func (m Model) visibleRows() ([]fileRow, int) {
	rows := make([]fileRow, 0, m.maxIdx-m.minIdx+1)
	maxNameWidth := 0
	for i, f := range m.files {
		if i < m.minIdx || i > m.maxIdx {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		name := f.Name()
		nameDisplay := name
		var symlinkPath string
		if isSymlink {
			symlinkPath, _ = filepath.EvalSymlinks(filepath.Join(m.CurrentDirectory, name))
			nameDisplay += " → " + symlinkPath
		}
		if w := lipgloss.Width(nameDisplay); w > maxNameWidth {
			maxNameWidth = w
		}
		rows = append(rows, fileRow{
			idx:         i,
			entry:       f,
			info:        info,
			isSymlink:   isSymlink,
			symlinkPath: symlinkPath,
			disabled:    !m.canSelect(name) && !f.IsDir(),
			nameDisplay: nameDisplay,
			size:        strings.Replace(humanize.Bytes(uint64(info.Size())), " ", "", 1), //nolint:gosec
			date:        humanize.Time(entryTime(m.SortField, info)),
		})
	}
	return rows, maxNameWidth
}

// rowSuffixWidth is the rendered width of everything in a row other than
// the name column: the cursor glyph, the optional permission string, the
// space separating each field, and the optional size/date columns.
func (m Model) rowSuffixWidth() int {
	w := 1 + 1 // cursor + space before name
	if m.ShowPermissions {
		w += 1 + permissionWidth
	}
	if m.ShowSize {
		w += 1 + fileSizeWidth
	}
	if m.ShowDate {
		w += 1 + dateWidth
	}
	return w
}

// View returns the view of the file picker.
func (m Model) View() string {
	if len(m.files) == 0 {
		return m.Styles.EmptyDirectory.Height(m.Height()).MaxHeight(m.Height()).String()
	}
	var s strings.Builder

	rows, maxNameWidth := m.visibleRows()
	// When Width is set, widen the name column beyond the longest visible
	// name so every row reaches Width — otherwise size/date only line up
	// with the longest name currently on screen, not the picker's actual
	// right edge, and short names leave them stranded mid-line.
	if nameColWidth := m.Width - m.rowSuffixWidth(); nameColWidth > maxNameWidth {
		maxNameWidth = nameColWidth
	}
	for _, r := range rows {
		pad := strings.Repeat(" ", maxNameWidth-lipgloss.Width(r.nameDisplay))

		if m.selected == r.idx { //nolint:nestif
			selected := ""
			if m.ShowPermissions {
				selected += " " + r.info.Mode().String()
			}
			selected += " " + r.nameDisplay + pad
			if m.ShowSize {
				selected += fmt.Sprintf(" %"+strconv.Itoa(fileSizeWidth)+"s", r.size)
			}
			if m.ShowDate {
				selected += fmt.Sprintf(" %"+strconv.Itoa(dateWidth)+"s", r.date)
			}
			if r.disabled {
				s.WriteString(m.Styles.DisabledCursor.Render(m.Cursor) + m.Styles.DisabledSelected.Render(selected))
			} else {
				s.WriteString(m.Styles.Cursor.Render(m.Cursor) + m.Styles.Selected.Render(selected))
			}
			s.WriteRune('\n')
			continue
		}

		style := m.Styles.File
		if r.entry.IsDir() {
			style = m.Styles.Directory
		} else if r.isSymlink {
			style = m.Styles.Symlink
		} else if r.disabled {
			style = m.Styles.DisabledFile
		}

		s.WriteString(m.Styles.Cursor.Render(" "))
		if m.ShowPermissions {
			s.WriteString(" " + m.Styles.Permission.Render(r.info.Mode().String()))
		}
		s.WriteString(" " + style.Render(r.nameDisplay) + pad)
		if m.ShowSize {
			s.WriteString(" " + m.Styles.FileSize.Render(fmt.Sprintf("%"+strconv.Itoa(fileSizeWidth)+"s", r.size)))
		}
		if m.ShowDate {
			s.WriteString(" " + m.Styles.Date.Render(fmt.Sprintf("%"+strconv.Itoa(dateWidth)+"s", r.date)))
		}
		s.WriteRune('\n')
	}

	for i := lipgloss.Height(s.String()); i <= m.Height(); i++ {
		s.WriteRune('\n')
	}

	return s.String()
}

// DidSelectFile returns whether a user has selected a file (on this msg).
func (m Model) DidSelectFile(msg tea.Msg) (bool, string) {
	didSelect, path := m.didSelectFile(msg)
	if didSelect && m.canSelect(path) {
		return true, path
	}
	return false, ""
}

// DidSelectDisabledFile returns whether a user tried to select a disabled file
// (on this msg). This is necessary only if you would like to warn the user that
// they tried to select a disabled file.
func (m Model) DidSelectDisabledFile(msg tea.Msg) (bool, string) {
	didSelect, path := m.didSelectFile(msg)
	if didSelect && !m.canSelect(path) {
		return true, path
	}
	return false, ""
}

func (m Model) didSelectFile(msg tea.Msg) (bool, string) {
	if len(m.files) == 0 {
		return false, ""
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// If the msg does not match the Select keymap then this could not have been a selection.
		if !key.Matches(msg, m.KeyMap.Select) {
			return false, ""
		}

		// The key press was a selection, let's confirm whether the current file could
		// be selected or used for navigating deeper into the stack.
		f := m.files[m.selected]
		info, err := f.Info()
		if err != nil {
			return false, ""
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		isDir := f.IsDir()

		if isSymlink {
			symlinkPath, _ := filepath.EvalSymlinks(filepath.Join(m.CurrentDirectory, f.Name()))
			info, err := os.Stat(symlinkPath)
			if err != nil {
				break
			}
			if info.IsDir() {
				isDir = true
			}
		}

		if (!isDir && m.FileAllowed) || (isDir && m.DirAllowed) && m.Path != "" {
			return true, m.Path
		}

		// If the msg was not a KeyPressMsg, then the file could not have been selected this iteration.
		// Only a KeyPressMsg can select a file.
	default:
		return false, ""
	}
	return false, ""
}

func (m Model) canSelect(file string) bool {
	if len(m.AllowedTypes) <= 0 {
		return true
	}

	for _, ext := range m.AllowedTypes {
		if strings.HasSuffix(file, ext) {
			return true
		}
	}
	return false
}

// HighlightedPath returns the path of the currently highlighted file or directory.
func (m Model) HighlightedPath() string {
	if len(m.files) == 0 || m.selected < 0 || m.selected >= len(m.files) {
		return ""
	}
	return filepath.Join(m.CurrentDirectory, m.files[m.selected].Name())
}
