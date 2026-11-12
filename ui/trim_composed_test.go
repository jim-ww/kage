package ui

import "testing"

func TestTrimComposedKeepsIndentation(t *testing.T) {
	cases := map[string]string{
		"  a = 1\n  b = 2\n":         "  a = 1\n  b = 2",
		"\n\n  a = 1\n  b = 2  \n\n": "  a = 1\n  b = 2",
		"hello  ":                    "hello",
		" \n \t\n":                   "",
		"  ":                         "",
		"\u00a0\n\u3000":             "",
		"\u200b":                     "",
		"\u200b\n  a\u00a0":          "  a",
	}
	for in, want := range cases {
		if got := trimComposed(in); got != want {
			t.Errorf("trimComposed(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlankMessagesAreNotSent(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n", " ", "​\n‍"} {
		m := newTestModelWithMessages(nil, nil)
		m.sender = &fakeSuccessSender{}
		m.input.SetValue(in)
		if cmd := m.sendCurrentInput(); cmd != nil || len(m.currentMessages()) != 0 {
			t.Errorf("%q was sent", in)
		}
	}
}
