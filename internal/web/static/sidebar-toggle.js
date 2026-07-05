// Sidebar collapse toggle (issue #175). Self-hosted (served from /static under
// script-src 'self') so it runs under the strict CSP — no inline handlers. The
// toolbar burger (the #nav-drawer <label>) now works at every width: below lg
// the native label→checkbox toggle still drives the daisyUI overlay drawer; at
// lg+ (where lg:drawer-open pins the sidebar visible regardless of the
// checkbox) the click instead flips a persistent .sidebar-collapsed class that
// hides the sidebar column (see input.css).
//
// Keyboard: a <label> has no native focus or key activation, so the template
// gives the burger role="button" + tabindex="0" and the keydown handler below
// maps Enter/Space to a synthetic click (which drives the checkbox below lg
// and the collapse branch at lg+). aria-expanded is kept in sync with
// whichever mechanism currently owns the sidebar.
//
// State lives on <html> and the listeners on document, so both survive boosted
// #main-content swaps AND htmx:historyRestore (which replaces body.innerHTML
// — the <html> class and document-level delegation are untouched), with no
// re-init hook needed. Loaded in <head> WITHOUT defer, like theme.js, so the
// saved collapsed state is applied before first paint — no sidebar flash.
(function () {
  "use strict";
  var KEY = "msgbrowse-sidebar-collapsed";
  var CLASS = "sidebar-collapsed";
  // Scoped to the toolbar burger: the drawer-overlay close label targets the
  // same checkbox but must never flip the collapsed state.
  var BURGER = '.app-toolbar label[for="nav-drawer"]';
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

  // aria-expanded mirrors whichever mechanism currently controls the sidebar:
  // the drawer checkbox below lg, the persistent collapsed class at lg+.
  function syncExpanded() {
    var burger = document.querySelector(BURGER);
    if (!burger) return;
    var expanded;
    if (wide.matches) {
      expanded = !document.documentElement.classList.contains(CLASS);
    } else {
      var box = document.getElementById("nav-drawer");
      expanded = !!(box && box.checked);
    }
    burger.setAttribute("aria-expanded", expanded ? "true" : "false");
  }

  // Document-level delegation works even though this runs before <body> is
  // parsed (the burger is matched at click time), and keeps working across
  // history restores.
  document.addEventListener("click", function (e) {
    var burger = e.target.closest && e.target.closest(BURGER);
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
    syncExpanded();
  });

  // Enter/Space on the focused burger behave like a click (see the keyboard
  // note in the header comment). preventDefault keeps Space from scrolling.
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Enter" && e.key !== " ") return;
    var burger = e.target.closest && e.target.closest(BURGER);
    if (!burger) return;
    e.preventDefault();
    burger.click();
  });

  // Below lg the native label→checkbox toggle (and the drawer-overlay close
  // label) change drawer state without going through the handlers above.
  document.addEventListener("change", function (e) {
    if (e.target && e.target.id === "nav-drawer") syncExpanded();
  });

  // First sync once the burger exists (this script runs before <body> parses).
  document.addEventListener("DOMContentLoaded", syncExpanded);
  // Crossing the breakpoint switches which mechanism aria-expanded mirrors.
  if (wide.addEventListener) {
    wide.addEventListener("change", syncExpanded);
  }
  // htmx:historyRestore replaces body.innerHTML from a snapshot that may carry
  // a stale aria-expanded value.
  document.addEventListener("htmx:historyRestore", syncExpanded);
})();
