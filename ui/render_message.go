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

// renderMessagesWithOffsets renders every loaded message of the chat and
// each one's line offset. All of them, not a window: scrolling takes the
// viewport's top and bottom for the loaded messages' ends.
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

	for i := range msgs {
		if i > 0 {
			sb.WriteByte('\n')
			currentLine++
		}
		offsets[i] = currentLine
		rendered := m.zone.Mark(zoneMessage(i), m.renderMessageRow(msgs, i, cw, nameWidth))
		sb.WriteString(rendered)
		currentLine += strings.Count(rendered, "\n")
	}
	return sb.String(), offsets
}

// formatMessageTime formats a message's time: timeLayout, or "15:04", the
// date being on day dividers.
func (m Model) formatMessageTime(t time.Time) string {
	if m.timeLayout != "" {
		return t.Format(m.timeLayout)
	}
	return t.Format("15:04")
}

// dateDividerLabel formats the date a day-divider line shows: "Tue 26 Aug"
// within the current year, "Tue 26 Aug 2025" once the year isn't obvious
// anymore.
func dateDividerLabel(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if t.Year() == now.Year() {
		return t.Format("Mon 2 Jan")
	}
	return t.Format("Mon 2 Jan 2006")
}

// messageDateDivider returns the day-divider line to show right before msg
// (empty if none is needed) - whenever it's the first message in the loaded
// history, or the previous message fell on a different calendar day.
func (m Model) messageDateDivider(msgIdx int, allMsgs []Message) string {
	if msgIdx < 0 || msgIdx >= len(allMsgs) {
		return ""
	}
	if msgIdx > 0 && sameDay(allMsgs[msgIdx].SentAt, allMsgs[msgIdx-1].SentAt) {
		return ""
	}
	return m.styles.renderDateDivider(dateDividerLabel(allMsgs[msgIdx].SentAt, time.Now()))
}

// callLogLine renders a call-log entry's one line, styled like a deleted
// message.
func (m Model) callLogLine(msg Message, msgIdx, totalWidth int) string {
	glyph := "call"
	if m.icons {
		glyph = "📞"
	}
	text := callLogText(*msg.CallLog)
	timeLabel := m.formatMessageTime(msg.SentAt)
	line := m.styles.messageDeleted.Render(fmt.Sprintf("%s %s [%s]", glyph, text, timeLabel))
	isSelected := msgIdx == m.selectedMsg
	if isSelected {
		line = m.styles.messageBar.Render("▌") + " " + line
	} else {
		line = "  " + line
	}
	if isSelected || m.isHovered(zoneMessage(msgIdx)) {
		line = m.styles.rowBackgroundTint(padLinesToWidth(line, totalWidth))
	}
	return line
}

// callLogText renders a call log's outcome as a short human-readable label,
// shared between the message list's compact call-log row and the chat
// list's last-message preview.
func callLogText(info CallLogInfo) string {
	switch info.Outcome {
	case "missed":
		return "Missed call"
	case "declined":
		return "Declined call"
	case "failed":
		return "Call failed"
	case "answered":
		dir := "Incoming"
		if info.Direction == "outgoing" {
			dir = "Outgoing"
		}
		return fmt.Sprintf("%s call · %s", dir, formatCallDuration(info.Duration))
	default:
		return "Call ended"
	}
}

// maxCollapsedBodyLines is how many wrapped body lines a message shows
// before collapsing behind a "show more" button - clicking (or the row's
// own expand zone) toggles it open via Model.expandedMsgs.
const maxCollapsedBodyLines = 15

// maxSenderNameDisplayWidth caps the name column one long name would widen.
const maxSenderNameDisplayWidth = 16

// senderDisplayName is name, a JID's localpart only, truncated to
// maxSenderNameDisplayWidth.
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

// msgKey keys a message's row state (expandedMsgs) across history loads:
// its ID, else LocalID, else index.
func msgKey(msg Message, idx int) string {
	if msg.ID != "" {
		return msg.ID
	}
	if msg.LocalID != "" {
		return "local:" + msg.LocalID
	}
	return fmt.Sprintf("idx:%d", idx)
}

// maxSenderNameWidth is the widest sender name in msgs, which every name is
// padded to so bodies line up.
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

// msgRowKey identifies one rendered message row: its index plus every
// piece of Model state renderMessage's output varies on. The message's own
// content is deliberately *not* part of the key — see msgRowCacheState.
type msgRowKey struct {
	idx         int
	selected    bool
	rowHovered  bool
	replyKey    bool
	reactKey    bool
	expandBtn   bool
	expanded    bool
	flashed     bool
	reactionIdx int // hovered reaction chip on this row, -1 for none
}

// msgRowCacheState memoizes rendered message rows (before zone.Mark), so a
// row the pointer leaves gets its earlier rendering back. It can't be keyed
// on a Message's content, so refreshViewport, which every content change
// goes through, clears it.
type msgRowCacheState struct {
	cw        int
	nameWidth int
	rows      map[msgRowKey]string
}

// msgRowCacheLimit caps rows kept between refreshes; past it the cache is
// cleared.
const msgRowCacheLimit = 2048

