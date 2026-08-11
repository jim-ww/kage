package ui

import (
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// progressReader wraps an io.Reader, calling onProgress after every Read
// with cumulative bytes read so far and the (already-known) total. Mirrors
// xmpp.progressReader (upload side) - kept as a separate copy rather than a
// shared package since ui must never import xmpp (see CLAUDE.md), and this
// is a handful of lines.
type progressReader struct {
	io.Reader
	total      int64
	sent       int64
	onProgress func(sent, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.Reader.Read(b)
	p.sent += int64(n)
	if p.onProgress != nil {
		p.onProgress(p.sent, p.total)
	}
	return n, err
}

// transferProgressChanMsg wraps a FileTransferProgressMsg together with the
// channel it came from, so the Update handler can re-issue
// listenForTransferChan to keep draining further messages from the same
// transfer. The transfer's own terminal message (openResultMsg/
// saveResultMsg) is sent on the same channel but isn't wrapped this way, so
// listening naturally stops once it arrives - see listenForTransferChan.
type transferProgressChanMsg struct {
	FileTransferProgressMsg
	ch chan tea.Msg
}

// listenForTransferChan returns a Cmd that reads exactly one message off ch
// and returns it verbatim. Used by a download's tea.Cmd to drain a
// background goroutine's progress + terminal messages one at a time into
// the Bubble Tea update loop, since a Cmd can otherwise only ever return a
// single message.
func listenForTransferChan(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// throttledProgressSender returns an onProgress func (sent, total int64)
// that sends a transferProgressChanMsg on ch at most once per whole
// percentage point, so a large download doesn't flood the channel with a
// message per 32KB read. Drops (rather than blocks on) a full channel - the
// next percentage change will likely get through.
func throttledProgressSender(ch chan tea.Msg, id, label string) func(sent, total int64) {
	lastPercent := -1
	return func(sent, total int64) {
		percent := -1
		if total > 0 {
			percent = int(sent * 100 / total)
		}
		if percent == lastPercent {
			return
		}
		lastPercent = percent
		select {
		case ch <- transferProgressChanMsg{FileTransferProgressMsg{ID: id, Label: label, Sent: sent, Total: total}, ch}:
		default:
		}
	}
}

// startOpen begins opening target unless a download of the same URL is
// already in flight - mashing the open key/button before the first press's
// download finishes would otherwise fire off a duplicate concurrent
// download of the same file for no benefit (the first one already
// satisfies both presses). A plain link/local path never downloads (see
// openWithXDGOpen), so those aren't tracked or deduped - only re-launching
// the browser on it, which is harmless.
func (m *Model) startOpen(target string, isAttachment bool) tea.Cmd {
	chat, _ := m.currentChat()
	isRemoteURL := strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "aesgcm://")
	downloadFirst := strings.HasPrefix(target, "aesgcm://") || (isAttachment && isRemoteURL)
	if !downloadFirst {
		return openWithXDGOpen(target, isAttachment, chat.Address)
	}
	if m.downloadsInFlight[target] {
		return nil
	}
	if m.downloadsInFlight == nil {
		m.downloadsInFlight = make(map[string]bool)
	}
	m.downloadsInFlight[target] = true
	delete(m.finishedTransfers, target)
	return openWithXDGOpen(target, isAttachment, chat.Address)
}

// startSave begins downloading target to the downloads directory unless a
// download of the same URL is already in flight - see startOpen.
func (m *Model) startSave(target string) tea.Cmd {
	if m.downloadsInFlight[target] {
		return nil
	}
	if m.downloadsInFlight == nil {
		m.downloadsInFlight = make(map[string]bool)
	}
	m.downloadsInFlight[target] = true
	delete(m.finishedTransfers, target)
	return saveURLToDownloads(target)
}

// startSaveAs begins downloading target to the user-chosen dest path (from
// the "save as" prompt) unless a download of the same URL is already in
// flight - see startOpen.
func (m *Model) startSaveAs(target, dest string) tea.Cmd {
	if m.downloadsInFlight[target] {
		return nil
	}
	if m.downloadsInFlight == nil {
		m.downloadsInFlight = make(map[string]bool)
	}
	m.downloadsInFlight[target] = true
	delete(m.finishedTransfers, target)
	return saveURLToPath(target, dest)
}

// setTransferProgress upserts a transfer's progress, tracking insertion
// order (transferOrder) separately so renderTransferLines has a stable,
// start-order rendering instead of Go's randomized map iteration. A no-op if
// the transfer's terminal result message already arrived - progress and the
// terminal message travel over separate channels (IPC broadcast vs. direct
// RPC return) with no ordering guarantee, so a late progress event (e.g. the
// final 100%) can otherwise arrive after clearTransfer and resurrect an
// entry that will never be cleared again.
func (m *Model) setTransferProgress(msg FileTransferProgressMsg) {
	if m.finishedTransfers[msg.ID] {
		return
	}
	if m.transfers == nil {
		m.transfers = make(map[string]FileTransferProgressMsg)
	}
	if _, exists := m.transfers[msg.ID]; !exists {
		m.transferOrder = append(m.transferOrder, msg.ID)
	}
	m.transfers[msg.ID] = msg
}

// clearTransfer removes a finished transfer (its terminal result message
// arrived) so it stops being rendered, and marks its ID as finished so a
// late-arriving progress message can't resurrect it - see setTransferProgress.
func (m *Model) clearTransfer(id string) {
	if m.finishedTransfers == nil {
		m.finishedTransfers = make(map[string]bool)
	}
	m.finishedTransfers[id] = true
	if _, ok := m.transfers[id]; !ok {
		return
	}
	delete(m.transfers, id)
	for i, k := range m.transferOrder {
		if k == id {
			m.transferOrder = append(m.transferOrder[:i], m.transferOrder[i+1:]...)
			break
		}
	}
}

// transferBarWidth is the fixed width (in characters) of the "[####----]"
// portion of a rendered transfer line.
const transferBarWidth = 16

// renderTransferLines returns one progress line per active transfer, in the
// order each one started, or nil if none are active.
func (m Model) renderTransferLines() []string {
	if len(m.transferOrder) == 0 {
		return nil
	}
	lines := make([]string, 0, len(m.transferOrder))
	for _, id := range m.transferOrder {
		tp, ok := m.transfers[id]
		if !ok {
			continue
		}
		lines = append(lines, formatTransferLine(tp))
	}
	return lines
}

// formatTransferLine renders one transfer as "<label> [####----] NN%", or
// "<label>... <bytes>" while the total size isn't known yet (e.g. a
// download before the response headers arrive).
func formatTransferLine(tp FileTransferProgressMsg) string {
	if tp.Total <= 0 {
		return fmt.Sprintf("%s... %s", tp.Label, humanBytes(tp.Sent))
	}
	percent := int(tp.Sent * 100 / tp.Total)
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%s %s %3d%%", tp.Label, renderProgressBar(percent, transferBarWidth), percent)
}

// renderProgressBar draws a "[####----]" bar, ASCII-only so it renders
// correctly in every terminal (same reasoning as plainFileIcons).
func renderProgressBar(percent, width int) string {
	if width < 1 {
		width = 1
	}
	filled := width * percent / 100
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-filled) + "]"
}

// humanBytes formats a byte count as e.g. "512 B", "3.1 KB", "22.4 MB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
