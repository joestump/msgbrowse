package web

import (
	"context"
	"net/http"
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
		source.WhatsApp: "src-whatsapp",
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
		".presence-dot.src-whatsapp", // WhatsApp presence dot (REQ-0009-007)
		".source-pill.src-signal",    // Signal source pill
		".source-pill.src-imessage",  // iMessage source pill
		".source-pill.src-whatsapp",  // WhatsApp source pill (REQ-0009-007)
		".conv-row-selected",         // selected-row modifier
		".pin-btn",                   // pin/unpin toggle (REQ-0006-010)
		".pin-btn-active",            // pinned-state fill
		"--color-info:#3b82f6",       // Signal blue token (slate)
		"--color-success:#34c759",    // iMessage green token (slate)
		"--color-whatsapp:#25d366",   // WhatsApp green token (slate)
		"--color-whatsapp:#128c7e",   // WhatsApp teal-green token (slate-light)
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
	if !strings.Contains(out, ".presence-dot.src-whatsapp{background:var(--color-whatsapp)}") {
		t.Error("whatsapp presence dot should derive its color from --color-whatsapp")
	}
}

// TestPinnedSection covers REQ-0006-010 end to end: with nothing pinned the
// sidebar shows no PINNED label; POSTing to /c/{id}/pin 303-redirects back, marks
// the conversation pinned, and the sidebar then renders it under a PINNED
// section. A second POST unpins it. The pin button on the conversation header
// reflects the current state.
func TestPinnedSection(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()

	conv, err := st.GetConversation(ctx, "Harper")
	if err != nil || conv == nil {
		t.Fatalf("get conversation: %v", err)
	}
	cid := itoa(conv.ID)

	// Nothing pinned: no PINNED label, and the header button reads "Pin".
	home := get(t, srv, "/").Body.String()
	if contains(home, ">Pinned<") {
		t.Error("PINNED section should be hidden when nothing is pinned")
	}
	convPage := get(t, srv, "/c/"+cid).Body.String()
	if !contains(convPage, `action="/c/`+cid+`/pin"`) {
		t.Error("conversation header missing the pin form POST")
	}
	if !contains(convPage, ">Pin<") {
		t.Error("pin button should read 'Pin' when unpinned")
	}

	// Pin via the POST route: expect a 303 redirect back to the conversation.
	rec := post(t, srv, "/c/"+cid+"/pin")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("pin POST status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/c/"+cid {
		t.Errorf("pin redirect = %q, want %q", loc, "/c/"+cid)
	}

	// Sidebar now has a PINNED section listing the conversation.
	home = get(t, srv, "/").Body.String()
	if !contains(home, ">Pinned<") {
		t.Error("PINNED section should appear after pinning")
	}
	if !contains(home, `id="sidebar-pinned"`) {
		t.Error("PINNED list container missing")
	}
	// The pinned row links to the conversation and uses the shared row markup.
	if !contains(home, `<ul id="sidebar-pinned"`) || !contains(home, `href="/c/`+cid+`"`) {
		t.Error("pinned conversation row missing from PINNED section")
	}

	// Header button now reads "Unpin".
	convPage = get(t, srv, "/c/"+cid).Body.String()
	if !contains(convPage, ">Unpin<") {
		t.Error("pin button should read 'Unpin' when pinned")
	}

	// Unpin: second POST flips it back; PINNED section disappears.
	if rec := post(t, srv, "/c/"+cid+"/pin"); rec.Code != http.StatusSeeOther {
		t.Fatalf("unpin POST status = %d, want 303", rec.Code)
	}
	home = get(t, srv, "/").Body.String()
	if contains(home, ">Pinned<") {
		t.Error("PINNED section should be gone after unpinning")
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
