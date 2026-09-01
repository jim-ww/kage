package emojipicker

import (
	"sort"

	"github.com/enescakir/emoji"
)

// shortcodes is the full :shortcode: list, computed once - emoji.Map() is a
// static built-in table, no need to rebuild it on every keystroke.
var shortcodes = func() []string {
	m := emoji.Map()
	codes := make([]string, 0, len(m))
	for code := range m {
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
