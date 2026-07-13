/* Dashboard */
import { state } from "./state.js";
import { $, esc, toast, fmtBytes } from "./util.js";
import { api, handleAuthError } from "./api.js";
import { setAuthed, stopPoll, wrapPage, pageHd } from "./shell.js";

function skeletonStats(n) {
  var html = "";
  for (var i = 0; i < n; i++) {
    html += '<div class="stat-cell"><div class="stat-label">—</div><div class="stat-value is-skeleton">0</div></div>';
  }
  return html;
}

function setStatGrid(items) {
  var host = $("kpis");
  if (!host) return;
  if (host.children.length !== items.length) {
    host.innerHTML = items.map(function (it) {
      return '<div class="stat-cell"><div class="stat-label">' + esc(it[0]) +
        '</div><div class="stat-value" data-k="' + esc(it[0]) + '">' + esc(String(it[1])) +
        "</div></div>";
    }).join("");
    return;
  }
  for (var i = 0; i < items.length; i++) {
    var cell = host.children[i];
    if (!cell) continue;
    var lab = cell.querySelector(".stat-label");
    var val = cell.querySelector(".stat-value");
    if (lab) lab.textContent = items[i][0];
    if (val) {
      val.classList.remove("is-skeleton");
      val.textContent = String(items[i][1]);
    }
  }
}

function loadDash(force) {
  if (document.visibilityState === "hidden" && !force) return;
  api("/admin/pool/stats").then(function (s) {
    setStatGrid([
      ["请求总数", s.requests_total != null ? s.requests_total : 0],
      ["热池", (s.pool_hot_size != null ? s.pool_hot_size : 0) + " / " + (s.hot_cap != null ? s.hot_cap : "—")],
      ["冷却", s.pool_cooldown_size != null ? s.pool_cooldown_size : 0],
      ["Inflight", s.proxy_inflight != null ? s.proxy_inflight : 0],
      ["拒绝 503", s.proxy_reject_total != null ? s.proxy_reject_total : 0],
      ["RSS", fmtBytes(s.process_rss_bytes)],
      ["令牌启用", (s.tokens_enabled != null ? s.tokens_enabled : 0) + " / " + (s.tokens_total != null ? s.tokens_total : 0)],
      ["额度耗尽", s.tokens_exhausted != null ? s.tokens_exhausted : 0]
    ]);
    var dm = $("dashMeta");
    if (dm) {
      dm.textContent = "uptime " + Math.round(s.uptime_seconds || 0) + "s · " +
        (s.listen || "") + " · " + (s.version || "");
    }
    var ver = $("hdVersion");
    if (ver && s.version) ver.textContent = String(s.version).indexOf("v") === 0 ? s.version : ("v" + s.version);
    var de = $("dashErr");
    if (de) de.innerHTML = "";
  }).catch(function (e) {
    if (handleAuthError(e)) return;
    var de = $("dashErr");
    if (de) de.innerHTML = '<div class="err-box">加载失败：' + esc(e.message) + "</div>";
    toast(e.message, false);
  });
}

export function renderDashboard() {
  setAuthed(true);
  if (!state.dashBuilt || !$("kpis")) {
    state.dashBuilt = true;
    $("main").innerHTML = wrapPage(
      pageHd("仪表盘", "运行态与池容量一览",
        '<button type="button" class="page-action-btn" id="dashRefresh">刷新</button>') +
      '<div id="dashErr"></div>' +
      '<div id="kpis" class="stat-grid">' + skeletonStats(8) + "</div>" +
      '<p class="muted dashboard-meta" id="dashMeta"></p>'
    );
    $("dashRefresh").addEventListener("click", function () { loadDash(true); });
  }
  loadDash(false);
  stopPoll();
  state.pollTimer = setInterval(function () { loadDash(false); }, 5000);
}
