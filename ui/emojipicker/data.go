package emojipicker

import (
	"sort"

	"github.com/enescakir/emoji"
)

// brokenTagSequenceFlags are the three "regional subdivision" flags gemoji
// ships (England, Scotland, Wales) - each is the black-flag base glyph
// followed by several Unicode tag characters (U+E00xx), a sequence most
// terminal fonts don't recognize as a single flag. Instead of rendering one
// flag glyph they show the black flag plus a handful of extra
// invisible-but-width-consuming characters, which throws off the picker
// grid's column alignment for the whole row they land in. Excluded from
// shortcodes entirely - a niche subdivision flag isn't worth a broken row.
var brokenTagSequenceFlags = map[string]bool{
	"\U0001f3f4\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f": true, // :england:/:flag_for_england:
	"\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f": true, // :scotland:/:flag_for_scotland:
	"\U0001f3f4\U000e0067\U000e0062\U000e0077\U000e006c\U000e0073\U000e007f": true, // :wales:/:flag_for_wales:
}

// shortcodes is the full :shortcode: list, computed once - emoji.Map() is a
// static built-in table, no need to rebuild it on every keystroke.
var shortcodes = func() []string {
	m := emoji.Map()
	codes := make([]string, 0, len(m))
	for code, glyph := range m {
		if brokenTagSequenceFlags[glyph] {
			continue
		}
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}()

// synonyms maps common informal words/phrases to shortcodes for emoji they
// don't literally name - e.g. "idk" has nothing to do with the string
// "shrug", but it's exactly what a shrug emoji means. gemoji ships this kind
// of "tags" data upstream but enescakir/emoji doesn't expose it, so this is
// a small hand-curated table covering the common cases; extend freely.
// Filtered at init to whatever shortcodes actually exist in shortcodes, so a
// typo or renamed alias here just drops quietly instead of panicking.
var synonyms = func() map[string][]string {
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
	known := make(map[string]bool, len(shortcodes))
	for _, c := range shortcodes {
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

var synonymWords = func() []string {
	words := make([]string, 0, len(synonyms))
	for w := range synonyms {
		words = append(words, w)
	}
	sort.Strings(words)
	return words
}()

// commonEmoji is a curated set of the reactions people generally reach for
// most, shown as the grid's default before anything's typed and before the
// caller has supplied enough of its own "recent" history to fill it.
var commonEmoji = []string{"👍", "❤️", "😂", "😮", "😢", "🙏", "🔥", "🎉", "👎", "😡", "😍", "🎊", "👀", "💯", "🤔", "👏"}
