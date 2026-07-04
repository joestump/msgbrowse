// Desktop-shell drag region (SPEC-0010 "Native shell affordances", issue #165).
//
// With the macOS title bar hidden (mac.TitleBarHiddenInset in the shell), the
// unified toolbar is the window's drag surface. Wails' own drag protocol reads
// the --wails-draggable CSS custom property on mousedown and posts the "drag"
// message to the native side (performWindowDragWithEvent) — but the JS runtime
// that implements that reader is injected only into pages served through the
// Wails asset handler, and msgbrowse serves the real app over loopback HTTP
// (SPEC-0010 design decision), so those pages never receive it. This file is
// the missing reader: the same computed-style check and the same "drag"
// message over the webview's script-message bridge, self-hosted under the
// app's strict CSP (script-src 'self'; no inline JS).
//
// It is included by page_start ONLY when the shell marks the render as
// desktop-chrome (web.Server.SetDesktopChrome), and it additionally no-ops
// unless the <body> carries that class and a webview bridge exists — a plain
// browser tab can never trigger it.
(function () {
  "use strict";

  var install = function () {
    if (!document.body || !document.body.classList.contains("desktop-chrome")) {
      return;
    }

    // post delivers the drag message on whichever bridge the webview exposes:
    // window.WailsInvoke when the Wails runtime happens to be present, else
    // the raw WKWebView / WebKitGTK script-message handler the runtime itself
    // uses ("external" — registered by Wails for every page in the webview).
    var post = function (msg) {
      try {
        if (typeof window.WailsInvoke === "function") {
          window.WailsInvoke(msg);
          return;
        }
        if (
          window.webkit &&
          window.webkit.messageHandlers &&
          window.webkit.messageHandlers.external
        ) {
          window.webkit.messageHandlers.external.postMessage(msg);
        }
      } catch (err) {
        /* no webview bridge — plain browser; ignore */
      }
    };

    // Mirrors the Wails runtime's dragTest: primary button, single click, and
    // the computed --wails-draggable on the event target must be exactly
    // "drag" (interactive toolbar children set no-drag in input.css).
    //
    // Like the runtime's default (deferDragToMouseMove: true), the "drag"
    // message posts on the FIRST mousemove with the button held — not on
    // mousedown — so a plain click on empty toolbar space focuses the window
    // instead of entering a native drag session (adversarial-review fix).
    var armed = false;
    var disarm = function () {
      armed = false;
      window.removeEventListener("mousemove", onMove, true);
      window.removeEventListener("mouseup", disarm, true);
    };
    var onMove = function (e) {
      if (!armed || e.buttons !== 1) {
        disarm();
        return;
      }
      disarm();
      post("drag");
    };
    window.addEventListener("mousedown", function (e) {
      if (e.buttons !== 1 || e.detail !== 1) {
        return;
      }
      if (!(e.target instanceof Element)) {
        return;
      }
      var val = window
        .getComputedStyle(e.target)
        .getPropertyValue("--wails-draggable");
      if (!val || val.trim() !== "drag") {
        return;
      }
      armed = true;
      window.addEventListener("mousemove", onMove, true);
      window.addEventListener("mouseup", disarm, true);
    });
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", install);
  } else {
    install();
  }
})();
