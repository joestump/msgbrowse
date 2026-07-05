// Sidebar collapse toggle (issue #175). Self-hosted (served from /static under
// script-src 'self') so it runs under the strict CSP — no inline handlers. The
// toolbar burger (the #nav-drawer <label>) now works at every width: below lg
// the native label→checkbox toggle still drives the daisyUI overlay drawer; at
// lg+ (where lg:drawer-open pins the sidebar visible regardless of the
// checkbox) the click instead flips a persistent .sidebar-collapsed class that
// hides the sidebar column (see input.css).
//
// State lives on <html> and the listener on document, so both survive boosted
// #main-content swaps AND htmx:historyRestore (which replaces body.innerHTML
// — the <html> class and document-level delegation are untouched), with no
// re-init hook needed. Loaded in <head> WITHOUT defer, like theme.js, so the
// saved collapsed state is applied before first paint — no sidebar flash.
(function () {
  "use strict";
  var KEY = "msgbrowse-sidebar-collapsed";
  var CLASS = "sidebar-collapsed";
  // Tailwind's lg breakpoint — keep in lockstep with the template's
  // lg:drawer-open and input.css's 64rem media queries.
  var wide = window.matchMedia("(min-width: 64rem)");

  try {
    if (localStorage.getItem(KEY) === "1") {
      document.documentElement.classList.add(CLASS);
    }
  } catch (e) {
    /* ignore storage failures (private mode) */
  }

  // Document-level delegation works even though this runs before <body> is
  // parsed (the burger is matched at click time), and keeps working across
  // history restores.
  document.addEventListener("click", function (e) {
    // Scoped to the toolbar burger: the drawer-overlay close label targets the
    // same checkbox but must never flip the collapsed state.
    var burger = e.target.closest && e.target.closest('.app-toolbar label[for="nav-drawer"]');
    if (!burger || !wide.matches) return;
    // At lg+ the checkbox is inert (lg:drawer-open) — suppress the label's
    // native toggle and flip the persistent collapsed class instead. Below lg
    // the early return above leaves the overlay-drawer checkbox path intact.
    e.preventDefault();
    var collapsed = document.documentElement.classList.toggle(CLASS);
    try {
      localStorage.setItem(KEY, collapsed ? "1" : "0");
    } catch (e2) {
      /* ignore storage failures (private mode) */
    }
  });
})();
