package store

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/joestump/msgbrowse/internal/signal"
)

// Markdown tokens stripped from sidebar previews. The target pattern tolerates
// one level of nested parens (Signal media names contain parens — issue #66),
// mirroring the parser's mdTarget in internal/signal.
var (
	previewImageRe = regexp.MustCompile(`!\[[^\]]*\]\((?:[^()]|\([^()]*\))+\)`)
	previewLinkRe  = regexp.MustCompile(`\[([^\]]*)\]\((?:[^()]|\([^()]*\))+\)`)
)

// preview returns a single-line, length-capped excerpt of a message body for
// sidebar previews. Raw Markdown never leaks into the sidebar: image tokens
// collapse to a 📷 placeholder and links to their text. Invisible Unicode
// format runes (zero-width joiners etc.) are dropped and leading blockquote
// markers stripped, then whitespace is collapsed and the result truncated on a
// rune boundary.
func preview(body string, max int) string {
	s := previewImageRe.ReplaceAllString(body, "📷 ")
	s = previewLinkRe.ReplaceAllString(s, "$1")
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = stripQuoteMarkers(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	// Cap on a rune boundary.
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}

// stripQuoteMarkers removes leading ">" quote markers (possibly nested, with
// interleaved spaces) from each line, so a quoted reply previews as its text
// rather than raw "> " markdown.
func stripQuoteMarkers(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		for strings.HasPrefix(trimmed, ">") {
			trimmed = strings.TrimLeft(trimmed[1:], " \t")
		}
		lines[i] = trimmed
	}
	return strings.Join(lines, "\n")
}

// reverse reverses a slice of MessageView in place.
func reverse(m []MessageView) {
	for i, j := 0, len(m)-1; i < j; i, j = i+1, j-1 {
		m[i], m[j] = m[j], m[i]
	}
}

// parseRFC3339 parses an RFC3339 timestamp, returning the zero time on error.
func parseRFC3339(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// parseLayout parses a "YYYY-MM-DD HH:MM:SS" timestamp, returning the zero time
// on error.
func parseLayout(s string) time.Time {
	t, _ := time.Parse(signal.TimestampLayout, s)
	return t
}
