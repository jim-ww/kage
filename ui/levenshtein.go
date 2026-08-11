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

// fuzzyContains reports whether content contains query as a plain substring
// or, failing that, has a word within typo-tolerant Levenshtein distance of
// it — used by the search-results popup's '/' filter. The substring check
// comes first and is what makes live-as-you-type filtering work at all: a
// short/partial query like "m" is a plain prefix of "message", but
// Levenshtein distance alone rejects it (word/query length mismatch
// dominates the score long before the word is fully typed) — plain
// substring containment has no such bias against short queries, so it
// always wins when it applies. Levenshtein only kicks in as a fallback,
// for a genuine typo like "recieve" that isn't a substring of "receive" at
// all. Its threshold scales with query length (a third of its rune count,
// minimum 1) so short queries still require a close match.
func fuzzyContains(content, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	lowerContent := strings.ToLower(content)
	if strings.Contains(lowerContent, query) {
		return true
	}
	threshold := max(1, len([]rune(query))/3)
	for _, word := range strings.FieldsFunc(lowerContent, func(r rune) bool {
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
