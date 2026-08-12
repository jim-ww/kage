package ui

import (
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// pasteAsFileThreshold is the length above which a paste into the compose
// box is treated as file content rather than a chat message: long pastes
// (logs, code, config dumps) are unreadable and unwieldy inline, so they're
// staged as an attachment instead of dumped into the textinput.
const pasteAsFileThreshold = 1000

// pasteLooksBinary reports whether pasted content is raw binary data rather
// than text — e.g. a "copy image" from a browser landing in the terminal's
// paste buffer as literal image bytes instead of a file path. Regardless of
// length, that's never useful typed into the compose box as text.
func pasteLooksBinary(content string) bool {
	if !utf8.ValidString(content) {
		return true
	}
	return strings.ContainsRune(content, 0)
}

// pasteFileExtension sniffs content to pick a file extension for a pasted
// binary blob, falling back to .bin when the type can't be identified (or
// isn't registered with a known extension) so the file still opens somehow.
func pasteFileExtension(content string) string {
	mimeType := http.DetectContentType([]byte(content))
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

// writePasteAsFile saves pasted content to a temp file so it can be staged
// as an attachment the same way a picked/dropped file is. The name is
// timestamped so repeated large pastes in one session don't collide; binary
// pastes (e.g. a copied image) get a sniffed extension instead of .txt so
// the resulting attachment opens in the right viewer.
func writePasteAsFile(content string) (string, error) {
	ext := ".txt"
	if pasteLooksBinary(content) {
		ext = pasteFileExtension(content)
	}
	path := filepath.Join(os.TempDir(), "kage-paste-"+time.Now().Format("20060102-150405.000")+ext)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// droppedFilePaths recognizes bracketed-paste content that is actually one
// or more dragged-and-dropped files rather than typed/pasted text: terminals
// deliver a drop as the file's absolute path (some as a file:// URI); a
// multi-file drop is delivered either as one path per line, or as several
// paths space-separated (quoted individually if they contain spaces) on a
// single line, depending on the terminal. Content that doesn't fully resolve
// to existing regular files is left alone so it reaches the compose input as
// normal paste text.
func droppedFilePaths(content string) ([]string, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false
	}
	if !strings.ContainsAny(content, "\n\r") {
		if path, ok := resolveDroppedPath(content); ok {
			return []string{path}, true
		}
		return resolveDroppedPaths(splitQuotedTokens(content))
	}
	return resolveDroppedPaths(strings.Split(content, "\n"))
}

// resolveDroppedPaths resolves every token to an existing regular file,
// failing the whole batch (rather than a partial one) if any token doesn't
// resolve.
func resolveDroppedPaths(tokens []string) ([]string, bool) {
	paths := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(strings.TrimRight(t, "\r"))
		if t == "" {
			continue
		}
		path, ok := resolveDroppedPath(t)
		if !ok {
			return nil, false
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, false
	}
	return paths, true
}

// resolveDroppedPath resolves a single bracketed-paste token to an existing
// regular file's path, or reports false if it isn't one.
func resolveDroppedPath(content string) (string, bool) {
	path := content
	if u, err := url.Parse(content); err == nil && u.Scheme == "file" && u.Path != "" {
		path = u.Path
	}
	path = strings.Trim(path, `"'`)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

// splitQuotedTokens splits a single-line paste on whitespace, treating a
// '"'- or '\''-quoted run as one token — the shape some terminals use to
// deliver several dropped file paths (individually quoted if they contain
// spaces) on one line.
func splitQuotedTokens(s string) []string {
	var tokens []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ' ':
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// isActivityMsg reports whether msg represents real keyboard/mouse input,
// as opposed to a tick, an IPC event, or anything else the program can
// receive with nobody actually at the keyboard — see Model.idle.
func isActivityMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyMsg, tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return true
	default:
		return false
	}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (retModel tea.Model, retCmd tea.Cmd) {
	if im, ok := msg.(idleMsg); ok {
		if im.gen == m.idleGen {
			m.idle = true
		}
	} else if isActivityMsg(msg) {
		m.idle = false
		m.idleGen++
		gen := m.idleGen
		defer func() {
			retCmd = tea.Batch(retCmd, idleTimer(gen))
		}()
	}

	if m.focusReporter != nil {
		oldFocused := m.focused && !m.idle
		oldAccountJID, oldChatAddress := m.activeChatKey()
		defer func() {
			rm, ok := retModel.(Model)
			if !ok {
				return
			}
			newFocused := rm.focused && !rm.idle
			newAccountJID, newChatAddress := rm.activeChatKey()
			if newFocused == oldFocused && newAccountJID == oldAccountJID && newChatAddress == oldChatAddress {
				return
			}
			reporter := rm.focusReporter
			retCmd = tea.Batch(retCmd, func() tea.Msg {
				_ = reporter.SetFocusState(newAccountJID, newChatAddress, newFocused)
				return nil
			})
		}()
	}

	if model, cmd, handled := m.handleEventMsg(msg); handled {
		return model, cmd
	}

	var cmds []tea.Cmd

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if model, cmd, handled := m.updateKeyMsg(keyMsg); handled {
			return model, cmd
		}
	}

	// Route remaining events to the focused component. Checked before the
	// m.selectedView switch below since the add-account form floats on top
	// of viewAccounts: non-key messages (bracketed-paste text, the
	// textinput cursor-blink tick) aren't caught by the key interception
	// above (that only matches tea.KeyMsg), so without this they'd fall
	// through to the "Account focus is handled by global keys only" case
	// and silently vanish — e.g. paste never reaching the focused field.
	if m.addingAccount {
		var cmd tea.Cmd
		m.addAccountInputs[m.addAccountFocus], cmd = m.addAccountInputs[m.addAccountFocus].Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
	}
	if m.renamingChat {
		var cmd tea.Cmd
		*m.renameInput, cmd = m.renameInput.Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
	}
	if m.searchingChat {
		var cmd tea.Cmd
		*m.searchInput, cmd = m.searchInput.Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
	}
	if m.searchResults != nil && m.searchResults.filtering {
		var cmd tea.Cmd
		m.searchResults.filterInput, cmd = m.searchResults.filterInput.Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
	}
	if m.savingAs {
		var cmd tea.Cmd
		*m.saveAsInput, cmd = m.saveAsInput.Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
	}
	if m.pickingFile {
		// Directory reads are asynchronous messages, not key presses, so they
		// bypass the key-interception block above and must still reach the
		// picker. Without this, the picker remains permanently empty.
		var cmd tea.Cmd
		*m.filePicker, cmd = m.filePicker.Update(msg)
		return m, tea.Batch(append(cmds, cmd)...)
	}

	// Route remaining events to the focused component.
	var cmd tea.Cmd
	switch m.selectedView {
	case viewAccounts:
		// Account focus is handled by global keys only.
	case viewChats:
		prev := m.chats.Index()
		*m.chats, cmd = m.chats.Update(msg)
		cmds = append(cmds, cmd)
		if m.chats.Index() != prev {
			m.chatSwitchGen++
			cmds = append(cmds, chatSwitchTimer(m.chatSwitchGen))
		}
	case viewChat:
		// Dragging a file onto most terminal emulators isn't a distinct
		// bubbletea event — the terminal just bracketed-pastes the dropped
		// file's absolute path(s) as text. Treat a paste that resolves
		// entirely to existing files, rather than typed/pasted prose, as a
		// drop and stage them as pending attachments instead of inserting
		// the path into the compose box.
		if pasteMsg, ok := msg.(tea.PasteMsg); ok {
			if paths, ok := droppedFilePaths(pasteMsg.Content); ok {
				for _, path := range paths {
					m.stageAttachment(path)
				}
				return m, nil
			}
			// A long paste (log dump, code, config) or a binary one (e.g.
			// an image copied in a browser landing here as raw bytes) is
			// unreadable and unwieldy inline — stage it as an attachment
			// instead of inserting it into the compose textinput.
			if pasteLooksBinary(pasteMsg.Content) || len(pasteMsg.Content) > pasteAsFileThreshold {
				if path, err := writePasteAsFile(pasteMsg.Content); err == nil {
					m.stageAttachment(path)
					return m, nil
				}
			}
		}

		oldValue := m.input.Value()
		wasMultiline := m.composeMultiline()
		beforeLine, beforeCol := m.input.Line(), m.input.Column()
		isDownKey := false
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			isDownKey = keyMsg.String() == "down"
		}
		*m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		if wasMultiline && isDownKey && m.input.Value() == oldValue &&
			m.input.Line() == beforeLine && m.input.Column() == beforeCol {
			m.fixStuckComposeCursorDown()
		}
		if m.input.Value() != oldValue {
			// The textarea grows/shrinks with content (DynamicHeight), which
			// shifts inputAreaHeight() — the viewport must be resized in
			// lockstep or it'll under/overlap the compose box by however
			// many rows just changed.
			m.updateSizes()
		}
		// This branch runs for every message type routed here, including
		// the textinput cursor-blink tick — not just keystrokes. Recomputing
		// (and resetting the highlighted suggestion) on every blink would
		// undo arrow-key navigation a few hundred ms after every move, so
		// only do it when the text itself actually changed.
		if m.reactingMsgIdx >= 0 && m.input.Value() != oldValue {
			if token, _, ok := currentWordToken(m.input.Value()); ok {
				m.setEmojiSuggestions(emojiSuggestionsFor(token))
			} else {
				m.setEmojiSuggestions(nil)
			}
		}
		if m.input.Value() != oldValue {
			m.pushDraftSnapshot(m.input.Value())
			// Editing/reacting content isn't a new-message draft — don't let
			// the debounce timer overwrite the chat's real stored Draft with
			// it (see stashDraftForCompose/restoreStashedDraft instead).
			if m.editingMsgIdx < 0 && m.reactingMsgIdx < 0 {
				if chat, ok := m.currentChat(); ok && chat.Address != "" {
					m.draftSaveGen++
					cmds = append(cmds, draftSaveTimer(m.currentAccount, chat.Address, m.draftSaveGen))
				}
			}
		}
		cmds = append(cmds, m.notifyTypingChanged(oldValue))
	}

	return m, tea.Batch(cmds...)
}

// notifyTypingChanged reacts to a keystroke in the compose input by sending
// a XEP-0085 chat-state update when needed, and returns a cmd that arms (or
// re-arms) the pause timeout — see typingPauseTimer. Skipped while composing
// a reaction (m.reactingMsgIdx >= 0): that's not really "typing a message".
//
//   - Input went from empty to non-empty, or we'd previously paused/stopped
//     (m.typingActiveTo isn't this chat): send "composing" and start the
//     pause timer.
//   - Input is still non-empty and we're already marked composing to this
//     chat: no new stanza (a held-down key shouldn't resend "composing"
//     every tick) — just re-arm the pause timer so it doesn't fire early.
//   - Input went empty: send "stopped" and don't arm a timer.
func (m *Model) notifyTypingChanged(oldValue string) tea.Cmd {
	if m.sender == nil || m.reactingMsgIdx >= 0 {
		return nil
	}
	chat, ok := m.currentChat()
	if !ok || chat.Address == "" {
		return nil
	}
	newValue := m.input.Value()
	if newValue == oldValue {
		return nil
	}

	if newValue == "" {
		if m.typingActiveTo == chat.Address {
			if err := m.sender.SetTyping(m.currentAccount, chat.Address, false); err == nil {
				m.typingActiveTo = ""
			}
		}
		return nil
	}

	if m.typingActiveTo != chat.Address {
		if err := m.sender.SetTyping(m.currentAccount, chat.Address, true); err != nil {
			return nil
		}
		m.typingActiveTo = chat.Address
	}
	m.typingGen++
	return typingPauseTimer(chat.Address, m.typingGen)
}

// notifyTypingStopped tells the peer we've stopped composing, if we'd told
// them otherwise — called wherever the input is cleared programmatically
// (message actually sent, reaction sent) or the user navigates away from the
// chat, none of which go through notifyTypingChanged's normal keystroke path.
func (m *Model) notifyTypingStopped() {
	if m.sender == nil || m.typingActiveTo == "" {
		return
	}
	if err := m.sender.SetTyping(m.currentAccount, m.typingActiveTo, false); err == nil {
		m.typingActiveTo = ""
	}
}

func isViewportPagingKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "pgup", "pgdown", "pageup", "pagedown":
		return true
	default:
		return false
	}
}

// digitKey reports whether msg is a single '1'-'9' keypress, and which digit.
func digitKey(msg tea.KeyMsg) (int, bool) {
	s := msg.String()
	if len(s) != 1 || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	return int(s[0] - '0'), true
}
