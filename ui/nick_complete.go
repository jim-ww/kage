package ui

import (
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// nickCompletion is the tab completion of a nick in a room's compose box:
// tab again puts the next match in place of the one inserted.
type nickCompletion struct {
	chat     string
	matches  []string
	idx      int
	at       string // "@" when the word typed started with one
	atStart  bool   // the word opened the message: "nick: " is addressing them
	inserted string
}

// completeNick completes the word before the compose cursor to a nick in
// the current room, reporting whether it did.
func (m *Model) completeNick() bool {
	chat, ok := m.currentChat()
	if !ok || !chat.Room {
		return false
	}
	lines := strings.Split(m.input.Value(), "\n")
	if m.input.Line() >= len(lines) {
		return false
	}
	runes := []rune(lines[m.input.Line()])
	before := string(runes[:min(m.input.Column(), len(runes))])

	c := m.nickCompletion
	if c != nil && c.chat == chat.Address && c.inserted != "" && strings.HasSuffix(before, c.inserted) {
		m.deleteBeforeCursor(utf8.RuneCountInString(c.inserted))
		c.idx = (c.idx + 1) % len(c.matches)
	} else {
		start := strings.LastIndexAny(before, " \t") + 1
		word := before[start:]
		at := ""
		if strings.HasPrefix(word, "@") {
			at, word = "@", word[1:]
		}
		matches := m.nickMatches(chat, word)
		if word == "" || len(matches) == 0 {
			return false
		}
		m.deleteBeforeCursor(utf8.RuneCountInString(at + word))
		c = &nickCompletion{chat: chat.Address, matches: matches, at: at, atStart: start == 0 && m.input.Line() == 0}
	}
	suffix := " "
	if c.atStart && c.at == "" {
		suffix = ": "
	}
	c.inserted = termText(c.at + c.matches[c.idx] + suffix)
	m.input.InsertString(c.inserted)
	m.nickCompletion = c
	return true
}

// nickMatches is the nicks in chat's room starting with prefix, ours left
// out: who spoke last first, then the rest as listed.
func (m Model) nickMatches(chat Chat, prefix string) []string {
	lower := strings.ToLower(prefix)
	present := make(map[string]bool, len(chat.Occupants))
	for _, o := range chat.Occupants {
		if o.Nick != chat.Nick && strings.HasPrefix(strings.ToLower(o.Nick), lower) {
			present[o.Nick] = true
		}
	}
	var matches []string
	msgs := m.currentMessages()
	for i := len(msgs) - 1; i >= 0 && len(matches) < len(present); i-- {
		if a := msgs[i].Author; present[a] && !slices.Contains(matches, a) {
			matches = append(matches, a)
		}
	}
	for _, o := range chat.Occupants {
		if present[o.Nick] && !slices.Contains(matches, o.Nick) {
			matches = append(matches, o.Nick)
		}
	}
	return matches
}

// deleteBeforeCursor deletes the n characters before the compose cursor.
func (m *Model) deleteBeforeCursor(n int) {
	for range n {
		*m.input, _ = m.input.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
}
