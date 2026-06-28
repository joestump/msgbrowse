// Live "Filter conversations" box for the msgbrowse sidebar. Self-hosted (served
// from /static under script-src 'self') so it runs under the strict CSP — no
// inline handlers. As the user types, it shows only the conversation rows whose
// (humanized) name contains the query, case-insensitively, and reveals a small
// empty-state line when nothing matches (SPEC-0006 REQ-0006-003).
(function () {
  "use strict";

  function init() {
    var input = document.getElementById("sidebar-filter");
    var list = document.getElementById("sidebar-conversations");
    if (!input || !list) return;

    var empty = document.querySelector(".sidebar-empty");
    var items = Array.prototype.slice.call(list.querySelectorAll(".conv-item"));

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
    }

    input.addEventListener("input", apply);
    apply();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
