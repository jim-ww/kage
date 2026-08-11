package ui

import (
	"strings"
	"unicode"
)

// levenshtein returns the edit distance between a and b (rune-wise): the
// minimum number of single-rune insertions, deletions, or substitutions
// needed to turn one into the other.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

// fuzzyContains reports whether content has a word within typo-tolerant
// Levenshtein distance of query — used by the search-results popup's '/'
// filter, so e.g. "recieve" still matches a message containing "receive".
// The threshold scales with query length (a third of its rune count,
// minimum 1) so short queries still require a close match rather than
// matching almost anything.
func fuzzyContains(content, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	threshold := max(1, len([]rune(query))/3)
	for _, word := range strings.FieldsFunc(strings.ToLower(content), func(r rune) bool {
		return !isWordRune(r)
	}) {
		if levenshtein(word, query) <= threshold {
			return true
		}
	}
	return false
}

// isWordRune reports whether r can appear inside a "word" for
// fuzzyContains's tokenizing — letters, digits, and marks (so accented
// letters composed as base+combining-mark stay joined to their word) count;
// everything else (spaces, punctuation) is a separator.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}
