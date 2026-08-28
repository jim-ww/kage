package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
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
	nameWidth := maxSenderNameWidth(msgs)

	var sb strings.Builder
	currentLine := 0

	for i, msg := range msgs {
		if i > 0 {
			sb.WriteByte('\n')
			currentLine++
		}
		offsets[i] = currentLine
		rendered := m.zone.Mark(zoneMessage(i), padLinesToWidth(m.renderMessage(msg, i, cw, msgs, nameWidth), cw))
		sb.WriteString(rendered)
		currentLine += strings.Count(rendered, "\n")
	}
	return sb.String(), offsets
}

// formatMessageTime formats a message timestamp per the user's config: a
// custom Go time layout when set, otherwise a bare "15:04" - the date isn't
// repeated on every message at all, since a date-divider line (see
// dateDividerLabel/renderDateDivider) already marks it once per day.
func (m Model) formatMessageTime(t time.Time) string {
	if m.timeLayout != "" {
		return t.Format(m.timeLayout)
	}
	return t.Format("15:04")
}

// dateDividerLabel formats the date a day-divider line shows: just "Jan 2"
// within the current year, "Jan 2 2025" once the year isn't obvious anymore.
func dateDividerLabel(t, now time.Time) string {
	if t.Year() == now.Year() {
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2 2006")
}

// messageDateDivider returns the day-divider line to show right before msg
// (empty if none is needed) - whenever it's the first message in the loaded
// history, or the previous message fell on a different calendar day.
func (m Model) messageDateDivider(msgIdx, width int, allMsgs []Message) string {
	if msgIdx < 0 || msgIdx >= len(allMsgs) {
		return ""
	}
	if msgIdx > 0 && sameDay(allMsgs[msgIdx].SentAt, allMsgs[msgIdx-1].SentAt) {
		return ""
	}
	return m.styles.renderDateDivider(width, dateDividerLabel(allMsgs[msgIdx].SentAt, time.Now()))
}

// callLogLine renders a call-log entry's compact "📞 ..." line instead of the
// normal author/content/timestamp bubble layout — same muted/italic styling
// as a deleted message, since it's informational rather than real chat
// content.
func (m Model) callLogLine(msg Message, msgIdx, totalWidth int) string {
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
	line := m.styles.messageDeleted.Render(fmt.Sprintf("%s %s [%s]", glyph, text, timeLabel))
	isSelected := msgIdx == m.selectedMsg
	if isSelected {
		line = m.styles.messageBar.Render("▌") + " " + line
	} else {
		line = "  " + line
	}
	if isSelected || m.isHovered(zoneMessage(msgIdx)) {
		line = m.styles.rowBackgroundTint(padLinesToWidth(line, totalWidth+2))
	}
	return line
}

// maxCollapsedBodyLines is how many wrapped body lines a message shows
// before collapsing behind a "show more" button - clicking (or the row's
// own expand zone) toggles it open via Model.expandedMsgs.
const maxCollapsedBodyLines = 6

// maxSenderNameDisplayWidth caps how wide a single sender name is allowed to
// make every message's "name" column - without it, one contact with an
// unusually long name/JID localpart would push the label (and so the start
// of every message's body) uselessly far right for the whole chat.
const maxSenderNameDisplayWidth = 16

// senderDisplayName returns name as-is unless it looks like a bare JID (no
// resolved nickname upstream), in which case only the localpart before '@'
// is shown - a full JID is too wide to sit at the start of every line in
// the message list. Also truncates to maxSenderNameDisplayWidth runes with
// a trailing "…", so a single long name can't blow out the shared name
// column every message in the chat is padded to (see maxSenderNameWidth).
func senderDisplayName(name string) string {
	if at := strings.IndexByte(name, '@'); at >= 0 {
		name = name[:at]
	}
	runes := []rune(name)
	if len(runes) > maxSenderNameDisplayWidth {
		return string(runes[:maxSenderNameDisplayWidth-1]) + "…"
	}
	return name
}

// msgKey returns a stable key for a message's per-row UI state (currently
// just expandedMsgs) that survives history reloads/pagination, unlike a
// raw slice index: prefers the server-assigned stanza ID, falling back to
// the client-generated correlation ID for not-yet-acked outgoing echoes,
// and only to the index when neither is set (e.g. synthetic rows).
func msgKey(msg Message, idx int) string {
	if msg.ID != "" {
		return msg.ID
	}
	if msg.LocalID != "" {
		return "local:" + msg.LocalID
	}
	return fmt.Sprintf("idx:%d", idx)
}

// maxSenderNameWidth returns the widest displayed sender name across msgs
// (call-log rows excluded, since they don't render one) - used to pad every
// message's name label to a common width so everything that follows it
// lines up down the whole chat instead of jittering with each sender's
// name length.
func maxSenderNameWidth(msgs []Message) int {
	width := 0
	for _, msg := range msgs {
		if msg.CallLog != nil {
			continue
		}
		if w := lipgloss.Width(senderDisplayName(msg.Author)); w > width {
			width = w
		}
	}
	return width
}

func (m Model) renderMessage(msg Message, msgIdx, totalWidth int, allMsgs []Message, nameWidth int) string {
	divider := m.messageDateDivider(msgIdx, totalWidth, allMsgs)
	if divider != "" {
		divider += "\n"
	}

	if msg.CallLog != nil {
		return divider + m.callLogLine(msg, msgIdx, totalWidth)
	}
	isSelected := msgIdx == m.selectedMsg
	rowHovered := m.isHovered(zoneMessage(msgIdx))

	nameStyle := m.styles.messageNickThem
	if msg.IsMe {
		nameStyle = m.styles.messageNickMe
	}
	name := senderDisplayName(msg.Author)
	label := strings.Repeat(" ", max(0, nameWidth-lipgloss.Width(name))) + name
	prefixWidth := lipgloss.Width(label) + 1 // trailing space between name and what follows it
	pad := strings.Repeat(" ", prefixWidth)

	headerLine := nameStyle.Render(label)
	if label != "" {
		headerLine += " "
	}
	hasTextQuoteReply := false
	if msg.ReplyTo != nil {
		reply := m.replyHeaderFragment(*msg.ReplyTo, allMsgs)
		headerLine += m.zone.Mark(zoneMessageReply(msgIdx), reply)
	}

	var bodyContent string
	switch {
	case msg.Retracted:
		// The chat view never shows retracted content — only that a delete
		// happened. Original content/attachments remain available in the
		// info popup (ctrl+i); we never actually erase local history.
		bodyContent = "*deleted*"
	case len(msg.Attachments) > 0:
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
	default:
		content := msg.Content
		// A real XEP-0461 reply (Message.ReplyTo) always wins over the
		// text convention below - a message can't be both.
		if msg.ReplyTo == nil {
			if preview, rest, ok := parseQuoteReply(content); ok {
				hasTextQuoteReply = true
				content = rest
				headerLine += m.styles.renderQuoteReplyFragment(preview)
			}
		}
		bodyContent = FormatMessageBody(content)
	}

	// Body text trails directly after the name on the header's own line
	// when there's no reply quote to make room for; a real reply or a
	// parsed ">"-quote convention both push the body down to its own line
	// instead, since the quote already occupies the space right after the
	// name.
	bodyOnHeaderLine := msg.ReplyTo == nil && !hasTextQuoteReply
	wrapWidth := max(totalWidth-prefixWidth, 8)
	bodyLines := strings.Split(ansi.Wrap(bodyContent, wrapWidth, " "), "\n")
	fullBodyLineCount := len(bodyLines)

	key := msgKey(msg, msgIdx)
	expanded := m.expandedMsgs[key]
	if !expanded && len(bodyLines) > maxCollapsedBodyLines {
		bodyLines = bodyLines[:maxCollapsedBodyLines]
	}
	needsExpandButton := fullBodyLineCount > maxCollapsedBodyLines

	var lines []string
	for i, line := range bodyLines {
		var styled string
		if msg.Retracted {
			styled = m.styles.messageDeleted.Render(line)
		} else {
			styled = m.styles.plainTextLine(line)
		}
		switch {
		case i == 0 && bodyOnHeaderLine:
			lines = append(lines, headerLine+styled)
		case i == 0:
			lines = append(lines, headerLine)
			lines = append(lines, pad+styled)
		default:
			lines = append(lines, pad+styled)
		}
	}
	if len(bodyLines) == 0 {
		lines = append(lines, headerLine)
	}

	if needsExpandButton {
		expandHovered := m.isExpandButtonHovered(msgIdx)
		lines = append(lines, pad+m.zone.Mark(zoneMessageExpand(msgIdx), m.styles.renderMsgExpandButton(expanded, expandHovered)))
	}

	lines = append(lines, pad+m.renderMessageStatusLine(msg, msgIdx, isSelected))

	// The left-edge bar (thick, red) marks selection only; it must sit flush
	// against the pane's own left edge - not indented behind a padding
	// column - so unselected rows get the same 2-cell-wide gap filled with
	// blank space instead, keeping every row's text aligned regardless of
	// whether the bar is showing.
	if isSelected {
		bar := m.styles.messageBar.Render("▌") + " "
		for i, line := range lines {
			lines[i] = bar + line
		}
	} else {
		for i, line := range lines {
			lines[i] = "  " + line
		}
	}

	// Selecting (keyboard) or hovering (mouse) tints every line of the row
	// with a subtle background instead of underlining individual fragments,
	// so the whole message reads as one highlighted block regardless of
	// which input method chose it. Padded out to the row's full width
	// first (bar/gutter included) so the tint fills the entire line instead
	// of stopping wherever that line's own text/status content happened to
	// end.
	if isSelected || rowHovered {
		for i, line := range lines {
			lines[i] = m.styles.rowBackgroundTint(padLinesToWidth(line, totalWidth+2))
		}
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

	return divider + strings.Join(lines, "\n")
}

// renderMessageStatusLine renders the line under a message's body: dimmed
// time/date, then - only while the message is selected - the "^r reply"/
// "^t react" buttons, then an edited marker, (for our own messages) the
// send/delivery status glyph, and finally any reactions on the message.
func (m Model) renderMessageStatusLine(msg Message, msgIdx int, isSelected bool) string {
	timeLabel := m.styles.messageTime.Render(m.formatMessageTime(msg.SentAt))
	parts := []string{timeLabel}

	if isSelected {
		replyKey := m.zone.Mark(zoneMessageReplyKey(msgIdx), m.styles.renderMsgActionKey("^r", true, m.isReplyKeyHovered(msgIdx)))
		reactKey := m.zone.Mark(zoneMessageReactKey(msgIdx), m.styles.renderMsgActionKey("^t", false, m.isReactKeyHovered(msgIdx)))
		replyBtn := m.zone.Mark(zoneMessageReplyBtn(msgIdx), replyKey+" "+m.styles.renderMsgActionLabel("reply"))
		reactBtn := m.zone.Mark(zoneMessageReactBtn(msgIdx), reactKey+" "+m.styles.renderMsgActionLabel("react"))
		parts = append(parts, replyBtn, reactBtn)
	}

	// Edited/encrypted/delivery are all small badges of the same kind -
	// joined by a single space between themselves, tighter than the double
	// space separating time/buttons/badges from each other.
	var badges []string

	if msg.Edited {
		editIcon := "edited"
		if m.icons {
			editIcon = "✎"
		}
		badges = append(badges, m.styles.messageTime.Render(editIcon))
	}

	if msg.Encrypted && m.showEncryptedIcon {
		lockIcon := "enc"
		if m.icons {
			lockIcon = "🔒"
		}
		badges = append(badges, m.styles.messageTime.Render(lockIcon))
	}

	if msg.IsMe {
		var status string
		switch {
		case msg.Failed:
			// Never rendered the same as a plain unconfirmed send (no status
			// glyph at all) - that ambiguity is exactly what let a message
			// that was never actually transmitted sit in the chat looking
			// like every other line. See Message.Failed's doc comment.
			status = "✗"
		case msg.Pending:
			status = "…"
		case msg.ID != "" && msg.Delivered:
			// The peer's client has it, which implies the server did too -
			// shown regardless of ServerAcked, since a delivery receipt can
			// race ahead of our own ping-based server confirmation.
			status = "✓✓"
		case msg.ID != "" && msg.ServerAcked:
			// Our server confirmed it has this (see Message.ServerAcked's doc
			// comment) - not yet peer-delivered, or no receipt is expected.
			status = "✓"
			// Sent locally (has an ID) but neither confirmed by the server nor
			// delivered yet: deliberately no glyph at all, rather than a
			// guessed "✓" - see Message.ServerAcked's doc comment for why a
			// local send succeeding is not proof the server ever got it.
		}
		if status != "" {
			badges = append(badges, m.styles.messageTime.Render(status))
		}
	}

	if len(badges) > 0 {
		parts = append(parts, strings.Join(badges, " "))
	}

	if len(msg.Reactions) > 0 {
		parts = append(parts, m.renderClickableReactions(msg.Reactions, msgIdx))
	}

	return strings.Join(parts, "  ")
}

// renderClickableReactions renders msg's aggregate reactions the same way
// renderReactions does ("😂 2 👍"), but with each one wrapped in its own
// click zone (see zoneMessageReaction) and our own contributions (Mine)
// bolded, so it's visible at a glance which reactions clicking would remove
// versus add. Each chip also highlights independently on hover (see
// isReactionHovered) - hovering one never affects its siblings. Clicking
// toggles our own reaction of that emoji - add if we haven't reacted with
// it, remove if we have (see toggleMyReaction).
func (m Model) renderClickableReactions(reactions []Reaction, msgIdx int) string {
	parts := make([]string, len(reactions))
	for i, r := range reactions {
		text := r.Emoji
		if r.Count > 1 {
			text = fmt.Sprintf("%s %d", r.Emoji, r.Count)
		}
		style := m.styles.plainText
		if r.Mine {
			style = style.Bold(true)
		}
		if m.isReactionHovered(msgIdx, i) {
			style = style.Reverse(true)
		}
		parts[i] = m.zone.Mark(zoneMessageReaction(msgIdx, i), style.Render(text))
	}
	return strings.Join(parts, " ")
}

// toggleMyReaction returns the emoji set our own reaction on the message
// should become after clicking reactions[i]'s emoji: every emoji we've
// already reacted with, except emoji is removed if it was already ours or
// added if it wasn't.
func toggleMyReaction(reactions []Reaction, emoji string) []string {
	mine := make(map[string]bool, len(reactions))
	for _, r := range reactions {
		if r.Mine {
			mine[r.Emoji] = true
		}
	}
	if mine[emoji] {
		delete(mine, emoji)
	} else {
		mine[emoji] = true
	}
	out := make([]string, 0, len(mine))
	for e := range mine {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// renderReactions formats a message's aggregate reactions as "😂 2 👍" —
// the count is only shown when more than one person reacted with that emoji.
func renderReactions(reactions []Reaction) string {
	parts := make([]string, len(reactions))
	for i, r := range reactions {
		if r.Count > 1 {
			parts[i] = fmt.Sprintf("%s %d", r.Emoji, r.Count)
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

// replyHeaderFragment renders the "↑ name │ preview" fragment trailing a
// reply's header line: the quoted author's name in the same color they get
// as a sender in their own messages, the truncated preview text dimmed.
func (m Model) replyHeaderFragment(idx int, allMsgs []Message) string {
	if idx < 0 || idx >= len(allMsgs) {
		return ""
	}
	orig := allMsgs[idx]
	nameStyle := m.styles.messageNickThem
	if orig.IsMe {
		nameStyle = m.styles.messageNickMe
	}
	name := nameStyle.Render(senderDisplayName(orig.Author))
	preview := m.styles.messageTime.Render(previewText(MessagePreviewContent(orig), previewLen))
	sep := m.styles.messageTime.Render("│")
	return "↑ " + name + " " + sep + " " + preview
}

// quoteLinePrefix matches a line that's part of an email/IRC-style quoted
// reply: optional leading whitespace, then one or more '>' markers (one per
// nesting level - "> foo" quotes once, ">> foo" quotes a message that was
// itself already a quote), then the quoted text.
var quoteLinePrefix = regexp.MustCompile(`^\s*>+\s?`)

// parseQuoteReply detects a leading run of quoted lines (see quoteLinePrefix)
// at the start of content and splits it into a synthetic reply preview -
// the last (least-nested) quoted line, markers stripped - and the actual
// new text following the quote block. Unlike a real XEP-0461 reply
// (Message.ReplyTo), this is just a text convention with no message to
// jump to: some clients (and manually-typed replies) still write it this
// way instead of using an actual reply. ok is false if content doesn't
// start with at least one quoted line, or if nothing follows the quote
// block (nothing left to show as the message's own body).
func parseQuoteReply(content string) (preview, rest string, ok bool) {
	lines := strings.Split(content, "\n")
	i := 0
	for i < len(lines) && quoteLinePrefix.MatchString(lines[i]) {
		i++
	}
	if i == 0 {
		return "", content, false
	}
	rest = strings.TrimLeft(strings.Join(lines[i:], "\n"), "\n")
	if rest == "" {
		return "", content, false
	}
	preview = quoteLinePrefix.ReplaceAllString(lines[i-1], "")
	return preview, rest, true
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
