/* DOM / format / toast helpers */
import { state } from "./state.js";

export function $(id) { return document.getElementById(id); }

export function esc(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

/** 轻量 toast；ok=true 成功边框，否则错误边框 */
export function toast(msg, ok) {
  var host = $("toastHost");
  if (!host) return;
  var el = $("toast");
  if (!el) {
    el = document.createElement("div");
    el.id = "toast";
    el.className = "toast hidden";
    el.setAttribute("role", "status");
    host.appendChild(el);
  }
  if (state.toastTimer) clearTimeout(state.toastTimer);
  el.textContent = msg == null ? "" : String(msg);
  el.classList.remove("hidden", "ok", "bad", "is-leaving");
  void el.offsetWidth;
  el.classList.add(ok ? "ok" : "bad");
  state.toastTimer = setTimeout(function () {
    el.classList.add("is-leaving");
    setTimeout(function () {
      el.classList.add("hidden");
      el.classList.remove("is-leaving");
    }, prefersReducedMotion() ? 0 : 180);
  }, 3000);
}

export function prefersReducedMotion() {
  try {
    return !!(window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
  } catch (_) {
    return false;
  }
}

export function fmtBytes(n) {
  if (n == null || n === "") return "—";
  var u = ["B", "KB", "MB", "GB", "TB"];
  var i = 0;
  n = Number(n);
  if (!isFinite(n) || n < 0) return "—";
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(i ? 1 : 0) + " " + u[i];
}

export function fmtCooldown(until) {
  until = Number(until) || 0;
  if (until <= 0) return "—";
  var now = Math.floor(Date.now() / 1000);
  if (until <= now) return "已过期";
  var left = until - now;
  if (left < 60) return left + "s";
  if (left < 3600) return Math.ceil(left / 60) + "m";
  return Math.ceil(left / 3600) + "h";
}

export function copyText(text) {
  if (!text) return Promise.reject(new Error("空内容"));
  if (navigator.clipboard && navigator.clipboard.writeText) {
    return navigator.clipboard.writeText(text);
  }
  return new Promise(function (resolve, reject) {
    var ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.left = "-9999px";
    document.body.appendChild(ta);
    ta.select();
    try {
      if (document.execCommand("copy")) resolve();
      else reject(new Error("copy failed"));
    } catch (e) {
      reject(e);
    } finally {
      document.body.removeChild(ta);
    }
  });
}

/** 列表首帧骨架，减轻 enter 后二次弹出感 */
export function skeletonRows(n, label) {
  n = n || 5;
  var rows = "";
  for (var i = 0; i < n; i++) {
    rows += '<div class="skeleton-row"><div class="skeleton sk-line"></div></div>';
  }
  return '<div class="skeleton-list" aria-busy="true" aria-label="' + esc(label || "加载中") + '">' +
    rows + "</div>";
}
