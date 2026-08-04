package ui

import (
	"strings"
	"testing"
)

func TestHighlightCodeBlocks(t *testing.T) {
	content := "check this out:\n```go\nfunc main() {}\n```\nneat right"
	got := highlightCodeBlocks(content)

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

func TestHighlightCodeBlocksNoFence(t *testing.T) {
	content := "just plain text, no code here"
	if got := highlightCodeBlocks(content); got != content {
		t.Fatalf("plain text should be returned unchanged, got %q", got)
	}
}

func TestHighlightCodeBlocksLangGluedToCode(t *testing.T) {
	// Real chat input is often typed without pausing after "```lang" for a
	// newline — the fence and first code line land on the same source line.
	content := "```nix inputs = {\n    nixpkgs.url = \"...\";\n  };\n```"
	got := highlightCodeBlocks(content)

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

func TestHighlightCodeBlocksUnknownLang(t *testing.T) {
	content := "```\nsome unlabeled content\n```"
	got := highlightCodeBlocks(content)
	if !strings.Contains(got, "some unlabeled content") {
		t.Fatalf("code content lost for unlabeled block: %q", got)
	}
}
