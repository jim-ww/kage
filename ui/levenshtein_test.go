package ui

import "testing"

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"receive", "recieve", 2},
		{"hello", "hello", 0},
	}
	for _, tt := range tests {
		if got := levenshtein(tt.a, tt.b); got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestFuzzyContains(t *testing.T) {
	tests := []struct {
		content, query string
		want           bool
	}{
		{"I will receive the package tomorrow", "recieve", true},
		{"nothing relevant here", "xyzzy", false},
		{"exact match here", "match", true},
		{"", "match", false},
		{"anything", "", true},
	}
	for _, tt := range tests {
		if got := fuzzyContains(tt.content, tt.query); got != tt.want {
			t.Errorf("fuzzyContains(%q, %q) = %v, want %v", tt.content, tt.query, got, tt.want)
		}
	}
}
