package web

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/source"
)

// TestNavbarGlobalCounts verifies the navbar renders the live global counts
// (REQ-0006-002): "<N> conversations · <M> messages" in the dim mono span.
func TestNavbarGlobalCounts(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()

	convs, err := st.ListConversations(ctx)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	total, err := st.CountMessages(ctx)
	if err != nil {
		t.Fatalf("count messages: %v", err)
	}

	body := get(t, srv, "/").Body.String()

	// The counts live in the navbar-counts span in tabular mono.
	if !contains(body, "navbar-counts") {
		t.Error("navbar missing the global-counts span")
	}
	want := itoa(int64(len(convs))) + " conversations · " + itoa(int64(total)) + " messages"
	if !contains(body, want) {
		t.Errorf("navbar missing global counts %q", want)
	}
	if total <= 0 || len(convs) == 0 {
		t.Fatalf("fixture should have conversations+messages (got %d convs / %d msgs)", len(convs), total)
	}
}

// TestSidebarPresenceDotAndSource verifies each conversation row renders a
// monogram avatar with a source-colored presence dot (REQ-0006-004), and that
// the filter input and CONVERSATIONS section are present (REQ-0006-003).
func TestSidebarPresenceDotAndSource(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := get(t, srv, "/").Body.String()

	for _, want := range []string{
		"Filter conversations", // filter input placeholder
		"sidebar-filter",       // filter input shell
		"avatar-mono",          // monogram avatar
		"presence-dot",         // presence dot element
	} {
		if !contains(body, want) {
			t.Errorf("sidebar missing %q", want)
		}
	}
	// The fixture's conversations are Signal, so the presence dot carries the
	// signal source modifier (blue, derived from --color-info).
	if !contains(body, "presence-dot src-signal") {
		t.Errorf("sidebar presence dot missing src-signal source color")
	}
}

// TestSelectedRowAccentRail verifies the open conversation's sidebar row carries
// the selected modifier (accent left rail + #1b2330 tint, REQ-0006-003) and that
// non-open rows do not.
func TestSelectedRowAccentRail(t *testing.T) {
	srv, st, _ := newTestServer(t)
	conv, err := st.GetConversation(context.Background(), "Harper")
	if err != nil || conv == nil {
		t.Fatalf("get conversation: %v", err)
	}

	open := get(t, srv, "/c/"+itoa(conv.ID)).Body.String()
	if !contains(open, "conv-row-selected") {
		t.Error("open conversation row missing the selected accent rail modifier")
	}
	// The selected modifier must hang off the active conversation's own row link.
	if !contains(open, `href="/c/`+itoa(conv.ID)+`" class="conv-row conv-row-selected"`) {
		t.Errorf("selected modifier not on the active conversation row")
	}

	// On a non-conversation page nothing is selected.
	home := get(t, srv, "/").Body.String()
	if contains(home, "conv-row-selected") {
		t.Error("home page should not mark any conversation row selected")
	}
}

// TestSourceSlug verifies the Go-chosen source modifier classes used by presence
// dots and source pills (REQ-0006-004).
func TestSourceSlug(t *testing.T) {
	cases := map[string]string{
		source.Signal:   "src-signal",
		source.IMessage: "src-imessage",
		"":              "src-unknown",
		"bogus":         "src-unknown",
	}
	for in, want := range cases {
		if got := sourceSlug(in); got != want {
			t.Errorf("sourceSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuiltCSSCarriesShellComponents guards the CSS drift requirement: the
// committed, go:embed-served app.css must contain the bespoke shell + identity
// component rules and the source-derived colors, so the clean rebuild is what
// actually ships (REQ-0006-002/003/004; ADR-0012 drift guard).
func TestBuiltCSSCarriesShellComponents(t *testing.T) {
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	out := string(css)
	for _, want := range []string{
		".app-navbar",                // navbar height
		".navbar-counts",             // global counts
		".navbar-toggle",             // circular theme toggle
		".sidebar-filter",            // filter input
		".avatar-mono",               // monogram avatar
		".presence-dot.src-signal",   // Signal presence dot
		".presence-dot.src-imessage", // iMessage presence dot
		".source-pill.src-signal",    // Signal source pill
		".source-pill.src-imessage",  // iMessage source pill
		".conv-row-selected",         // selected-row modifier
		"--color-info:#3b82f6",       // Signal blue token (slate)
		"--color-success:#34c759",    // iMessage green token (slate)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("built app.css missing %q (rebuild: rm -rf .tools && make css)", want)
		}
	}
	// The presence dot reads its color from the theme variables so both slate and
	// slate-light variants restyle automatically.
	if !strings.Contains(out, ".presence-dot.src-signal{background:var(--color-info)}") {
		t.Error("presence dot should derive its color from --color-info")
	}
}

// TestThemeStillSelfHosted re-checks ADR-0010/REQ-0006-001: every script the
// shell loads is same-origin, so the strict CSP (script-src 'self') holds with
// the new sidebar.js.
func TestThemeStillSelfHosted(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := get(t, srv, "/")
	body := rec.Body.String()
	for _, src := range []string{"/static/theme.js", "/static/sidebar.js", "/static/htmx.min.js"} {
		if !contains(body, `src="`+src+`"`) {
			t.Errorf("page missing self-hosted script %q", src)
		}
	}
	// No off-origin script/style references slipped in. (SVG xmlns URLs are not
	// fetched, so we check src=/href= attributes specifically.)
	for _, bad := range []string{`src="http://`, `src="https://`, `href="https://cdn`} {
		if contains(body, bad) {
			t.Errorf("page references an off-origin asset (%q); CSP would block it", bad)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !contains(csp, "script-src 'self'") {
		t.Errorf("CSP no longer restricts scripts to self: %q", csp)
	}
}
