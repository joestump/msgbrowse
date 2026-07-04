package web

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/source"
)

// TestToolbarContextualTitle verifies the unified toolbar's contextual title
// (#152 Option A): "msgbrowse" on home, the active conversation's display name on
// a transcript page, and that the title links home. It also asserts Option A
// dropped the old global counts from the toolbar (they moved to Status under
// Settings → Diagnostics).
func TestToolbarContextualTitle(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()

	// Home: the toolbar shows the "msgbrowse" wordmark in the contextual title.
	home := get(t, srv, "/").Body.String()
	if !contains(home, "app-toolbar") {
		t.Error("home missing the unified toolbar")
	}
	if !contains(home, `class="toolbar-title" aria-label="msgbrowse home"`) {
		t.Error("home toolbar title should be the msgbrowse-home link")
	}
	if !contains(home, ">msgbrowse</a>") {
		t.Error("home toolbar title should read 'msgbrowse'")
	}
	// Option A removed the global counts from the toolbar entirely.
	if contains(home, "navbar-counts") || contains(home, " conversations · ") {
		t.Error("Option A should have removed the global counts from the toolbar")
	}

	// Transcript: the toolbar title becomes the conversation's display name.
	conv, err := st.GetConversation(ctx, "Harper")
	if err != nil || conv == nil {
		t.Fatalf("get conversation: %v", err)
	}
	convPage := get(t, srv, "/c/"+itoa(conv.ID)).Body.String()
	if !contains(convPage, ">"+humanName(conv.Name)+"</a>") {
		t.Errorf("transcript toolbar title should read the conversation name %q", humanName(conv.Name))
	}
	// It is still the home link (clickable back to home), not a plain heading.
	if !contains(convPage, `class="toolbar-title" aria-label="msgbrowse home"`) {
		t.Error("transcript toolbar title should still link home")
	}
}

// TestToolbarSearchForm verifies the toolbar's search pill is a boosted GET form
// targeting /search with a labelled input (#152), so Enter runs a search on the
// existing /search page.
func TestToolbarSearchForm(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := get(t, srv, "/").Body.String()

	if !contains(body, `<form action="/search" method="get" role="search"`) {
		t.Error("toolbar search form should be a GET to /search with role=search")
	}
	if !contains(body, "toolbar-search") {
		t.Error("toolbar missing the search pill")
	}
	// Boosted so Enter swaps the /search page into #main-content (boosted-nav).
	if !contains(body, `class="toolbar-search hidden sm:flex"`) ||
		!contains(body, `hx-boost="true"`) {
		t.Error("toolbar search form should be boosted (hx-boost)")
	}
	// The input is named q, labelled, and carries the placeholder.
	if !contains(body, `name="q"`) ||
		!contains(body, `aria-label="Search messages"`) ||
		!contains(body, `placeholder="Search messages"`) {
		t.Error("toolbar search input should be named q, labelled, and placeheld 'Search messages'")
	}
}

// TestToolbarIconButtonsLabelled verifies every icon-only toolbar control carries
// an aria-label and the header stays role=banner (#152 accessibility).
func TestToolbarIconButtonsLabelled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := get(t, srv, "/").Body.String()

	for _, want := range []string{
		`aria-label="Toggle sidebar"`, // mobile drawer toggle
		`aria-label="Toggle theme"`,   // theme toggle
		`aria-label="Settings"`,       // settings gear → /settings
	} {
		if !contains(body, want) {
			t.Errorf("toolbar icon button missing aria-label %q", want)
		}
	}
	// The theme toggle is still the existing data-theme-toggle control.
	if !contains(body, "data-theme-toggle") {
		t.Error("toolbar missing the data-theme-toggle control")
	}
	// The settings gear links to /settings.
	if !contains(body, `href="/settings"`) {
		t.Error("toolbar settings gear should link to /settings")
	}
	// The header keeps banner semantics (daisyUI dropped, but role=banner is the
	// implicit role of <header> not nested in a section/article — which holds here).
	if !contains(body, "<header ") {
		t.Error("toolbar should be a <header> (role=banner)")
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
		".app-toolbar",               // unified toolbar (#152 Option A)
		".toolbar-title",             // contextual title
		".toolbar-icon-btn",          // icon buttons (drawer/theme/settings)
		".toolbar-search",            // toolbar search pill
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
