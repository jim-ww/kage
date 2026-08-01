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

	return strings.Join(lines, "\n")
}

func (m Model) replyPreview(idx int, allMsgs []Message) string {
	if idx < 0 || idx >= len(allMsgs) {
		return ""
	}
	orig := allMsgs[idx]
	preview := strings.ReplaceAll(orig.Content, "\n", " ")
	runes := []rune(preview)
	if len(runes) > 30 {
		preview = string(runes[:27]) + "…"
	}
	return fmt.Sprintf("↪ %s: %s", orig.Author, preview)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
