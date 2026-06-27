package store

import (
	"strings"
	"time"

	"github.com/joestump/sigbrowse/internal/signal"
)

// preview returns a single-line, length-capped excerpt of a message body for
// sidebar previews. Newlines are collapsed to spaces.
func preview(body string, max int) string {
	s := strings.Join(strings.Fields(body), " ")
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
