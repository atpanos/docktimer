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

  // Pause the container auto-refresh while a modal-ish interaction is open.
  document.addEventListener("htmx:beforeRequest", function (event) {
    var elt = event.detail && event.detail.elt;
    if (elt && elt.matches && elt.matches('button[hx-post*="/containers/"]')) {
      elt.setAttribute("aria-busy", "true");
    }
  });

  document.addEventListener("htmx:afterRequest", function (event) {
    var elt = event.detail && event.detail.elt;
    if (elt && elt.removeAttribute) elt.removeAttribute("aria-busy");
  });
})();
