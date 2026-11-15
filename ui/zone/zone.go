// Package zone finds where marked parts of a rendered frame ended up on
// screen, for mouse hit-testing: Mark wraps a part in invisible markers,
// Scan strips them from the final frame and records each part's bounds,
// Get looks them up. It has bubblezone's API, but Scan is linear in the
// frame and its zones can be queried as soon as it returns.
package zone

import (
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/cells"
)

// ZoneInfo is a zone's bounds in the last scanned frame, 0-based and
// inclusive.
type ZoneInfo struct {
	StartX, StartY int
	EndX, EndY     int
}

// IsZero reports whether z is no zone.
func (z *ZoneInfo) IsZero() bool { return z == nil }

// InBounds reports whether msg's position is inside z.
func (z *ZoneInfo) InBounds(msg tea.MouseMsg) bool {
	if z.IsZero() || z.StartX > z.EndX || z.StartY > z.EndY {
		return false
	}
	e := msg.Mouse()
	return e.X >= z.StartX && e.Y >= z.StartY && e.X <= z.EndX && e.Y <= z.EndY
}

// Pos is msg's position relative to z's top left, or -1, -1 outside it.
func (z *ZoneInfo) Pos(msg tea.MouseMsg) (x, y int) {
	if !z.InBounds(msg) {
		return -1, -1
	}
	e := msg.Mouse()
	return e.X - z.StartX, e.Y - z.StartY
}

// Manager holds the marker of every id marked so far and the zones of the
// last scanned frame. Its methods are safe for concurrent use.
type Manager struct {
	mu       sync.RWMutex
	disabled bool
	markers  map[string]string // id -> marker
	ids      map[string]string // marker -> id
	zones    map[string]*ZoneInfo
}

// New returns an empty Manager.
func New() *Manager {
	return &Manager{markers: map[string]string{}, ids: map[string]string{}, zones: map[string]*ZoneInfo{}}
}

// SetEnabled turns marking on or off: Mark returns its input as is while
// it's off.
func (m *Manager) SetEnabled(enabled bool) {
	m.mu.Lock()
	m.disabled = !enabled
	m.mu.Unlock()
}

// Mark wraps v in id's markers. An empty id or v is returned as is.
func (m *Manager) Mark(id, v string) string {
	if id == "" || v == "" {
		return v
	}
	m.mu.RLock()
	marker, disabled := m.markers[id], m.disabled
	m.mu.RUnlock()
	if disabled {
		return v
	}
	if marker == "" {
		m.mu.Lock()
		if marker = m.markers[id]; marker == "" {
			// A CSI sequence no terminal acts on: Scan strips it anyway.
			marker = "\x1b[" + strconv.Itoa(1000+len(m.markers)) + "z"
			m.markers[id] = marker
			m.ids[marker] = id
		}
		m.mu.Unlock()
	}
	return marker + v + marker
}

// Get is id's zone in the last scanned frame, nil if it wasn't in it.
func (m *Manager) Get(id string) *ZoneInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.zones[id]
}

// Scan strips the markers from frame and makes the zones they delimit the
// ones Get reports, replacing the previous frame's.
func (m *Manager) Scan(frame string) string {
	var (
		out       strings.Builder
		copied    int // frame[:copied] is in out, markers dropped
		lineStart int // where the current line starts in out
		y         int
		open      = map[string]*ZoneInfo{}
		zones     = map[string]*ZoneInfo{}
	)
	out.Grow(len(frame))
	// x is the width of the current line up to frame[i].
	x := func(i int) int {
		return cells.Width(out.String()[lineStart:]) + cells.Width(frame[copied:i])
	}

	m.mu.RLock()
	for i := 0; i < len(frame); i++ {
		switch frame[i] {
		case '\n':
			out.WriteString(frame[copied : i+1])
			copied = i + 1
			lineStart = out.Len()
			y++
		case '\x1b':
			end := markerEnd(frame, i)
			if end < 0 {
				continue
			}
			id, ok := m.ids[frame[i:end]]
			if !ok {
				continue
			}
			if z, started := open[id]; started {
				z.EndX, z.EndY = x(i)-1, y
				zones[id] = z
				delete(open, id)
			} else {
				open[id] = &ZoneInfo{StartX: x(i), StartY: y}
			}
			out.WriteString(frame[copied:i])
			copied = end
			i = end - 1
		}
	}
	m.mu.RUnlock()
	out.WriteString(frame[copied:])

	m.mu.Lock()
	m.zones = zones
	m.mu.Unlock()
	return out.String()
}

// markerEnd is the end of the "\x1b[<digits>z" marker at frame[i], or -1.
func markerEnd(frame string, i int) int {
	j := i + 2
	if j >= len(frame) || frame[i+1] != '[' {
		return -1
	}
	for j < len(frame) && frame[j] >= '0' && frame[j] <= '9' {
		j++
	}
	if j == i+2 || j >= len(frame) || frame[j] != 'z' {
		return -1
	}
	return j + 1
}
