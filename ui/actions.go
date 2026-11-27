package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/jim-ww/kage/ui/modal"
)

func (m *Model) switchAccount(index int) tea.Cmd {
	if index < 0 || index >= len(m.accounts) || index == m.currentAccount || m.accounts[index].Removed {
		return nil
	}
	m.currentAccount = index
	m.forgetHistoryLoads()
	m.cancelPending()
	m.lastClickedMsgIdx = -1
	m.lastClickTime = time.Time{}
	m.chats.Select(0)
	m.selectedMsg = 0
	cmd := m.chats.SetItems(m.accounts[index].Chats)
	if msgs := m.currentMessages(); len(msgs) > 0 {
		m.selectedMsg = len(msgs) - 1
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	return cmd
}

// scrolledPastFirstPage reports whether the chat viewport has scrolled up
// more than one full screen's worth of content away from the bottom — the
// "jump to latest" button's show/hide condition. A plain !AtBottom() would
// show it the instant the bottom line scrolls out of view even by one row,
// which is still the first screenful the user would see if they scrolled
// the rest of the way down themselves; the button is for the case where
// they've paged/navigated far enough that "latest" is no longer one
// scroll away.
//
// If the currently loaded window isn't the live tail (HistoryNewer set —
// paged up far enough to fall off the tail window, or landed on a search
// result), "distance from the bottom of this window" is meaningless as a
// proxy for "distance from the actual latest message": the window can be
// short (little history left in that direction, or a narrow search-result
// window) and scrolling around inside it would cross the local YOffset/
// TotalLineCount threshold back and forth, flickering the button even
// though the real latest message is nowhere close. In that case the button
// always shows — jumpToLatestMessage does a real fetch back to the tail
// regardless of where in this window the viewport happens to sit.
func (m Model) scrolledPastFirstPage() bool {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return false
	}
	if m.accounts[m.currentAccount].HistoryNewer[chatIdx] {
		return true
	}
	height := m.viewport.Height()
	if height <= 0 {
		return false
	}
	return m.viewport.YOffset() < m.viewport.TotalLineCount()-2*height
}

// jumpToLatestMessage scrolls the chat viewport to the bottom and moves the
// selection to the newest loaded message — used by the floating "jump to
// latest" button shown whenever the viewport has scrolled away from the
// bottom (via message navigation, paging, or landing on a search result).
// If the currently loaded window isn't the live tail (HistoryNewer set —
// paged up, or landed on a search result), that's a real fetch anchored on
// nil (see HistoryAnchor's doc comment: nil means "the true latest window"),
// same as opening the chat fresh. Otherwise it's already showing the tail,
// so this is just a local selection/scroll move — no round trip needed.
func (m *Model) jumpToLatestMessage() tea.Cmd {
	chatIdx := m.currentChatIndex()
	if chatIdx < 0 || m.currentAccount < 0 || m.currentAccount >= len(m.accounts) {
		return nil
	}
	if m.accounts[m.currentAccount].HistoryNewer[chatIdx] && !m.loadingHistoryWindow[chatIdx] {
		if chat, ok := m.currentChat(); ok {
			if cmd := m.daemon.LoadHistoryWindow(m.currentAccount, chat.Address, nil); cmd != nil {
				m.markHistoryWindowLoading(chatIdx, "")
				return cmd
			}
		}
	}

	msgs := m.currentMessages()
	if len(msgs) == 0 {
		m.viewport.GotoBottom()
		return nil
	}
	old := m.selectedMsg
	m.selectedMsg = len(msgs) - 1
	m.refreshViewportSelection(old, m.selectedMsg)
	m.viewport.GotoBottom()
	return nil
}

func (m *Model) actionMakeDefaultAccount(index int) tea.Cmd {
	if index < 0 || index >= len(m.accounts) {
		return nil
	}
	if err := m.daemon.SaveState(DefaultAccount(m.accounts[index].Name)); err != nil {
		return m.showNotification(fmt.Sprintf("setting default account: %v", err))
	}
	return m.showNotification("Default account set")
}

