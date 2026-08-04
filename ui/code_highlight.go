package ui

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// fencedCodeBlock matches ```lang ... ``` blocks, capturing the (optional)
// language tag and the code body. The tag isn't required to be on its own
// line — chat input is often typed in one go without pausing for a newline
// right after "```lang" — so the body is just whatever follows the tag up
// to the closing fence.
var fencedCodeBlock = regexp.MustCompile("(?s)```([a-zA-Z0-9_+-]*)(.*?)```")

// highlightCodeBlocks replaces fenced code blocks in content with
// syntax-highlighted ANSI text, leaving everything else untouched.
func highlightCodeBlocks(content string) string {
	if !strings.Contains(content, "```") {
		return content
	}
	return fencedCodeBlock.ReplaceAllStringFunc(content, func(block string) string {
		match := fencedCodeBlock.FindStringSubmatch(block)
		lang, code := match[1], strings.TrimSpace(match[2])
		highlighted, err := highlightCode(lang, code)
		if err != nil {
			return block
		}
		return highlighted
	})
}

// highlightCode renders code as ANSI-colored text using chroma, guessing the
// language from lang (a fenced-block tag) or, failing that, from the code
// content itself.
func highlightCode(lang, code string) (string, error) {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := formatters.TTY256.Format(&buf, style, iterator); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
