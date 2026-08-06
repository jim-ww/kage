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
		rendered := m.zone.Mark(zoneMessage(i), padLinesToWidth(m.renderMessage(msg, i, cw, msgs), cw))
		sb.WriteString(rendered)
		currentLine += strings.Count(rendered, "\n")
	}
	return sb.String(), offsets
}

// formatMessageTime formats a message timestamp per the user's config: a
// custom Go time layout when set, otherwise "15:04" for today's messages
// (if timeOnlyToday) or "2006-01-02 15:04" for anything older/when
// timeOnlyToday is off.
func (m Model) formatMessageTime(t time.Time) string {
	if m.timeLayout != "" {
		return t.Format(m.timeLayout)
	}
	if m.timeOnlyToday && sameDay(t, time.Now()) {
		return t.Format("15:04")
	}
	return t.Format("2006-01-02 15:04")
}

func (m Model) renderMessage(msg Message, msgIdx, totalWidth int, allMsgs []Message) string {
	isSelected := msgIdx == m.selectedMsg
	prefix := m.styles.renderMessagePrefix(isSelected, m.isHovered(zoneMessage(msgIdx)))

	timeLabel := m.formatMessageTime(msg.SentAt)
	if msg.Encrypted {
		lockIcon := "enc"
		if m.icons {
			lockIcon = "🔒"
		}
		timeLabel += " " + lockIcon
	}
	dirGlyph := "«"
	if msg.IsMe {
		dirGlyph = "»"
		if msg.ID != "" {
			status := "✓"
			if msg.Delivered {
				status = "✓✓"
			}
			timeLabel += " " + status
		}
	}
	name := ""
	if m.showNames {
		name = msg.Author + " "
	}
	headerPlain := fmt.Sprintf("%s %s[%s ] ", dirGlyph, name, timeLabel)
	header := m.styles.renderMessageHeader(name, timeLabel, msg.IsMe)
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

	var bodyContent string
	if msg.Retracted {
		// The chat view never shows retracted content — only that a delete
		// happened. Original content/attachments remain available in the
		// info popup (ctrl+i); we never actually erase local history.
		bodyContent = "*deleted*"
	} else if len(msg.Attachments) > 0 {
		// Attachments is authoritative (XEP-0066), not derived from Content -
		// a sender's fallback body text isn't guaranteed to literally end
		// with the attachment URLs, so this only strips them from the
		// displayed text as a best-effort de-dupe when it does, rather than
		// gating whether attachment styling shows at all.
		text := strings.TrimSpace(msg.Content)
		if joined := strings.Join(msg.Attachments, "\n"); strings.HasSuffix(text, joined) {
			text = strings.TrimSpace(strings.TrimSuffix(text, joined))
		}
		var parts []string
		if text != "" {
			parts = append(parts, highlightCodeBlocks(text))
		}
		for _, a := range msg.Attachments {
			parts = append(parts, renderAttachmentLine(a, m.icons))
		}
		bodyContent = strings.Join(parts, "\n")
	} else {
		bodyContent = highlightCodeBlocks(msg.Content)
	}
	bodyLines := strings.Split(ansi.Wrap(bodyContent, wrapWidth, " "), "\n")
	for i, line := range bodyLines {
		if msg.Retracted {
			line = m.styles.messageDeleted.Render(line)
		} else {
			line = m.styles.plainTextLine(line)
		}
		if i == 0 {
			lines = append(lines, prefix+header+line)
			continue
		}
		lines = append(lines, "  "+indent+line)
	}

	if len(msg.Reactions) > 0 {
		lines = append(lines, "  "+indent+m.styles.plainText.Render(renderReactions(msg.Reactions)))
	}

	return strings.Join(lines, "\n")
}

// renderReactions formats a message's aggregate reactions as "😂×2 👍" —
// the count is only shown when more than one person reacted with that emoji.
func renderReactions(reactions []Reaction) string {
	parts := make([]string, len(reactions))
	for i, r := range reactions {
		if r.Count > 1 {
			parts[i] = fmt.Sprintf("%s×%d", r.Emoji, r.Count)
		} else {
			parts[i] = r.Emoji
		}
	}
	return strings.Join(parts, " ")
}

func (m Model) replyPreview(idx int, allMsgs []Message) string {
	if idx < 0 || idx >= len(allMsgs) {
		return ""
	}
	orig := allMsgs[idx]
	return fmt.Sprintf("↪ %s: %s", orig.Author, previewText(messagePreviewContent(orig), previewLen))
}

// previewLen is the shared truncation budget for single-line message
// previews shown in reply hints and delete-confirmation popups.
const previewLen = 40

// messagePreviewContent returns the text to preview for msg in a reply quote
// or hint: for a lone-attachment message, Content is the raw upload URL
// (aesgcm:// or https://), which isn't meaningful to a human — show the
// decoded filename instead, same as the attachment's own rendered body line.
func messagePreviewContent(msg Message) string {
	if len(msg.Attachments) == 1 && strings.TrimSpace(msg.Content) == msg.Attachments[0] {
		return attachmentDisplayName(msg.Attachments[0])
	}
	return msg.Content
}

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