// encryptionModeOrDefault returns mode, or m.defaultEncryptionMode
// (config's default_encryption_mode, "omemo-v1" if unset) if mode is unset.
func (m *Model) encryptionModeOrDefault(mode string) string {
	if mode == "" {
		if m.defaultEncryptionMode == "" {
			return "omemo-v1"
		}
		return m.defaultEncryptionMode
	}
	return mode
}

// encryptionModes lists every selectable outgoing-encryption mode, in the
// order they're offered in the encryption picker. "omemo-v1"/"omemo-v2"
// force a specific OMEMO protocol version for this chat.
var encryptionModes = []string{"omemo-v1", "omemo-v2", "gpg", "none"}

// actionOpenEncryptionMenu opens a picker submenu (chat-item context menu's
// "Encryption") listing every mode with the current one marked, so picking
// one sets it directly instead of cycling through them one keypress at a
// time.
func (m *Model) actionOpenEncryptionMenu() tea.Cmd {
	idx := m.currentChatIndex()
	items := m.chats.Items()
	if idx < 0 || idx >= len(items) {
		return m.showNotification("no chat selected")
	}
	chat, ok := items[idx].(Chat)
	if !ok {
		return nil
	}
	modes := encryptionModes
	switch {
	case chat.PrivateRoom != "":
		return m.showNotification("private messages in a room go through it, unencrypted")
	case chat.Room && !chat.Private:
		return m.showNotification("only a private room (members-only, showing members' addresses) can be encrypted")
	case chat.Room:
		// What groups use everywhere.
		modes = []string{"omemo-v1", "none"}
	}

	current := m.encryptionModeOrDefault(chat.EncryptionMode)
	menuItems := make([]contextMenuItem, 0, len(modes))
	for _, mode := range modes {
		if mode == "gpg" && !m.useGPG {
			continue
		}
		label := mode
		if mode == current {
			label = "✓ " + mode
		}
		menuItems = append(menuItems, contextMenuItem{
			label: label,
			run:   func(m *Model) tea.Cmd { return m.actionSetChatEncryption(mode) },
		})
	}
	m.openContextMenu("Encryption", menuItems)
	return nil
}

// actionSetChatEncryption sets the selected chat's outgoing message
// encryption to mode directly (the encryption picker's per-mode entries).
func (m *Model) actionSetChatEncryption(mode string) tea.Cmd {
	idx := m.currentChatIndex()
	items := m.chats.Items()
	if idx < 0 || idx >= len(items) {
		return m.showNotification("no chat selected")
	}
	chat, ok := items[idx].(Chat)
	if !ok {
		return nil
	}

	if err := m.daemon.SetChatEncryption(m.currentAccount, chat.Address, mode); err != nil {
		return m.showNotification("setting encryption: " + err.Error())
	}
	chat.EncryptionMode = mode
	m.accounts[m.currentAccount].Chats[idx] = chat
	cmd := m.chats.SetItem(idx, chat)
	return tea.Batch(cmd, m.showNotification("Encryption: "+mode))
}

func (m *Model) showNotification(text string) tea.Cmd {
	m.noticeID++
	m.noticeText = text
	id := m.noticeID
	return tea.Tick(m.noticeDuration, func(time.Time) tea.Msg {
		return noticeClearMsg{id: id}
	})
}

// cancelPending clears any in-progress edit, reply, reaction, or emoji-picker
// composition.
func (m *Model) cancelPending() {
	wasComposing := !m.editingMsg.empty()
	m.editingMsg = msgRef{}
	m.replyTo = msgRef{}
	m.lastClickedMsgIdx = -1
	if wasComposing {
		m.restoreStashedDraft()
	}
	m.input.Placeholder = "message..."
	m.updateSizes()
}

// actionChatInfo shows what's known about the current chat's contact; its
// address line copies the address.
func (m *Model) actionChatInfo() tea.Cmd {
	chat, ok := m.currentChat()
	if !ok {
		return m.showNotification("no chat selected")
	}
	lines := m.chatInfoLines(chat)
	switch {
	case chat.Room:
		lines = roomInfoLines(chat)
	case chat.PrivateRoom != "":
		lines = m.privateInfoLines(chat)
	}
	text := modal.NewText(lines...)
	text.ActLabel = "copy address"
	title := chat.Name
	if title == "" {
		title = chat.Address
	}
	return m.openModal(&modal.Modal[*Model]{
		Title: title,
		Body:  &chatInfoBody{Text: text, address: chat.Address},
		OnAct: func(m *Model, v any) tea.Cmd {
			if err := clipboard.WriteAll(v.(string)); err != nil {
				return m.showNotification("copy failed")
			}
			return m.showNotification("address copied")
		},
	})
}

