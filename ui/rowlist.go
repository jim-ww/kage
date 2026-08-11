package ui

import tea "charm.land/bubbletea/v2"

// pagedCursor is the cursor+page state shared by popups that show a
// navigable, paginated list of rows (contact manager, OMEMO device list):
// up/down/j/k move the cursor, left/right/h/l change page (resetting the
// cursor to the top of the new page). Embed it anonymously so its cursor and
// page fields promote straight onto the embedding popup state, e.g.
// cs.cursor/cs.page keep working unchanged. Page bucketing itself comes from
// openPageBounds/openPageCount (openable.go); this only adds the
// keyboard/mouse navigation on top.
type pagedCursor struct {
	cursor int
	page   int
}

// handleNavKey handles up/down/j/k and left/right/h/l against a page holding
// rowCount of a total items spread across pages, returning true if msg was a
// navigation key (and so was consumed).
func (pc *pagedCursor) handleNavKey(msg tea.KeyMsg, rowCount, total int) bool {
	switch {
	case msg.String() == "up" || matchesLetter(msg, 'k'):
		pc.cursor = max(0, pc.cursor-1)
	case msg.String() == "down" || matchesLetter(msg, 'j'):
		pc.cursor = min(rowCount-1, pc.cursor+1)
	case msg.String() == "left" || matchesLetter(msg, 'h'):
		pc.page = max(0, pc.page-1)
		pc.cursor = 0
	case msg.String() == "right" || matchesLetter(msg, 'l'):
		if pc.page < openPageCount(total)-1 {
			pc.page++
		}
		pc.cursor = 0
	default:
		return false
	}
	return true
}

// bounds returns the current page's slice bounds into a total-length list.
func (pc *pagedCursor) bounds(total int) (start, end int) {
	return openPageBounds(total, pc.page)
}

// renderRow marks label as zoneID and prefixes it with the cursor/hover
// indicator, for building a popup's paginated row list.
func (m Model) renderRow(zoneID string, i, cursor int, label string) string {
	prefix := m.styles.renderMessagePrefix(i == cursor, m.isHovered(zoneID))
	return m.zone.Mark(zoneID, prefix+label)
}

// rowUnderMouse returns the index (of rowCount rows, named by zoneID) the
// mouse is over, or -1 if it's over none of them.
func (m Model) rowUnderMouse(mouse tea.MouseMsg, rowCount int, zoneID func(int) string) int {
	for i := range rowCount {
		if m.zone.Get(zoneID(i)).InBounds(mouse) {
			return i
		}
	}
	return -1
}
