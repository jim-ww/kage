package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// renderMessagesWithOffsets renders all messages for the active chat and
// returns the full string plus the line-offset of each message within it.
//
// This used to render only a windowed subset of the loaded messages
// (centered on the selection) to bound the cost of charm.land/bubbles
// viewport.SetContentLines, which rescans every line it's given on every
// call — but decoupling "what's rendered" from "what's loaded" broke every
// piece of code that used viewport.YOffset()==0/AtBottom() as a proxy for
// "reached the start/end of all loaded messages" (mouse wheel and
// PageUp/PageDown scrolling), causing scrolling to get stuck or jump to
// unrelated messages once a chat's loaded history exceeded the window.
// maxMessagesPerChat already bounds how many messages are loaded at all
// (default 200), and the other fixes in this area (a much smaller Model
// struct, content-addressed sidebar/viewport-frame caches) cut enough of
// the real cost that windowing on top of them wasn't worth the correctness
// risk — see the removal in git history if this cost matters again.
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

// callLogLine renders a call-log entry's compact "📞 ..." line instead of the
// normal author/content/timestamp bubble layout — same muted/italic styling
// as a deleted message, since it's informational rather than real chat
// content.
func (m Model) callLogLine(msg Message, msgIdx int) string {
	glyph := "call"
	if m.icons {
		glyph = "📞"
	}
	var text string
	switch msg.CallLog.Outcome {
	case "missed":
		text = "Missed call"
	case "declined":
		text = "Declined call"
	case "failed":
		text = "Call failed"
	case "answered":
		dir := "Incoming"
		if msg.CallLog.Direction == "outgoing" {
			dir = "Outgoing"
		}
		text = fmt.Sprintf("%s call · %s", dir, formatCallDuration(msg.CallLog.Duration))
	default:
		text = "Call ended"
	}
	timeLabel := m.formatMessageTime(msg.SentAt)
	style := m.styles.messageDeleted
	switch {
	case msgIdx == m.selectedMsg:
		style = style.Bold(true)
	case m.isHovered(zoneMessage(msgIdx)):
		style = style.Underline(true)
	}
	return style.Render(fmt.Sprintf("%s %s [%s]", glyph, text, timeLabel))
}

