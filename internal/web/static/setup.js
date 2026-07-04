// Permission-guidance modal behavior for the Setup page (SPEC-0013 REQ
// "Permission detection and guidance", §Accessibility "Focus management for
// guidance modals"). Self-hosted (served from /static under script-src 'self')
// so it runs under the strict CSP — no inline handlers.
//
// Responsibilities:
//   - Open the guidance dialog when a card's "How to grant access" trigger is
//     clicked (data-setup-guide-open="<dialog id>"), move focus into it, and
//     remember the trigger so focus can be RESTORED to it on close.
//   - Trap Tab/Shift+Tab focus within the open dialog; close on Escape or a
//     [data-setup-guide-close] control (the X button or the backdrop).
//   - After a Recheck swaps the card, announce the new state into the dialog's
//     aria-live result region — or, when the card left Needs-permission (the
//     dialog is gone), leave focus on the now-updated card so the change is
//     perceivable.
//
// One document-level delegated listener set, so it keeps working across htmx
// boosted swaps, card recheck swaps, and history restores without re-init.
(function () {
  "use strict";

  // The trigger element that opened the currently-open dialog, so focus can be
  // restored to it on close (SPEC-0013 §Accessibility "return to the triggering
  // control").
  var lastTrigger = null;

  // Focusable-descendant selector for the focus trap. Kept conservative: the
  // dialog only contains buttons and a link.
  var FOCUSABLE =
    'a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])';

  function openDialog(dialog, trigger) {
    if (!dialog || !dialog.hidden) return;
    lastTrigger = trigger || null;
    dialog.hidden = false;
    dialog.classList.add("open");
    // Move focus into the dialog — the close button is a stable first stop.
    var focusables = dialog.querySelectorAll(FOCUSABLE);
    if (focusables.length) {
      focusables[0].focus();
    } else {
      dialog.focus();
    }
  }

  function closeDialog(dialog, restoreFocus) {
    if (!dialog || dialog.hidden) return;
    dialog.classList.remove("open");
    dialog.hidden = true;
    if (restoreFocus !== false && lastTrigger && document.contains(lastTrigger)) {
      lastTrigger.focus();
    }
    lastTrigger = null;
  }

  function openDialogById(id, trigger) {
    var dialog = document.getElementById(id);
    openDialog(dialog, trigger);
  }

  // The single currently-open dialog, if any.
  function openDialogEl() {
    return document.querySelector("[data-setup-guide].open");
  }

  // Trap Tab within the open dialog: wrap from last→first and first→last.
  function trapTab(e, dialog) {
    var focusables = Array.prototype.filter.call(
      dialog.querySelectorAll(FOCUSABLE),
      function (el) {
        return el.offsetParent !== null || el === document.activeElement;
      }
    );
    if (!focusables.length) {
      e.preventDefault();
      dialog.focus();
      return;
    }
    var first = focusables[0];
    var last = focusables[focusables.length - 1];
    var active = document.activeElement;
    if (e.shiftKey) {
      if (active === first || !dialog.contains(active)) {
        e.preventDefault();
        last.focus();
      }
    } else {
      if (active === last || !dialog.contains(active)) {
        e.preventDefault();
        first.focus();
      }
    }
  }

  // Open triggers.
  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!t || !t.closest) return;

    var opener = t.closest("[data-setup-guide-open]");
    if (opener) {
      openDialogById(opener.getAttribute("data-setup-guide-open"), opener);
      return;
    }

    var closer = t.closest("[data-setup-guide-close]");
    if (closer) {
      var dialog = closer.closest("[data-setup-guide]");
      closeDialog(dialog, true);
      return;
    }
  });

  // Escape closes; Tab is trapped — only while a dialog is open.
  document.addEventListener("keydown", function (e) {
    var dialog = openDialogEl();
    if (!dialog) return;
    if (e.key === "Escape") {
      e.preventDefault();
      closeDialog(dialog, true);
    } else if (e.key === "Tab") {
      trapTab(e, dialog);
    }
  });

  // After a Recheck swaps the card <li> (htmx outerHTML swap of
  // #setup-card-<source>), report the outcome. Two cases:
  //   - The card is STILL Needs-permission: the (new) dialog stays closed, but
  //     the grant is not yet present — announce that in the fresh dialog's live
  //     region and reopen it so the user can retry, keeping the guidance visible.
  //   - The card LEFT Needs-permission (Ready/Enabled): the dialog is gone;
  //     move focus to the updated card so the state change is perceivable, and
  //     no dialog announcement is needed (the card's aria-label carries the new
  //     state).
  document.addEventListener("htmx:afterSwap", function (e) {
    var target = e.target;
    if (!target || !target.id || target.id.indexOf("setup-card-") !== 0) return;

    var stillBlocked = target.classList.contains("setup-card-needs-permission");
    if (stillBlocked) {
      var dialog = target.querySelector("[data-setup-guide]");
      var result = target.querySelector(".setup-guide-result");
      if (result) {
        result.textContent = "";
        result.textContent =
          "Still waiting on permission. Grant access, then Recheck again.";
      }
      // Reopen the (freshly rendered) dialog so the guidance and the live message
      // stay visible; the recheck button lives inside it.
      if (dialog) openDialog(dialog, lastTrigger);
    } else {
      // Grant is present now — the card flipped out of Needs-permission. Drop the
      // stale trigger reference and land focus on the updated card.
      lastTrigger = null;
      if (target.setAttribute) target.setAttribute("tabindex", "-1");
      if (target.focus) target.focus();
    }
  });
})();
