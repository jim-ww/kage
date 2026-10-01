package xmpp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestStripReplyFallbackCodePoints pins XEP-0426 counting: XEP-0428's
// start/end are Unicode code points, not bytes. Slicing the body by them
// directly happens to work for ASCII (where the two coincide) and silently
// corrupts anything else - either leaving quote text behind or cutting a
// multi-byte character in half, which is what makes a reply from a
// spec-compliant client render as mojibake.
func TestStripReplyFallbackCodePoints(t *testing.T) {
	for _, tc := range []struct {
		name  string
		quote string
		reply string
	}{
		{name: "ascii", quote: "> bob: are you there?\n", reply: "yes"},
		{name: "cyrillic", quote: "> Иван: ты тут?\n", reply: "да, тут"},
		{name: "japanese", quote: "> たろう: こんにちは世界\n", reply: "やあ"},
		{name: "emoji", quote: "> bob: 🧛🏾 👨‍👨‍👦‍👦 🇺🇳\n", reply: "nice"},
		{name: "mixed", quote: "> Иван: hello 世界 🇺🇳\n", reply: "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.quote + tc.reply
			end := utf8.RuneCountInString(tc.quote)
			fb := &fallbackElem{
				For:  "urn:xmpp:reply:0",
				Body: &fallbackBodyElem{End: &end},
			}
			got := stripReplyFallback(body, fb)
			if got != tc.reply {
				t.Errorf("stripReplyFallback() = %q, want %q", got, tc.reply)
			}
			if !utf8.ValidString(got) {
				t.Errorf("stripReplyFallback() produced invalid UTF-8: %q", got)
			}
		})
	}
}

// TestSendReplyFallbackEndIsCodePoints is the other half of the round trip:
// what Send puts on the wire has to be counted the same way a compliant
// receiver will read it, or every non-ASCII reply we send is the one that
// renders wrong on the other end.
func TestSendReplyFallbackEndIsCodePoints(t *testing.T) {
	quote := BuildFallbackQuote("Иван", "ты тут?")
	if utf8.RuneCountInString(quote) == len(quote) {
		t.Fatal("test quote must be non-ASCII for byte/code-point counts to differ")
	}
	body := quote + "да"

	end := utf8.RuneCountInString(quote)
	fb := &fallbackElem{For: "urn:xmpp:reply:0", Body: &fallbackBodyElem{End: &end}}
	if got := stripReplyFallback(body, fb); got != "да" {
		t.Errorf("round trip with code point offset = %q, want %q", got, "да")
	}

	// The byte count this used to send instead overshoots the body's code
	// point length entirely, so a compliant receiver can't even apply it.
	byteEnd := len(quote)
	fbBytes := &fallbackElem{For: "urn:xmpp:reply:0", Body: &fallbackBodyElem{End: &byteEnd}}
	if got := stripReplyFallback(body, fbBytes); got == "да" {
		t.Error("byte offset should not strip cleanly; test no longer distinguishes the two")
	}
}

// TestStripReplyFallbackIgnoresUnrelated covers the guards: a fallback for a
// different feature (e.g. XEP-0424 retraction), or offsets that don't
// address the body, must leave the body completely alone rather than
// mangling it.
func TestStripReplyFallbackIgnoresUnrelated(t *testing.T) {
	body := "> Иван: ты тут?\nда"
	past := 9999
	negative := -1
	zero := 0

	for _, tc := range []struct {
		name string
		fb   *fallbackElem
	}{
		{name: "nil", fb: nil},
		{name: "other feature", fb: &fallbackElem{For: "urn:xmpp:message-retract:1", Body: &fallbackBodyElem{End: &zero}}},
		{name: "no body range", fb: &fallbackElem{For: "urn:xmpp:reply:0"}},
		{name: "end past body", fb: &fallbackElem{For: "urn:xmpp:reply:0", Body: &fallbackBodyElem{End: &past}}},
		{name: "negative start", fb: &fallbackElem{For: "urn:xmpp:reply:0", Body: &fallbackBodyElem{Start: &negative, End: &zero}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripReplyFallback(body, tc.fb); got != body {
				t.Errorf("stripReplyFallback() = %q, want it unchanged (%q)", got, body)
			}
		})
	}
}

// TestStripReplyFallbackWholeBody is the no-start/no-end form Send uses for
// retractions, and the start-only form: an absent end means "to the end of
// the body".
func TestStripReplyFallbackWholeBody(t *testing.T) {
	body := "> Иван: ты тут?\nда"
	start := utf8.RuneCountInString("> Иван: ты тут?\n")
	fb := &fallbackElem{For: "urn:xmpp:reply:0", Body: &fallbackBodyElem{Start: &start}}
	if got := stripReplyFallback(body, fb); got != "> Иван: ты тут?\n" {
		t.Errorf("stripReplyFallback() = %q, want the trailing range removed", got)
	}
}

// TestRuneOffsetToByte covers the boundary cases the slicing depends on:
// the very end of the string is a valid offset (it's where an end range
// stops), anything past it is not.
func TestRuneOffsetToByte(t *testing.T) {
	s := "Иван" // 4 code points, 8 bytes
	for _, tc := range []struct {
		off, want int
	}{
		{0, 0}, {1, 2}, {2, 4}, {3, 6}, {4, 8}, {5, -1}, {-1, -1},
	} {
		if got := runeOffsetToByte(s, tc.off); got != tc.want {
			t.Errorf("runeOffsetToByte(%q, %d) = %d, want %d", s, tc.off, got, tc.want)
		}
	}
	if got := runeOffsetToByte("", 0); got != 0 {
		t.Errorf(`runeOffsetToByte("", 0) = %d, want 0`, got)
	}
}

// TestDispatchArchiveResultReplyNonASCII is TestDispatchArchiveResultReply's
// non-ASCII counterpart, guarding the MAM path against the same byte/code
// point confusion now that it shares stripReplyFallback with the live path.
func TestDispatchArchiveResultReplyNonASCII(t *testing.T) {
	quote := "> Иван: ты тут?\n"
	end := utf8.RuneCountInString(quote)
	msg := messageBody{
		Body:     quote + "да, тут",
		Reply:    &replyElem{ID: "orig1"},
		Fallback: &fallbackElem{For: "urn:xmpp:reply:0", Body: &fallbackBodyElem{End: &end}},
	}

	c := &Client{}
	ch := make(chan ArchivedMessage, 1)
	c.mamWaiters = map[string]chan ArchivedMessage{"q1": ch}
	c.dispatchArchiveResult(&mamResultElem{
		QueryID:   "q1",
		ID:        "arch1",
		Forwarded: mamForwarded{Message: msg},
	})

	select {
	case am := <-ch:
		if am.ReplyToID != "orig1" {
			t.Errorf("ReplyToID = %q, want %q", am.ReplyToID, "orig1")
		}
		if am.Body != "да, тут" {
			t.Errorf("Body = %q, want %q", am.Body, "да, тут")
		}
		if strings.Contains(am.Body, "�") {
			t.Errorf("Body contains a replacement character: %q", am.Body)
		}
	default:
		t.Fatal("dispatchArchiveResult did not deliver to waiter")
	}
}
