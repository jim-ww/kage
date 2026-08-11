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
		// Regression: a short/partial query (as typed live, one keystroke at
		// a time) must match as a plain prefix/substring, not get rejected
		// by Levenshtein distance just because "m" and "message" are very
		// different lengths.
		{"Test message", "m", true},
		{"Test message", "me", true},
		{"Test message", "mes", true},
		{"Test message", "message", true},
	}
	for _, tt := range tests {
		if got := fuzzyContains(tt.content, tt.query); got != tt.want {
			t.Errorf("fuzzyContains(%q, %q) = %v, want %v", tt.content, tt.query, got, tt.want)
		}
	}
}