// chatInfoLines is the contact info screen's content for chat.
func (m Model) chatInfoLines(chat Chat) []modal.Line {
	// The screen's title is the chat's name.
	lines := []modal.Line{{Text: "Address: " + chat.Address, Value: chat.Address}}
	lines = append(lines, modal.Line{Text: "Presence: " + presenceGlyph(chat.Presence) + " " + presenceLabel(chat.Presence)})
	for _, r := range chat.Resources {
		name := r.Name
		if name == "" {
			name = resourceDisplayName(r.Resource)
		}
		lines = append(lines, modal.Line{Text: fmt.Sprintf("  %s %s (%s)", deviceGlyph(r.Presence), name, r.Resource)})
	}
	subscription := subscriptionLabel(chat)
	if chat.Subscription == "both" && !chat.SubscriptionPending {
		subscription = "both ways"
	}
	if subscription == "" {
		subscription = "unknown (not in roster)"
	}
	lines = append(lines, modal.Line{Text: "Subscription: " + subscription})
	mode := m.encryptionModeOrDefault(chat.EncryptionMode)
	if chat.EncryptionMode == "" {
		mode += " (default)"
	}
	lines = append(lines, modal.Line{Text: "Encryption: " + mode})
	notifications := "on"
	if chat.Muted {
		notifications = "muted"
	}
	lines = append(lines, modal.Line{Text: "Notifications: " + notifications})
	if chat.Pinned {
		lines = append(lines, modal.Line{Text: "Pinned: yes"})
	}
	if !chat.LastActivity.IsZero() {
		lines = append(lines, modal.Line{Text: "Last message: " + chat.LastActivity.Local().Format("2006-01-02 15:04")})
	}
	return lines
}

// chatInfoBody is the contact info screen: the contact's avatar, when there
// is one, left of the info lines.
type chatInfoBody struct {
	*modal.Text
	address string
	height  int // rows the screen has for it, 0 until sized

	// picture is the avatar rendered at rows, for avatar generation gen.
	picture string
	rows    int
	gen     uint64
}

// chatInfoAvatarGap separates the avatar from the info lines.
const chatInfoAvatarGap = 2

// avatarRows is the avatar's height: as tall as the info lines, within
// bounds. 0 without an avatar.
func (b *chatInfoBody) avatarRows() int {
	if !avatarsEnabled() || !hasAvatarPicture(b.address) {
		return 0
	}
	rows := min(8, max(4, len(b.Lines)))
	if b.height > 0 {
		rows = min(rows, b.height)
	}
	return rows
}

// avatar renders the picture once per size and avatar change: the shared
// render cache holds one picture, the sidebar panel's.
func (b *chatInfoBody) avatar() string {
	rows := b.avatarRows()
	if rows == 0 {
		return ""
	}
	if gen := avatarGeneration(); b.picture == "" || b.rows != rows || b.gen != gen {
		img, _ := avatarPicture(b.address)
		b.picture, b.rows, b.gen = renderImageBlocks(img, 2*rows, rows), rows, gen
	}
	return b.picture
}

func (b *chatInfoBody) View() string {
	pic := b.avatar()
	if pic == "" {
		return b.Text.View()
	}
	return joinHorizontal(pic, strings.Repeat(" ", chatInfoAvatarGap), b.Text.View())
}

func (b *chatInfoBody) SetSize(width, height int) {
	b.height = height
	if rows := b.avatarRows(); rows > 0 {
		width = max(1, width-2*rows-chatInfoAvatarGap)
	}
	b.Text.SetSize(width, height)
}

func (b *chatInfoBody) PreferredWidth() int {
	w := b.Text.PreferredWidth()
	if rows := b.avatarRows(); rows > 0 {
		w += 2*rows + chatInfoAvatarGap
	}
	return w
}
