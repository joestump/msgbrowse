// Theme toggle for msgbrowse. Self-hosted (served from /static under
// script-src 'self') so it runs under the strict CSP. Switches the daisyUI
// theme between "slate" (default, dark) and "slate-light" (derived light) and
// persists the choice in localStorage. Loaded in <head> (not deferred) so the
// saved theme is applied before first paint — no flash of the default theme
// (SPEC-0006 REQ-0006-001).
(function () {
  "use strict";
  var KEY = "msgbrowse-theme";
  var DARK = "slate";
  var LIGHT = "slate-light";

  var saved;
  try {
    saved = localStorage.getItem(KEY);
  } catch (e) {
    saved = null;
  }
  if (saved === DARK || saved === LIGHT) {
    document.documentElement.setAttribute("data-theme", saved);
  }

  // Event delegation on document works even though this runs before <body> is
  // parsed: the listener is attached to the document, and the toggle button is
  // matched at click time.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest("[data-theme-toggle]");
    if (!btn) return;
    var current = document.documentElement.getAttribute("data-theme") || DARK;
    var next = current === DARK ? LIGHT : DARK;
    document.documentElement.setAttribute("data-theme", next);
    try {
      localStorage.setItem(KEY, next);
    } catch (e) {
      /* ignore storage failures (private mode) */
    }
  });
})();