func newMsgRowCacheState() *msgRowCacheState {
	return &msgRowCacheState{rows: make(map[msgRowKey]string)}
}

// rowCache returns the row cache, creating it for a Model built as a
// literal. Value-receiver renderers treat nil as uncached.
func (m *Model) rowCache() *msgRowCacheState {
	if m.msgRowCache == nil {
		m.msgRowCache = newMsgRowCacheState()
	}
	return m.msgRowCache
}

func (m Model) msgRowCacheKey(msgs []Message, idx int) msgRowKey {
	k := msgRowKey{
		idx:         idx,
		selected:    idx == m.selectedMsg,
		rowHovered:  m.isHovered(zoneMessage(idx)),
		replyKey:    m.isReplyKeyHovered(idx),
		reactKey:    m.isReactKeyHovered(idx),
		expandBtn:   m.isExpandButtonHovered(idx),
		flashed:     m.flashMsgIdx >= 0 && idx == m.flashMsgIdx,
		reactionIdx: -1,
	}
	if idx >= 0 && idx < len(msgs) {
		k.expanded = m.expandedMsgs[msgKey(msgs[idx], idx)]
		if m.hover != nil && m.hover.reactionMsgIdx == idx {
			k.reactionIdx = m.hover.reactionIdx
		}
	}
	return k
}

// renderMessageRow renders one message row padded to cw, reusing the
// previous rendering of that row in that same visual state when there is
// one. See msgRowCacheState.
func (m Model) renderMessageRow(msgs []Message, idx, cw, nameWidth int) string {
	render := func() string {
		return padLinesToWidth(m.renderMessage(msgs[idx], idx, cw, msgs, nameWidth), cw)
	}
	c := m.msgRowCache
	if c == nil {
		return render()
	}
	if c.cw != cw || c.nameWidth != nameWidth || len(c.rows) > msgRowCacheLimit {
		clear(c.rows)
		c.cw, c.nameWidth = cw, nameWidth
	}
	key := m.msgRowCacheKey(msgs, idx)
	if row, ok := c.rows[key]; ok {
		return row
	}
	row := render()
	c.rows[key] = row
	return row
}

func (m Model) renderMessage(msg Message, msgIdx, totalWidth int, allMsgs []Message, nameWidth int) string {
	divider := m.messageDateDivider(msgIdx, allMsgs)
	if divider != "" {
		// Starts where message text does: gutter, then name column.
		divider = strings.Repeat(" ", 2+nameWidth+1) + divider + "\n"
	}

	if msg.CallLog != nil {
		return divider + m.callLogLine(msg, msgIdx, totalWidth)
	}
	isSelected := msgIdx == m.selectedMsg
	rowHovered := m.isHovered(zoneMessage(msgIdx))

	// The 2-cell gutter (selection bar) is added below.
	totalWidth -= 2

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
	replyIdx := messageIndexByID(allMsgs, msg.ReplyToID)
	hasReply := replyIdx >= 0
	switch {
	case replyIdx >= 0:
		reply := m.replyHeaderFragment(replyIdx, allMsgs, totalWidth-prefixWidth)
		headerLine += m.zone.Mark(zoneMessageReply(msgIdx), reply)
	case msg.ReplyToID != "" && msg.QuotedPreview != "":
		// Target not loaded: the stored quote, not clickable.
		hasReply = true
		headerLine += m.replyHeaderFragmentFallback(msg.QuotedAuthor, msg.QuotedPreview, totalWidth-prefixWidth)
	}

	var bodyContent string
	switch {
	case msg.Retracted:
		// The chat view never shows retracted content — only that a delete
		// happened. Original content/attachments remain available in the
		// info popup (ctrl+i); we never actually erase local history.
		bodyContent = "*deleted*"
	case len(msg.Attachments) > 0:
		// Attachments (XEP-0066) decide; their URLs are stripped from the
		// text only if it ends with them.
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
		// A real XEP-0461 reply (Message.ReplyToID) always wins over the
		// text convention below - a message can't be both.
		if !hasReply {
			if preview, rest, ok := parseQuoteReply(content); ok {
				hasTextQuoteReply = true
				content = rest
				headerLine += m.styles.renderQuoteReplyFragment(preview, totalWidth-prefixWidth)
			}
		}
		bodyContent = FormatMessageBody(content)
	}

	// The body follows the name, unless a quote takes that place.
	bodyOnHeaderLine := !hasReply && !hasTextQuoteReply
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
		lines = append(lines, pad+m.zone.Mark(zoneMessageExpand(msgIdx), m.styles.renderMsgExpandButton(expanded, expandHovered, caretKey(shortestKey(m.keys.ExpandMsg)))))
	}

	lines = append(lines, pad+m.renderMessageStatusLine(msg, msgIdx, rowHovered))

	// The selection bar sits at the left edge; other rows get blanks.
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

	// Selected or hovered, the whole row is tinted, padded to full width.
	if isSelected || rowHovered {
		for i, line := range lines {
			lines[i] = m.styles.rowBackgroundTint(padLinesToWidth(line, totalWidth+2))
		}
	}

	if m.flashMsgIdx >= 0 && msgIdx == m.flashMsgIdx {
		// Styling stripped first: the inner styling's resets would end the
		// flash background.
		for i, line := range lines {
			lines[i] = m.styles.messageFlash.Render(ansi.Strip(line))
		}
	}

	return divider + strings.Join(lines, "\n")
}

