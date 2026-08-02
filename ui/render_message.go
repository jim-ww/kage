package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderMessagesWithOffsets renders all messages for the active chat and
// returns the full string plus the line-offset of each message within it.
func (m Model) renderMessagesWithOffsets() (string, []int) {
	cw := m.chatAreaWidth()
	if cw <= 10 {
		return "", nil
	}

	chatIdx := m.currentChatIndex()
	if chatIdx < 0 {
		return "", nil
	}
	msgs := m.currentMessages()
	offsets := make([]int, len(msgs))

	var sb strings.Builder
	currentLine := 0

	for i, msg := range msgs {
		if i > 0 {
			sb.WriteByte('\n')
			currentLine++
		}
		offsets[i] = currentLine
		rendered := m.renderMessage(msg, i, cw, msgs)
		sb.WriteString(rendered)
		currentLine += strings.Count(rendered, "\n") + 1
	}
	return sb.String(), offsets
}

func (m Model) renderMessage(msg Message, msgIdx, totalWidth int, allMsgs []Message) string {
	isSelected := msgIdx == m.selectedMsg
	prefix := m.styles.renderMessagePrefix(isSelected)

	nick := msg.Author
	timeLabel := msg.SentAt.Format("15:04")
	if !sameDay(msg.SentAt, time.Now()) {
		timeLabel = msg.SentAt.Format("2006-01-02 15:04")
	}
	headerPlain := fmt.Sprintf("[%s] <%s> ", timeLabel, nick)
	header := m.styles.renderMessageHeader(timeLabel, nick, msg.IsMe)
	indent := strings.Repeat(" ", lipgloss.Width(headerPlain))
	wrapWidth := totalWidth - lipgloss.Width(prefix) - lipgloss.Width(indent)
	wrapWidth = max(wrapWidth, 8)

	var lines []string
	if msg.ReplyTo != nil {
		reply := m.replyPreview(*msg.ReplyTo, allMsgs)
		replyWrapped := strings.SplitSeq(ansi.Wrap(reply, max(8, totalWidth-lipgloss.Width(prefix)-2), " "), "\n")
		for line := range replyWrapped {
			lines = append(lines, prefix+m.styles.messageReply.Render(line))
			prefix = "  "
		}
	}

	bodyLines := strings.Split(ansi.Wrap(msg.Content, wrapWidth, " "), "\n")
	for i, line := range bodyLines {
		if i == 0 {
			lines = append(lines, prefix+header+line)
			continue
		}
		lines = append(lines, "  "+indent+line)
	}

	if msg.Retracted {
		// Content stays visible — we don't trust a remote retraction to
		// erase what was said on our side — but the attempt is flagged.
		lines = append(lines, "  "+indent+m.styles.messageReply.Render("(sender attempted to delete this message)"))
	}

	return strings.Join(lines, "\n")
}

func (m Model) replyPreview(idx int, allMsgs []Message) string {
	if idx < 0 || idx >= len(allMsgs) {
		return ""
	}
	orig := allMsgs[idx]
	return fmt.Sprintf("↪ %s: %s", orig.Author, previewText(orig.Content, previewLen))
}

// previewLen is the shared truncation budget for single-line message
// previews shown in reply hints and delete-confirmation popups.
const previewLen = 40

// previewText collapses newlines and truncates s to at most n runes,
// appending an ellipsis when truncated.
func previewText(s string, n int) string {
	flat := strings.ReplaceAll(s, "\n", " ")
	runes := []rune(flat)
	if len(runes) <= n {
		return flat
	}
	return string(runes[:n]) + "…"
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
