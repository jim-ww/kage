package ui

import (
	"sort"
	"strings"

	"github.com/enescakir/emoji"
	"github.com/sahilm/fuzzy"
)

// emojiShortcodes is the full :shortcode: list, computed once — emoji.Map()
// is a static built-in table, no need to rebuild it on every keystroke.
var emojiShortcodes = func() []string {
	m := emoji.Map()
	codes := make([]string, 0, len(m))
	for code := range m {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}()

// emojiSuggestion is one fuzzy-matched shortcode suggestion.
type emojiSuggestion struct {
	Shortcode string
	Emoji     string
}

const maxEmojiSuggestions = 6

// currentEmojiToken returns the trailing `:partial` token at the end of s
// (if any) that a shortcode suggestion popup should match against, along
// with the offset it starts at. ok is false if the input isn't currently in
// the middle of typing a shortcode (no trailing unterminated ":...").
func currentEmojiToken(s string) (token string, start int, ok bool) {
	idx := strings.LastIndexByte(s, ':')
	if idx < 0 {
		return "", 0, false
	}
	token = s[idx:]
	if strings.ContainsAny(token, " \n\t") {
		return "", 0, false // whitespace after ':' means it's not an in-progress token
	}
	return token, idx, true
}

// emojiSuggestionsFor returns up to maxEmojiSuggestions shortcode matches for
// the partial token (including its leading ':') the user is currently typing.
func emojiSuggestionsFor(partial string) []emojiSuggestion {
	if len(partial) < 2 { // just ":" alone — too broad to be useful
		return nil
	}
	matches := fuzzy.Find(partial, emojiShortcodes)
	n := min(len(matches), maxEmojiSuggestions)
	out := make([]emojiSuggestion, n)
	for i := 0; i < n; i++ {
		code := matches[i].Str
		out[i] = emojiSuggestion{Shortcode: code, Emoji: emoji.Parse(code)}
	}
	return out
}

// acceptEmojiSuggestion replaces the trailing partial shortcode token in the
// input with the full accepted shortcode, followed by a space so typing (or
// sending) can continue immediately.
func acceptEmojiSuggestion(input, shortcode string) string {
	_, start, ok := currentEmojiToken(input)
	if !ok {
		return input + shortcode + " "
	}
	return input[:start] + shortcode + " "
}

// toEmojiSet parses reaction input text (raw emoji and/or :shortcode:s,
// whitespace-separated) into a de-duplicated slice of literal emoji.
func toEmojiSet(input string) []string {
	parsed := emoji.Parse(input)
	fields := strings.Fields(parsed)
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// mineEmojis returns the emoji our own account is currently reacting with.
func mineEmojis(reactions []Reaction) []string {
	var out []string
	for _, r := range reactions {
		if r.Mine {
			out = append(out, r.Emoji)
		}
	}
	return out
}

// toggleEmoji returns mine with target added if absent, or removed if present.
func toggleEmoji(mine []string, target string) []string {
	for i, e := range mine {
		if e == target {
			out := make([]string, 0, len(mine)-1)
			out = append(out, mine[:i]...)
			return append(out, mine[i+1:]...)
		}
	}
	return append(append([]string{}, mine...), target)
}

// setMyReactions recomputes a message's aggregate Reactions after our own
// contribution changes from oldMine to newMine, preserving every other
// reactor's counts — we only track aggregates here, not per-reactor
// breakdowns, so this reconciles the diff rather than rebuilding from
// scratch.
func setMyReactions(reactions []Reaction, newMine []string) []Reaction {
	newSet := make(map[string]bool, len(newMine))
	for _, e := range newMine {
		newSet[e] = true
	}

	result := make([]Reaction, 0, len(reactions)+len(newMine))
	seen := make(map[string]bool, len(reactions))
	for _, r := range reactions {
		seen[r.Emoji] = true
		isMine := newSet[r.Emoji]
		count := r.Count
		switch {
		case r.Mine && !isMine:
			count--
		case !r.Mine && isMine:
			count++
		}
		if count > 0 {
			result = append(result, Reaction{Emoji: r.Emoji, Count: count, Mine: isMine})
		}
	}
	for _, e := range newMine {
		if !seen[e] {
			result = append(result, Reaction{Emoji: e, Count: 1, Mine: true})
		}
	}
	return result
}
