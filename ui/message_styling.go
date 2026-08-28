package ui

import (
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// XEP-0393 (Message Styling) inline spans: *bold*, _emphasis_,
// ~strikethrough~, `inline code`. Per the spec a marker only opens/closes a
// span when it sits directly against non-whitespace on the inside, and
// against whitespace/punctuation/a string edge on the outside - that's what
// keeps "5 * 3 = 15" or "snake_case_name" from being misread as styling.
var (
	boldSpan   = regexp.MustCompile(`(^|[\s([{])\*(\S(?:[^*\n]*\S)?)\*($|[\s)\]}.,!?;:])`)
	italicSpan = regexp.MustCompile(`(^|[\s([{])_(\S(?:[^_\n]*\S)?)_($|[\s)\]}.,!?;:])`)
	strikeSpan = regexp.MustCompile(`(^|[\s([{])~(\S(?:[^~\n]*\S)?)~($|[\s)\]}.,!?;:])`)
	codeSpan   = regexp.MustCompile("(^|[\\s([{])`(\\S(?:[^`\\n]*\\S)?)`($|[\\s)\\]}.,!?;:])")
)

var (
	// Bold(true) alone renders inconsistently across terminals - some just
	// tint the existing color instead of using a heavier glyph - so bold
	// text also gets a color to stay visually distinct everywhere. Matches
	// the theme's default PopupDanger red (theme.go) rather than a one-off.
	boldStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f7768e"))
	italicStyle = lipgloss.NewStyle().Italic(true)
	strikeStyle = lipgloss.NewStyle().Strikethrough(true)
	// urlStyle is a fixed ANSI blue rather than a theme color - links should
	// read the same regardless of which theme is active, the way a browser's
	// link color doesn't follow the page's palette.
	urlStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("12"))
)

// FormatMessageBody renders XEP-0393 message styling markup (fenced code
// blocks, inline code, bold, emphasis, strikethrough) as ANSI-styled text for
// display in the chat view. Used only where the terminal renders styled
// content directly - never for previews (see StripMessageStyling).
func FormatMessageBody(content string) string {
	content = urlPattern.ReplaceAllStringFunc(content, func(url string) string {
		return urlStyle.Render(url)
	})
	if !hasStylingMarkers(content) {
		return content
	}
	content, fences := extractFenced(content, func(lang, code string) string {
		highlighted, err := highlightCode(lang, code)
		if err != nil {
			return "```" + lang + "\n" + code + "\n```"
		}
		return highlighted
	})
	content, codes := extractSpan(content, codeSpan, func(s string) string {
		// No language tag on an inline span (unlike a fenced block) - let
		// highlightCode guess from content, same fallback chain as fences.
		highlighted, err := highlightCode("", s)
		if err != nil {
			return s
		}
		return highlighted
	})
	content = applySpan(content, boldSpan, func(s string) string { return boldStyle.Render(s) })
	content = applySpan(content, italicSpan, func(s string) string { return italicStyle.Render(s) })
	content = applySpan(content, strikeSpan, func(s string) string { return strikeStyle.Render(s) })
	content = restorePlaceholders(content, codes)
	content = restorePlaceholders(content, fences)
	return content
}

// StripMessageStyling removes XEP-0393 markup, leaving plain text - for
// anywhere a message is shown as an unstyled single line: the chat list's
// last-message preview, reply/delete-confirmation hints (both via
// MessagePreviewContent), and desktop notifications (notifyPreview).
func StripMessageStyling(content string) string {
	if !hasStylingMarkers(content) {
		return content
	}
	content, fences := extractFenced(content, func(_, code string) string { return code })
	content = applySpan(content, codeSpan, identitySpan)
	content = applySpan(content, boldSpan, identitySpan)
	content = applySpan(content, italicSpan, identitySpan)
	content = applySpan(content, strikeSpan, identitySpan)
	return restorePlaceholders(content, fences)
}

func identitySpan(s string) string { return s }

func hasStylingMarkers(content string) bool {
	return strings.ContainsAny(content, "*_~`")
}

// extractFenced pulls every ```lang ... ``` block out of content, replacing
// each with a placeholder so later inline-span passes never scan (and
// potentially corrupt) code content. render turns each block's language tag
// and body into the replacement text stored for restorePlaceholders.
func extractFenced(content string, render func(lang, code string) string) (string, []string) {
	if !strings.Contains(content, "```") {
		return content, nil
	}
	var extracted []string
	replaced := fencedCodeBlock.ReplaceAllStringFunc(content, func(block string) string {
		m := fencedCodeBlock.FindStringSubmatch(block)
		lang, code := m[1], strings.TrimSpace(m[2])
		extracted = append(extracted, render(lang, code))
		return placeholder(len(extracted) - 1)
	})
	return replaced, extracted
}

// extractSpan is applySpan, but defers rendering: each match is replaced
// with a placeholder immediately (so it can't be re-matched by a later
// span pass) and render(body) is stashed for restorePlaceholders.
func extractSpan(content string, re *regexp.Regexp, render func(string) string) (string, []string) {
	var extracted []string
	replaced := applySpan(content, re, func(body string) string {
		extracted = append(extracted, render(body))
		return placeholder(len(extracted) - 1)
	})
	return replaced, extracted
}

// applySpan replaces every match of re (one of the *Span patterns above,
// each shaped as leading-boundary/body/trailing-boundary) with the leading
// boundary followed by render(body). The trailing boundary is deliberately
// left unconsumed - re-scanning resumes right at it - so it can double as
// the leading boundary for an immediately adjacent span ("*a* *b*"), which
// a naive ReplaceAllStringFunc would miss since it consumes match text
// left-to-right with no overlap.
func applySpan(content string, re *regexp.Regexp, render func(string) string) string {
	var b strings.Builder
	rest := content
	for {
		loc := re.FindStringSubmatchIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:loc[0]])
		b.WriteString(rest[loc[2]:loc[3]])
		b.WriteString(render(rest[loc[4]:loc[5]]))
		rest = rest[loc[6]:]
	}
}

func placeholder(i int) string {
	return "\x00" + strconv.Itoa(i) + "\x00"
}

func restorePlaceholders(content string, values []string) string {
	for i, v := range values {
		content = strings.Replace(content, placeholder(i), v, 1)
	}
	return content
}
