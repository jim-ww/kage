package ui

import "testing"

func TestTrimComposedKeepsIndentation(t *testing.T) {
	cases := map[string]string{
		"  a = 1\n  b = 2\n":         "  a = 1\n  b = 2",
		"\n\n  a = 1\n  b = 2  \n\n": "  a = 1\n  b = 2",
		"hello  ":                    "hello",
		" \n \t\n":                   "",
		"  ":                         "",
	}
	for in, want := range cases {
		if got := trimComposed(in); got != want {
			t.Errorf("trimComposed(%q) = %q, want %q", in, got, want)
		}
	}
}
