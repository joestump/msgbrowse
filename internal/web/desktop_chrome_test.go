// Tests for the desktop-shell presentation seams the web layer exposes
// (SPEC-0010, issues #165/#167): the SetDesktopChrome template flag that pads
// the unified toolbar past the macOS traffic lights and loads the drag-region
// script, and the SetShellNotes provider that surfaces systray/dock startup
// diagnostics on the Logs page. All headless, CGO_ENABLED=0.
package web

import (
	"testing"
	"time"
)

// TestDesktopChromeFlagRendersBodyClassAndScript verifies the minimal
// template-flag mechanism (#165): with SetDesktopChrome(true) every full-page
// render carries the desktop-chrome <body> class and the /static/desktop.js
// include; without it (browser mode) neither appears.
func TestDesktopChromeFlagRendersBodyClassAndScript(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := get(t, srv, "/").Body.String()
	if contains(body, "desktop-chrome") {
		t.Error("browser mode must not carry the desktop-chrome body class")
	}
	if contains(body, "/static/desktop.js") {
		t.Error("browser mode must not load desktop.js")
	}

	srv.SetDesktopChrome(true)
	body = get(t, srv, "/").Body.String()
	if !contains(body, `class="bg-base-100 text-base-content desktop-chrome"`) {
		t.Error("desktop mode should add the desktop-chrome class to <body>")
	}
	if !contains(body, `<script src="/static/desktop.js" defer></script>`) {
		t.Error("desktop mode should load the drag-region script desktop.js")
	}
}

// TestDesktopChromeOnEveryFullPage confirms the flag flows through every
// full-page surface (they all share page_start), so the traffic-light inset
// never disappears when navigating without HTMX.
func TestDesktopChromeOnEveryFullPage(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.SetDesktopChrome(true)
	for _, path := range []string{"/", "/search", "/gallery", "/settings", "/logs", "/status", "/providers"} {
		body := get(t, srv, path).Body.String()
		if !contains(body, "desktop-chrome") {
			t.Errorf("GET %s missing the desktop-chrome body class", path)
		}
	}
}

// TestDesktopDragRegionInBuiltCSS guards the committed app.css against drift
// (the css.yml check rebuilds it, this fails earlier and locally): the built
// stylesheet must carry the toolbar drag region, the no-drag opt-outs for its
// interactive children, and the traffic-light inset scoped to desktop-chrome.
func TestDesktopDragRegionInBuiltCSS(t *testing.T) {
	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read embedded app.css: %v", err)
	}
	s := string(css)
	for _, want := range []string{
		"--wails-draggable:drag",
		"--wails-draggable:no-drag",
		".desktop-chrome .app-toolbar",
		".desktop-chrome .app-sidebar",
	} {
		if !contains(s, want) {
			t.Errorf("built app.css missing %q — run `rm -rf .tools && make css` after editing input.css", want)
		}
	}
}

// TestSidebarToggleAllWidths is the issue-#175 burger contract: the toolbar
// burger renders at every width (the old lg:hidden gate is gone), the shell
// loads sidebar-toggle.js (the CSP-clean lg+ collapse path, before paint like
// theme.js), and the built app.css carries the lg+ collapsed override plus the
// desktop-chrome traffic-light re-inset for the collapsed state.
func TestSidebarToggleAllWidths(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := get(t, srv, "/").Body.String()

	if !contains(body, `<label for="nav-drawer" class="toolbar-icon-btn" aria-label="Toggle sidebar">`) {
		t.Error("toolbar burger should render at all widths (toolbar-icon-btn, no lg:hidden)")
	}
	if contains(body, "lg:hidden") {
		t.Error("the burger must not be gated to narrow viewports (lg:hidden)")
	}
	if !contains(body, `<script src="/static/sidebar-toggle.js"></script>`) {
		t.Error("shell missing sidebar-toggle.js (loaded without defer, like theme.js)")
	}

	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read embedded app.css: %v", err)
	}
	s := string(css)
	for _, want := range []string{
		// The persistent collapse: sidebar-toggle.js flips this class on <html>.
		"html.sidebar-collapsed .drawer-side{display:none}",
		// Collapsed at lg+ in the desktop shell, the toolbar steps past the
		// traffic lights again (#165).
		"html.sidebar-collapsed .desktop-chrome .app-toolbar{padding-left:80px}",
	} {
		if !contains(s, want) {
			t.Errorf("built app.css missing %q — run `rm -rf .tools && make css` after editing input.css", want)
		}
	}
}

// TestLogsPageRendersShellNotes verifies the #167 observability contract: the
// desktop shell's diagnostics reach the Logs page, errors carry the failed
// badge, and browser mode (no provider) renders no shell section.
func TestLogsPageRendersShellNotes(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := get(t, srv, "/logs").Body.String()
	if contains(body, "Desktop shell") {
		t.Error("browser mode (no provider) must not render the Desktop shell section")
	}

	when := time.Date(2026, 7, 4, 9, 30, 5, 0, time.UTC)
	srv.SetShellNotes(func() []ShellNote {
		return []ShellNote{
			{Time: when, Level: ShellNoteInfo, Message: "menubar: status item registered"},
			{Time: when.Add(time.Second), Level: ShellNoteError, Message: "menubar: status item did not register"},
		}
	})
	body = get(t, srv, "/logs").Body.String()
	if !contains(body, "Desktop shell") {
		t.Error("Logs page missing the Desktop shell section with a provider wired")
	}
	if !contains(body, "menubar: status item registered") {
		t.Error("Logs page missing the info note")
	}
	if !contains(body, "menubar: status item did not register") {
		t.Error("Logs page missing the error note")
	}
	if !contains(body, "09:30:05") {
		t.Error("Logs page should render note clock times")
	}
	// Errors are conveyed as a text badge (never color alone).
	if !contains(body, `log-badge log-badge-failed">error</span>`) {
		t.Error("error notes should carry the failed text badge")
	}
}

// TestLogsEntriesFragmentSkipsShellNotes pins the fragment contract: the
// self-polling entries swap replaces only the per-source job panels, so the
// shell section (rendered outside logs_entries) is never duplicated or
// clobbered by a poll.
func TestLogsEntriesFragmentSkipsShellNotes(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.SetShellNotes(func() []ShellNote {
		return []ShellNote{{Time: time.Now(), Level: ShellNoteInfo, Message: "menubar: status item registered"}}
	})
	frag := get(t, srv, "/logs?fragment=entries").Body.String()
	if contains(frag, "Desktop shell") {
		t.Error("the logs_entries fragment must not include the Desktop shell section")
	}
}