// renderMessageStatusLine renders the line under a message: time, edited
// marker, our messages' send status, the reply/react buttons while hovered,
// and reactions.
func (m Model) renderMessageStatusLine(msg Message, msgIdx int, rowHovered bool) string {
	timeLabel := m.styles.messageTime.Render(m.formatMessageTime(msg.SentAt))
	parts := []string{timeLabel}

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
			// Unconfirmed: no glyph (see Message.ServerAcked).
		}
		if status != "" {
			badges = append(badges, m.styles.messageTime.Render(status))
		}
	}

	if len(badges) > 0 {
		parts = append(parts, strings.Join(badges, " "))
	}

	if rowHovered {
		replyKey := m.zone.Mark(zoneMessageReplyKey(msgIdx), m.styles.renderMsgActionKey("↩", m.isReplyKeyHovered(msgIdx)))
		reactKey := m.zone.Mark(zoneMessageReactKey(msgIdx), m.styles.renderMsgActionKey("+", m.isReactKeyHovered(msgIdx)))
		replyBtn := m.zone.Mark(zoneMessageReplyBtn(msgIdx), replyKey)
		reactBtn := m.zone.Mark(zoneMessageReactBtn(msgIdx), reactKey)
		// Tighter than the double space separating time/badges/buttons from
		// each other - these two read as one button group.
		parts = append(parts, replyBtn+" "+reactBtn)
	}

	if len(msg.Reactions) > 0 {
		parts = append(parts, m.renderClickableReactions(msg.Reactions, msgIdx))
	}

	return strings.Join(parts, "  ")
}

// renderClickableReactions renders reactions ("😂 2 👍"), each clickable to
// toggle our own (toggleMyReaction), ours bold.
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

// toggleMyReaction is our reactions with emoji toggled.
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

// replyHeaderFragment renders a reply's "↑ name │ preview" in budget cells.
func (m Model) replyHeaderFragment(idx int, allMsgs []Message, budget int) string {
	if idx < 0 || idx >= len(allMsgs) {
		return ""
	}
	orig := allMsgs[idx]
	nameStyle := m.styles.messageNickThem
	if orig.IsMe {
		nameStyle = m.styles.messageNickMe
	}
	displayName := senderDisplayName(orig.Author)
	name := nameStyle.Render(displayName)
	sep := m.styles.messageTime.Render("│")
	// "↑ " + name + " " + "│" + " "
	fixed := 2 + lipgloss.Width(displayName) + 3
	preview := m.styles.messageTime.Render(previewText(MessagePreviewContent(orig), min(previewLen, budget-fixed)))
	return "↑ " + name + " " + sep + " " + preview
}

// replyHeaderFragmentFallback is replyHeaderFragment from the stored quote,
// for a target not loaded; the name in the "them" color.
func (m Model) replyHeaderFragmentFallback(author, preview string, budget int) string {
	nameStyle := m.styles.messageNickThem
	if author == "me" {
		nameStyle = m.styles.messageNickMe
	}
	displayName := senderDisplayName(author)
	name := nameStyle.Render(displayName)
	sep := m.styles.messageTime.Render("│")
	fixed := 2 + lipgloss.Width(displayName) + 3
	previewRendered := m.styles.messageTime.Render(previewText(preview, min(previewLen, budget-fixed)))
	return "↑ " + name + " " + sep + " " + previewRendered
}

// quoteLinePrefix matches a "> "-quoted line, nested or not.
var quoteLinePrefix = regexp.MustCompile(`^\s*>+\s?`)

// parseQuoteReply splits content starting with quoted lines into a reply
// preview (the last quoted line) and the rest - a text convention, not a
// XEP-0461 reply. ok is false without quote, or without text after it.
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

// retractedPreview is what every single-line preview of a retracted message
// shows in place of its content.
const retractedPreview = "message deleted"

// MessagePreviewContent is msg as one line of preview. Attachments show as
// file names, not URLs: an aesgcm:// one holds the file's key.
func MessagePreviewContent(msg Message) string {
	// Never what was deleted.
	if msg.Retracted {
		return retractedPreview
	}
	return messagePreviewBody(msg)
}

// messagePreviewBody is MessagePreviewContent even for a retracted message,
// for the purge confirmation to name it.
func messagePreviewBody(msg Message) string {
	if msg.CallLog != nil {
		return callLogText(*msg.CallLog)
	}
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

// previewText is s on one line, in at most n cells, ellipsis included.
func previewText(s string, n int) string {
	flat := strings.ReplaceAll(s, "\n", " ")
	if n <= 0 {
		return ""
	}
	return ansi.Truncate(flat, n, "…")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Local().Date()
	by, bm, bd := b.Local().Date()
	return ay == by && am == bm && ad == bd
}
