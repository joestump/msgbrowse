// Live "Filter conversations" box for the msgbrowse sidebar. Self-hosted (served
// from /static under script-src 'self') so it runs under the strict CSP — no
// inline handlers. As the user types, it shows only the conversation rows whose
// (humanized) name contains the query, case-insensitively, across BOTH the
// PINNED and CONVERSATIONS sections, reveals a small empty-state line when
// nothing matches, and hides the PINNED group header when none of its rows match
// (SPEC-0006 REQ-0006-003 / REQ-0006-010).
(function () {
  "use strict";

  function initFilter() {
    var input = document.getElementById("sidebar-filter");
    if (!input) return;

    // Every conversation row in either section carries .conv-item + data-name.
    var items = Array.prototype.slice.call(document.querySelectorAll(".conv-item"));
    if (!items.length) return;

    var empty = document.querySelector(".sidebar-empty");
    var pinnedUl = document.getElementById("sidebar-pinned");
    var pinnedHead = pinnedUl ? pinnedUl.previousElementSibling : null;

    function anyVisible(ul) {
      if (!ul) return false;
      var rows = ul.querySelectorAll(".conv-item");
      for (var i = 0; i < rows.length; i++) {
        if (!rows[i].hidden) return true;
      }
      return false;
    }

    function apply() {
      var q = input.value.trim().toLowerCase();
      var shown = 0;
      for (var i = 0; i < items.length; i++) {
        var name = (items[i].getAttribute("data-name") || "").toLowerCase();
        var match = q === "" || name.indexOf(q) !== -1;
        items[i].hidden = !match;
        if (match) shown++;
      }
      if (empty) empty.hidden = shown !== 0;
      // Drop the PINNED header + list entirely when nothing in it matches, so a
      // filtered-out section doesn't leave a dangling "Pinned" label.
      if (pinnedUl && pinnedHead) {
        var visible = anyVisible(pinnedUl);
        pinnedUl.hidden = !visible;
        pinnedHead.hidden = !visible;
      }
    }

    input.addEventListener("input", apply);
    apply();
  }

  // Keep the active-conversation highlight correct. A boosted navigation swaps
  // only #main-content, leaving the sidebar DOM (including the server-rendered
  // .conv-row-selected) untouched — so move the highlight to the row whose href
  // matches the current path. Prefix-matching keeps the highlight on
  // jump-to-context pages (/c/{id}/at/{mid}) whose path extends the row's href.
  // Runs on initial load, after every HTMX settle (boosted nav), and on
  // browser back/forward.
  function syncActive() {
    var path = window.location.pathname;
    var rows = document.querySelectorAll("a.conv-row");
    for (var i = 0; i < rows.length; i++) {
      var href = rows[i].getAttribute("href");
      rows[i].classList.toggle(
        "conv-row-selected",
        href === path || (href !== "/" && path.indexOf(href + "/") === 0)
      );
    }
  }

  function init() {
    initFilter();
    syncActive();
  }

  // After a boosted swap settles: re-sync the active row and close the mobile
  // drawer — the #nav-drawer checkbox lives outside #main-content, so unlike a
  // full page load nothing else resets it and the overlay would stay open.
  document.addEventListener("htmx:afterSettle", function () {
    syncActive();
    var drawer = document.getElementById("nav-drawer");
    if (drawer) drawer.checked = false;
  });
  window.addEventListener("popstate", syncActive);

  // htmx restores history snapshots by replacing body.innerHTML, which drops
  // the element-bound filter listener and stales the captured row list — fully
  // re-init against the restored DOM (also resets any snapshotted hidden rows,
  // since the input's typed value is not serialized into the snapshot).
  document.addEventListener("htmx:historyRestore", init);

  // A failed boosted request swaps nothing (htmx's default for 4xx/5xx), which
  // would turn an error page into a dead click — fall back to full navigation
  // so the error stays visible.
  document.addEventListener("htmx:responseError", function (e) {
    var path = e.detail && e.detail.requestConfig && e.detail.requestConfig.path;
    if (path) window.location.href = path;
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
