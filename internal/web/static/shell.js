// App-shell behaviors for the #190 full-width-header layout. Self-hosted
// (served from /static under script-src 'self') so it runs under the strict
// CSP — no inline handlers.
//
// 1. Header-tab active state. The Messages/Media tabs live in the page_start
//    shell, which boosted partial renders never re-render — the server marks
//    the active tab (.NavTab) only on full loads. syncTabs() re-derives the
//    state from location.pathname after every boosted swap / history move,
//    exactly like sidebar.js's syncActive does for conversation rows:
//      - "/" and every /c/* transcript read as Messages;
//      - /media (the tab's URL) and /gallery* (the canonical media surface it
//        aliases — gallery tab/filter links stay on /gallery) read as Media;
//      - every other route (Search, Settings, …) activates neither.
//    Keep in lockstep with baseData.NavTab (internal/web/handlers.go).
//
// 2. Infinite-scroll keep-alive. htmx 2.0.4 re-checks hx-trigger="revealed"
//    sentinels only after a *window* scroll/resize event sets its dirty flag,
//    but the #190 shell moves all scrolling off the window into inner
//    containers (#main-content, the sidebar list) whose scroll events do not
//    bubble. The capture-phase listener below (capture, because scroll does
//    not bubble) forwards inner scrolls as a synthetic window scroll so the
//    transcript/gallery load-more sentinels keep firing. Document scrolls are
//    excluded: those already reach window natively, and the synthetic event —
//    dispatched ON window, so it never re-enters this document-level capture
//    listener — must not double-fire for them.
(function () {
  "use strict";

  function syncTabs(name) {
    var tabs = document.querySelectorAll(".header-tabs [data-nav-tab]");
    for (var i = 0; i < tabs.length; i++) {
      var tab = tabs[i];
      var on = tab.getAttribute("data-nav-tab") === name;
      tab.classList.toggle("header-tab-active", on);
      if (on) {
        tab.setAttribute("aria-current", "page");
      } else {
        tab.removeAttribute("aria-current");
      }
    }
  }

  function sync() {
    var path = window.location.pathname;
    var name = "";
    if (path === "/" || path.indexOf("/c/") === 0) {
      name = "messages";
    } else if (path === "/media" || path.indexOf("/gallery") === 0) {
      name = "media";
    }
    syncTabs(name);
  }

  // Same event set sidebar.js uses for its active-row sync: after every
  // boosted swap settles (hx-push-url has updated location by then), on
  // back/forward, and after htmx rebuilds the body from a history snapshot.
  document.addEventListener("htmx:afterSettle", sync);
  window.addEventListener("popstate", sync);
  document.addEventListener("htmx:historyRestore", sync);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", sync);
  } else {
    sync();
  }

  document.addEventListener(
    "scroll",
    function (e) {
      if (e.target !== document) window.dispatchEvent(new Event("scroll"));
    },
    true
  );
})();
