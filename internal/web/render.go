package web

import (
	"fmt"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"

	"github.com/joestump/sigbrowse/internal/signal"
)

// bodyTokenRe matches, in priority order, a Markdown image, a Markdown link, or
// a bare http(s) URL. Everything else is treated as plain text and escaped.
var bodyTokenRe = regexp.MustCompile(
	`(!\[[^\]]*\]\([^)]+\))` + // 1: image
		`|(\[[^\]]*\]\([^)]+\))` + // 2: markdown link
		`|(https?://[^\s<>()\[\]"'` + "`" + `]+)`, // 3: bare url
)

// mdLinkParts extracts the text and target from a Markdown link/image token.
var mdLinkParts = regexp.MustCompile(`^!?\[([^\]]*)\]\(([^)]+)\)$`)

// renderBody converts a raw message body into safe HTML for the transcript.
// Image markdown is dropped (images are rendered separately as thumbnails);
// Markdown links to URLs become anchors; Markdown links to media are dropped
// (shown as file attachments); bare URLs are linkified. All other text is
// HTML-escaped, so message content (which is untrusted) can never inject markup.
func renderBody(body string) template.HTML {
	if body == "" {
		return ""
	}
	var b strings.Builder
	last := 0
	for _, loc := range bodyTokenRe.FindAllStringSubmatchIndex(body, -1) {
		// Escape the plain text preceding this token.
		if loc[0] > last {
			b.WriteString(escapeText(body[last:loc[0]]))
		}
		last = loc[1]
		token := body[loc[0]:loc[1]]
		switch {
		case loc[2] >= 0: // image: drop
			// no-op
		case loc[4] >= 0: // markdown link
			if parts := mdLinkParts.FindStringSubmatch(token); parts != nil {
				text, target := parts[1], strings.TrimSpace(parts[2])
				if isURL(target) {
					b.WriteString(anchor(target, text))
				}
				// else: media file link — drop (rendered as attachment)
			}
		case loc[6] >= 0: // bare url
			u := strings.TrimRight(token, trailingURLPunct)
			b.WriteString(anchor(u, u))
			// Re-append any stripped trailing punctuation as escaped text.
			if len(u) < len(token) {
				b.WriteString(escapeText(token[len(u):]))
			}
		}
	}
	if last < len(body) {
		b.WriteString(escapeText(body[last:]))
	}
	return template.HTML(b.String())
}

// trailingURLPunct mirrors the parser's bare-URL trimming.
const trailingURLPunct = ".,;:!?)]}>\"'"

// escapeText escapes plain text and turns newlines into <br>.
func escapeText(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>")
}

// anchor builds a safe external link. The href is URL- and attribute-escaped and
// carries rel attributes that prevent referrer leakage and tab-nabbing.
func anchor(href, text string) string {
	safeHref := html.EscapeString(href)
	return fmt.Sprintf(`<a href="%s" target="_blank" rel="noopener noreferrer nofollow">%s</a>`,
		safeHref, html.EscapeString(text))
}

func isURL(target string) bool {
	return strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")
}

// mediaURL builds the in-app URL that serves an attachment for a conversation.
// The conversation is keyed by id (names contain spaces and punctuation); the
// relative path is URL-path-escaped segment by segment.
func mediaURL(convID int64, relPath string) string {
	clean := strings.TrimPrefix(relPath, "./")
	parts := strings.Split(clean, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return fmt.Sprintf("/media/%d/%s", convID, strings.Join(parts, "/"))
}

// humanSize renders a byte count as a human-readable string.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// domainOf is a thin wrapper so templates can group links by domain.
func domainOf(rawurl string) string { return signal.Domain(rawurl) }
