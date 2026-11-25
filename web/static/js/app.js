// Docktimer frontend helpers: CSRF header injection for HTMX, toast messages
// and a small confirm handler for destructive plain forms.
(function () {
  "use strict";

  function csrfToken() {
    var meta = document.querySelector('meta[name="csrf-token"]');
    return meta ? meta.getAttribute("content") : "";
  }

  document.addEventListener("htmx:configRequest", function (event) {
    var token = csrfToken();
    if (token && event.detail && event.detail.headers) {
      event.detail.headers["X-CSRF-Token"] = token;
    }
  });

  function toast(message, type) {
    var root = document.getElementById("toast-root");
    if (!root || !message) return;
    var el = document.createElement("div");
    el.className = "toast toast-" + (type === "error" ? "error" : "success");
    el.setAttribute("role", "status");
    el.textContent = message;
    root.appendChild(el);
    window.setTimeout(function () {
      el.style.opacity = "0";
      el.style.transition = "opacity 200ms ease";
      window.setTimeout(function () { el.remove(); }, 220);
    }, 5000);
  }

  document.body.addEventListener("toast", function (event) {
    var detail = event.detail || {};
    toast(detail.message, detail.type);
  });

  document.addEventListener("submit", function (event) {
    var form = event.target;
    if (!form || !form.dataset || !form.dataset.confirm) return;
    if (!window.confirm(form.dataset.confirm)) {
      event.preventDefault();
    }
  });

  // Track in-flight container actions so the 10-second table poll cannot
  // replace the table (and the action's response target) before it finishes.
  var pendingActions = 0;

  function isActionButton(elt) {
    return elt && elt.matches && elt.matches('button[hx-post*="/containers/"]');
  }

  document.addEventListener("htmx:beforeRequest", function (event) {
    var elt = event.detail && event.detail.elt;
    if (isActionButton(elt)) {
      pendingActions += 1;
      elt.setAttribute("aria-busy", "true");
      elt.dataset.dtPending = "1";
      return;
    }
    // Do not start a poll while a container action is still running.
    if (elt && elt.id === "container-table" && pendingActions > 0) {
      event.preventDefault();
    }
  });

  document.addEventListener("htmx:beforeSwap", function (event) {
    var detail = event.detail || {};
    var path = detail.pathInfo && detail.pathInfo.requestPath;
    // On htmx:beforeSwap, detail.elt is the swap target (the table), not the
    // request source. Suppress only the in-flight poll response; the action
    // response must still update the table and deliver its toast.
    if (path === "/containers/table" && pendingActions > 0) {
      detail.shouldSwap = false;
    }
  });

  document.addEventListener("htmx:afterRequest", function (event) {
    var elt = event.detail && event.detail.elt;
    if (!elt || !elt.dataset || elt.dataset.dtPending !== "1") return;
    delete elt.dataset.dtPending;
    elt.removeAttribute("aria-busy");
    pendingActions = Math.max(0, pendingActions - 1);
  });
})();
