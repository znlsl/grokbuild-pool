/* Shell: theme, mobile menu, page chrome, route swap */
import { state } from "./state.js";
import { $, prefersReducedMotion } from "./util.js";

export function themeInit() {
  var saved = null;
  try {
    saved = localStorage.getItem("pool-admin-theme");
  } catch (_) { /* 隐私模式等 */ }
  if (saved !== "light" && saved !== "dark") {
    saved = "light";
  }
  document.documentElement.setAttribute("data-theme", saved);
  syncThemeBtn();
}

export function toggleTheme() {
  var cur = document.documentElement.getAttribute("data-theme") === "light" ? "dark" : "light";
  document.documentElement.setAttribute("data-theme", cur);
  try {
    localStorage.setItem("pool-admin-theme", cur);
  } catch (_) { /* ignore */ }
  syncThemeBtn();
}

export function syncThemeBtn() {
  var btn = $("themeToggle");
  if (!btn) return;
  var light = document.documentElement.getAttribute("data-theme") !== "dark";
  btn.textContent = light ? "深色" : "浅色";
  btn.title = light ? "切换到深色" : "切换到浅色";
}

function mobileMenuIsOpen() {
  var drawer = $("mobileDrawer");
  return !!(drawer && drawer.classList.contains("is-open"));
}

function mobileMenuFocusable() {
  var drawer = $("mobileDrawer");
  if (!drawer) return [];
  return Array.prototype.slice.call(
    drawer.querySelectorAll('a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])')
  );
}

export function setMobileMenu(open, restoreFocus) {
  var btn = $("menuBtn");
  var drawer = $("mobileDrawer");
  var overlay = $("mobileOverlay");
  if (!btn || !drawer || !overlay) return;

  open = !!open && !!state.adminKey && window.innerWidth < 768;
  btn.setAttribute("aria-expanded", open ? "true" : "false");
  drawer.setAttribute("aria-hidden", open ? "false" : "true");
  drawer.classList.toggle("is-open", open);
  overlay.hidden = !open;
  overlay.classList.toggle("hidden", !open);
  document.body.classList.toggle("nav-open", open);
  if (open) drawer.removeAttribute("inert");
  else drawer.setAttribute("inert", "");

  if (open) {
    window.requestAnimationFrame(function () {
      var current = drawer.querySelector('[aria-current="page"]');
      var target = current || $("menuClose") || mobileMenuFocusable()[0];
      if (target) target.focus();
    });
  } else if (restoreFocus && !btn.classList.contains("hidden")) {
    btn.focus();
  }
}

export function bindMobileMenu() {
  var btn = $("menuBtn");
  var close = $("menuClose");
  var overlay = $("mobileOverlay");
  var nav = $("mobileNav");
  if (!btn || !close || !overlay || !nav) return;

  btn.addEventListener("click", function () { setMobileMenu(true, false); });
  close.addEventListener("click", function () { setMobileMenu(false, true); });
  overlay.addEventListener("click", function () { setMobileMenu(false, true); });
  nav.addEventListener("click", function (ev) {
    if (ev.target.closest("a[href]")) setMobileMenu(false, false);
  });
  document.addEventListener("keydown", function (ev) {
    if (!mobileMenuIsOpen()) return;
    if (ev.key === "Escape") {
      ev.preventDefault();
      setMobileMenu(false, true);
      return;
    }
    if (ev.key !== "Tab") return;
    var focusable = mobileMenuFocusable();
    if (!focusable.length) return;
    var first = focusable[0];
    var last = focusable[focusable.length - 1];
    if (focusable.indexOf(document.activeElement) === -1) {
      ev.preventDefault();
      first.focus();
    } else if (ev.shiftKey && document.activeElement === first) {
      ev.preventDefault();
      last.focus();
    } else if (!ev.shiftKey && document.activeElement === last) {
      ev.preventDefault();
      first.focus();
    }
  });
  window.addEventListener("resize", function () {
    if (window.innerWidth >= 768 && mobileMenuIsOpen()) setMobileMenu(false, false);
  });
}

export function setAuthed(on) {
  var nav = $("nav");
  var lo = $("logoutBtn");
  var menu = $("menuBtn");
  if (nav) {
    nav.classList.toggle("is-locked", !on);
    nav.classList.remove("hidden");
  }
  if (lo) lo.classList.toggle("hidden", !on);
  if (menu) menu.classList.toggle("hidden", !on);
  if (!on) setMobileMenu(false, false);
}

export function stopPoll() {
  if (state.pollTimer) {
    clearInterval(state.pollTimer);
    clearTimeout(state.pollTimer);
    state.pollTimer = null;
  }
}

export function wrapPage(html, animate) {
  if (animate == null) animate = !!state.pageAnimate;
  var enter = animate && !prefersReducedMotion() ? " page-enter" : "";
  return '<div class="page' + enter + '">' + html + "</div>";
}

export function pageHd(title, sub, actionsHtml) {
  return (
    '<div class="page-hd">' +
    "<div><div class=\"page-title\">" + title + "</div>" +
    (sub ? '<div class="page-sub">' + sub + "</div>" : "") +
    "</div>" +
    (actionsHtml ? '<div class="page-actions">' + actionsHtml + "</div>" : "") +
    "</div>"
  );
}

/**
 * 换页：短 leave → 写新 HTML → enter。
 * renderFn 应同步写入 main；generation token 防止连点叠动画。
 */
export function swapMain(renderFn, opts) {
  opts = opts || {};
  var animate = !!opts.animate && !prefersReducedMotion();
  var main = $("main");
  if (!main) {
    state.pageAnimate = false;
    renderFn();
    return;
  }

  state.routeGen += 1;
  var gen = state.routeGen;

  function commit() {
    if (gen !== state.routeGen) return;
    state.pageAnimate = animate;
    renderFn();
    state.pageAnimate = false;
    if (opts.resetScroll) {
      try { window.scrollTo(0, 0); } catch (_) { /* ignore */ }
      try { main.focus({ preventScroll: true }); } catch (_) {
        try { main.focus(); } catch (__) { /* ignore */ }
      }
    }
  }

  if (!animate) {
    commit();
    return;
  }

  var page = main.querySelector(".page");
  if (!page) {
    commit();
    return;
  }

  page.classList.remove("page-enter");
  page.classList.add("page-leave");
  var done = false;
  var finish = function () {
    if (done || gen !== state.routeGen) return;
    done = true;
    commit();
  };
  page.addEventListener("animationend", finish, { once: true });
  window.setTimeout(finish, 160);
}
