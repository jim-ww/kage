package ui

import (
	"strings"
	"testing"
)

func TestFormatMessageBodyCodeBlock(t *testing.T) {
	content := "check this out:\n```go\nfunc main() {}\n```\nneat right"
	got := FormatMessageBody(content)

	if !strings.Contains(got, "check this out:") || !strings.Contains(got, "neat right") {
		t.Fatalf("surrounding text lost: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Fatalf("fence markers should be stripped: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI escape codes in highlighted output: %q", got)
	}
}

func TestFormatMessageBodyNoMarkers(t *testing.T) {
	content := "just plain text, no code here"
	if got := FormatMessageBody(content); got != content {
		t.Fatalf("plain text should be returned unchanged, got %q", got)
	}
}

func TestFormatMessageBodyLangGluedToCode(t *testing.T) {
	// Real chat input is often typed without pausing after "```lang" for a
	// newline — the fence and first code line land on the same source line.
	content := "```nix inputs = {\n    nixpkgs.url = \"...\";\n  };\n```"
	got := FormatMessageBody(content)

	if strings.Contains(got, "```") {
		t.Fatalf("fence markers should be stripped: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI escape codes in highlighted output: %q", got)
	}
	if !strings.Contains(got, "inputs") || !strings.Contains(got, "nixpkgs") {
		t.Fatalf("code content lost: %q", got)
	}
}

func TestFormatMessageBodyUnknownLang(t *testing.T) {
	content := "```\nsome unlabeled content\n```"
	got := FormatMessageBody(content)
	if !strings.Contains(got, "some unlabeled content") {
		t.Fatalf("code content lost for unlabeled block: %q", got)
	}
}

func TestFormatMessageBodyInlineSpans(t *testing.T) {
	content := "this is *bold* and _italic_ and ~strike~ and `code` here"
	got := FormatMessageBody(content)
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI escape codes, got %q", got)
	}
	if strings.Contains(got, "*bold*") || strings.Contains(got, "_italic_") || strings.Contains(got, "~strike~") || strings.Contains(got, "`code`") {
		t.Fatalf("markers should not survive unstyled: %q", got)
	}
}

func TestFormatMessageBodyNoFalsePositives(t *testing.T) {
	content := "5 * 3 = 15 should not bold, snake_case_name should not italic"
	if got := FormatMessageBody(content); got != content {
		t.Fatalf("expected no styling applied, got %q", got)
	}
}

func TestFormatMessageBodyAdjacentSpans(t *testing.T) {
	content := "*a* *b* adjacent spans"
	got := FormatMessageBody(content)
	if !strings.Contains(got, "adjacent spans") {
		t.Fatalf("trailing text lost: %q", got)
	}
	if strings.Count(got, "\x1b[") != 4 {
		t.Fatalf("expected both adjacent spans styled (open+reset each), got %q", got)
	}
}

func TestStripMessageStylingRemovesMarkers(t *testing.T) {
	content := "this is *bold* and _italic_ and ~strike~ and `code` here"
	got := StripMessageStyling(content)
	want := "this is bold and italic and strike and code here"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStripMessageStylingCodeBlock(t *testing.T) {
	content := "before\n```go\nfunc main() {}\n```\nafter"
	got := StripMessageStyling(content)
	if strings.Contains(got, "```") || strings.Contains(got, "\x1b[") {
		t.Fatalf("expected plain text with no fence markers or ANSI, got %q", got)
	}
	if !strings.Contains(got, "func main() {}") {
		t.Fatalf("code content lost: %q", got)
	}
}

func TestStripMessageStylingNoFalsePositives(t *testing.T) {
	content := "5 * 3 = 15, snake_case_name, a~b"
	if got := StripMessageStyling(content); got != content {
		t.Fatalf("expected unchanged, got %q", got)
	}
}
