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

// emojiSynonyms maps common informal words/phrases to shortcodes for emoji
// they don't literally name — e.g. "idk" has nothing to do with the string
// "shrug", but it's exactly what a shrug emoji means. gemoji ships this kind
// of "tags" data upstream but enescakir/emoji doesn't expose it, so this is
// a small hand-curated table covering the common cases; extend freely.
// Filtered at init to whatever shortcodes actually exist in emojiShortcodes,
// so a typo or renamed alias here just drops quietly instead of panicking.
var emojiSynonyms = func() map[string][]string {
	raw := map[string][]string{
		"idk":        {":shrug:"},
		"dunno":      {":shrug:"},
		"unsure":     {":shrug:"},
		"whatever":   {":shrug:"},
		"like":       {":+1:"},
		"agree":      {":+1:"},
		"dislike":    {":-1:"},
		"disagree":   {":-1:"},
		"lol":        {":joy:", ":laughing:"},
		"lmao":       {":rolling_on_the_floor_laughing:", ":joy:"},
		"rofl":       {":rolling_on_the_floor_laughing:"},
		"funny":      {":joy:"},
		"love":       {":heart:"},
		"sad":        {":cry:", ":disappointed:"},
		"upset":      {":disappointed:"},
		"crying":     {":sob:"},
		"angry":      {":rage:"},
		"mad":        {":rage:", ":angry:"},
		"congrats":   {":tada:", ":clap:"},
		"thanks":     {":pray:", ":raised_hands:"},
		"thankyou":   {":pray:"},
		"cool":       {":sunglasses:"},
		"awesome":    {":fire:"},
		"great":      {":fire:"},
		"wow":        {":open_mouth:", ":astonished:"},
		"surprised":  {":astonished:"},
		"think":      {":thinking:"},
		"hmm":        {":thinking:"},
		"watching":   {":eyes:"},
		"suspicious": {":eyes:"},
		"fire":       {":fire:"},
		"lit":        {":fire:"},
		"party":      {":tada:"},
		"celebrate":  {":tada:"},
		"ok":         {":ok_hand:"},
		"okay":       {":ok_hand:"},
		"yes":        {":white_check_mark:", ":+1:"},
		"no":         {":x:", ":-1:"},
		"question":   {":question:"},
		"perfect":    {":100:"},
		"clap":       {":clap:"},
		"applause":   {":clap:"},
		"please":     {":pray:"},
		"facepalm":   {":man_facepalming:", ":woman_facepalming:"},
		"oof":        {":grimacing:"},
		"dead":       {":skull:"},
		"dying":      {":skull:"},
		"confused":   {":confused:"},
		"tired":      {":tired_face:"},
		"sleepy":     {":sleepy:"},
		"sick":       {":face_with_thermometer:"},
		"money":      {":moneybag:"},
		"rich":       {":moneybag:"},
		"scared":     {":scream:"},
		"fear":       {":fearful:"},
		"kiss":       {":kiss:"},
		"wave":       {":wave:"},
		"hi":         {":wave:"},
		"hello":      {":wave:"},
		"bye":        {":wave:"},
		"brain":      {":brain:"},
		"smart":      {":brain:"},
		"mindblown":  {":exploding_head:"},
	}
	known := make(map[string]bool, len(emojiShortcodes))
	for _, c := range emojiShortcodes {
		known[c] = true
	}
	out := make(map[string][]string, len(raw))
	for word, codes := range raw {
		var kept []string
		for _, c := range codes {
			if known[c] {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			out[word] = kept
		}
	}
	return out
}()

var emojiSynonymWords = func() []string {
	words := make([]string, 0, len(emojiSynonyms))
	for w := range emojiSynonyms {
		words = append(words, w)
	}
	sort.Strings(words)
	return words
}()

// emojiSuggestion is one fuzzy-matched shortcode suggestion.
type emojiSuggestion struct {
	Shortcode string
	Emoji     string
}

const maxEmojiSuggestions = 6

// currentWordToken returns the word at the end of s that a suggestion popup
// should match against — the text since the last whitespace, whether or not
// it starts with ':'. ok is false if s is empty, too short, or the cursor
// position (end of s) is past a trailing space (not mid-word).
func currentWordToken(s string) (token string, start int, ok bool) {
	if s == "" || strings.HasSuffix(s, " ") || strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\t") {
		return "", 0, false
	}
	start = strings.LastIndexAny(s, " \n\t") + 1 // 0 if not found, correctly
	token = s[start:]
	if len(token) < 2 {
		return "", 0, false
	}
	return token, start, true
}

// emojiSuggestionsFor returns up to maxEmojiSuggestions matches for the word
// the user is currently typing — combining a direct fuzzy match against
// shortcode names with a fuzzy match against emojiSynonyms, so "idk" finds
// :shrug: even though the word "idk" never appears in the shortcode itself.
func emojiSuggestionsFor(word string) []emojiSuggestion {
	if len(word) < 2 {
		return nil
	}

	type scored struct {
		code  string
		score int
	}
	best := make(map[string]int)
	consider := func(code string, score int) {
		if s, ok := best[code]; !ok || score > s {
			best[code] = score
		}
	}

	for _, m := range fuzzy.Find(word, emojiShortcodes) {
		consider(m.Str, m.Score)
	}
	// Synonym matches are curated, deliberate associations ("idk" -> shrug)
	// rather than incidental character overlap, so they rank above whatever
	// noise plain fuzzy shortcode matching turns up.
	const synonymBonus = 1000
	for _, m := range fuzzy.Find(word, emojiSynonymWords) {
		for _, code := range emojiSynonyms[m.Str] {
			consider(code, m.Score+synonymBonus)
		}
	}

	ranked := make([]scored, 0, len(best))
	for code, score := range best {
		ranked = append(ranked, scored{code, score})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	n := min(len(ranked), maxEmojiSuggestions)
	out := make([]emojiSuggestion, n)
	for i := 0; i < n; i++ {
		out[i] = emojiSuggestion{Shortcode: ranked[i].code, Emoji: emoji.Parse(ranked[i].code)}
	}
	return out
}

// acceptEmojiSuggestion replaces the word currently being typed with the
// accepted shortcode, followed by a space so typing (or sending) can
// continue immediately.
func acceptEmojiSuggestion(input, shortcode string) string {
	_, start, ok := currentWordToken(input)
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