func (m Model) renderMessage(msg Message, msgIdx, totalWidth int, allMsgs []Message) string {
	if msg.CallLog != nil {
		return m.callLogLine(msg, msgIdx)
	}
	isSelected := msgIdx == m.selectedMsg
	rowHovered := m.isHovered(zoneMessage(msgIdx))

	timeLabel := m.formatMessageTime(msg.SentAt)
	if msg.Encrypted && m.showEncryptedIcon {
		lockIcon := "enc"
		if m.icons {
			lockIcon = "🔒"
		}
		timeLabel += " " + lockIcon
	}
	if msg.Edited {
		editIcon := "edited"
		if m.icons {
			editIcon = "✎"
		}
		timeLabel += " " + editIcon
	}
	if msg.IsMe {
		switch {
		case msg.Failed:
			// Never rendered the same as a plain unconfirmed send (no status
			// glyph at all) - that ambiguity is exactly what let a message
			// that was never actually transmitted sit in the chat looking
			// like every other line. See Message.Failed's doc comment.
			timeLabel += " ✗"
		case msg.Pending:
			timeLabel += " …"
		case msg.ID != "" && msg.Delivered:
			// The peer's client has it, which implies the server did too -
			// shown regardless of ServerAcked, since a delivery receipt can
			// race ahead of our own ping-based server confirmation.
			timeLabel += " ✓✓"
		case msg.ID != "" && msg.ServerAcked:
			// Our server confirmed it has this (see Message.ServerAcked's doc
			// comment) - not yet peer-delivered, or no receipt is expected.
			timeLabel += " ✓"
			// Sent locally (has an ID) but neither confirmed by the server nor
			// delivered yet: deliberately no glyph at all, rather than a
			// guessed "✓" - see Message.ServerAcked's doc comment for why a
			// local send succeeding is not proof the server ever got it.
		}
	}
	name := ""
	if m.showNames {
		name = msg.Author + " "
	}
	// isSelected/rowHovered double as the message's own selection/hover
	// indicator (bold/underline on the header) - there's no separate
	// left-hand cursor glyph, so this doesn't cost a column.
	header := m.styles.renderMessageHeader(name, timeLabel, msg.IsMe, isSelected, rowHovered)
	// The header (dir glyph/name/timestamp/badges) is its own line, varying
	// in length message to message - content always starts on the next line
	// flush against the left edge instead of trailing the header inline, so
	// an encrypted/edited/receipt badge showing up on one message never
	// shifts where a neighboring message's text starts.
	// The reply button trails directly after the header, shown whenever the
	// row is selected or hovered - so it moves right along with keyboard
	// navigation between messages, not just mouse hover. It doesn't eat into
	// wrapWidth since the body text below never shares a line with it.
	wrapWidth := max(totalWidth, 8)
	var replyBtn string
	if isSelected || rowHovered {
		replyBtnHovered := rowHovered && m.isReplyButtonHovered(msgIdx)
		replyBtn = " " + m.zone.Mark(zoneMessageReplyBtn(msgIdx), m.styles.renderReplyButton(m.icons, replyBtnHovered))
	}

	var lines []string
	if msg.ReplyTo != nil {
		reply := m.replyPreview(*msg.ReplyTo, allMsgs)
		replyWrapped := strings.SplitSeq(ansi.Wrap(reply, max(8, totalWidth-2), " "), "\n")
		for line := range replyWrapped {
			// Marked with its own zone (nested inside the outer zoneMessage)
			// so a click here jumps to the quoted message instead of
			// replying to this one.
			lines = append(lines, m.zone.Mark(zoneMessageReply(msgIdx), "  "+m.styles.messageReply.Render(line)))
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
			parts = append(parts, FormatMessageBody(text))
		}
		chat, _ := m.currentChat()
		for _, a := range msg.Attachments {
			parts = append(parts, renderAttachmentLine(a, m.icons, chat.Address))
		}
		bodyContent = strings.Join(parts, "\n")
	} else {
		bodyContent = FormatMessageBody(msg.Content)
	}
	lines = append(lines, header+replyBtn)

	bodyLines := strings.Split(ansi.Wrap(bodyContent, wrapWidth, " "), "\n")
	for _, line := range bodyLines {
		if msg.Retracted {
			line = m.styles.messageDeleted.Render(line)
		} else {
			line = m.styles.plainTextLine(line)
		}
		lines = append(lines, line)
	}

	if len(msg.Reactions) > 0 {
		lines = append(lines, m.styles.plainText.Render(renderReactions(msg.Reactions)))
	}

	if m.flashMsgIdx >= 0 && msgIdx == m.flashMsgIdx {
		// Strip each line's own ANSI styling first: nesting messageFlash's
		// background around already-styled text would just lose the
		// background at the first reset code the inner styling emits (e.g.
		// after the header's foreground color), leaving the rest of the line
		// unhighlighted.
		for i, line := range lines {
			lines[i] = m.styles.messageFlash.Render(ansi.Strip(line))
		}
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
	return fmt.Sprintf("↪ %s: %s", orig.Author, previewText(MessagePreviewContent(orig), previewLen))
}

// previewLen is the shared truncation budget for single-line message
// previews shown in reply hints and delete-confirmation popups.
const previewLen = 40

// MessagePreviewContent returns the text to preview for msg anywhere it's
// shown as a single line (reply quotes/hints, delete-confirmation popups,
// the chat list's last-message preview): for an attachment message, Content
// is (or ends with) the raw upload URL(s) (aesgcm:// or https://), which
// aren't meaningful to a human and can leak sensitive material (an aesgcm://
// URL's fragment is the file's decryption key) — show the decoded
// filename(s) instead, same as the attachment's own rendered body line.
func MessagePreviewContent(msg Message) string {
	if len(msg.Attachments) == 0 {
		return StripMessageStyling(msg.Content)
	}
	text := strings.TrimSpace(msg.Content)
	if joined := strings.Join(msg.Attachments, "\n"); strings.HasSuffix(text, joined) {
		text = strings.TrimSpace(strings.TrimSuffix(text, joined))
	}
	text = StripMessageStyling(text)
	names := make([]string, len(msg.Attachments))
	for i, a := range msg.Attachments {
		names[i] = attachmentDisplayName(a)
	}
	label := strings.Join(names, ", ")
	if text != "" {
		return text + " [" + label + "]"
	}
	return label
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
