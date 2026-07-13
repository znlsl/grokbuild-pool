/* Tokens page */
import { state } from "./state.js";
import { $, esc, toast, copyText, skeletonRows } from "./util.js";
import { api, handleAuthError } from "./api.js";
import { setAuthed, stopPoll, wrapPage, pageHd } from "./shell.js";

function extractPlainKeys(res) {
  var keys = [];
  var list = (res && res.tokens) || [];
  list.forEach(function (x) {
    if (!x) return;
    var k = x.api_key || x.plaintext;
    if (!k && x.token && typeof x.token === "object") {
      /* 嵌套 token 无明文 */
    }
    if (k) keys.push(String(k));
  });
  if (!keys.length && res) {
    if (res.api_key) keys.push(String(res.api_key));
    else if (res.plaintext) keys.push(String(res.plaintext));
  }
  return keys;
}

function bindOnceBox(keys) {
  var box = $("onceBox");
  if (!box) return;
  if (!keys.length) {
    box.innerHTML =
      '<div class="once-box"><p class="muted">已创建，但响应中未包含明文密钥。</p></div>';
    return;
  }
  var rows = keys.map(function (k, i) {
    return '<div class="once-row">' +
      '<div class="once-key mono">' + esc(k) + "</div>" +
      '<button type="button" class="btn btn-sm btn-secondary" data-copy-one="' + i + '">复制</button>' +
      "</div>";
  }).join("");
  box.innerHTML =
    '<div class="once-box">' +
    '<div class="once-head">' +
    '<p class="muted once-hint">明文密钥仅显示一次（共 ' + keys.length +
    " 把），请立即复制：</p>" +
    '<button type="button" class="btn sm primary" id="copyAllKeys">全部复制</button>' +
    "</div>" +
    '<div class="once-list">' + rows + "</div></div>";

  var all = $("copyAllKeys");
  if (all) {
    all.addEventListener("click", function () {
      copyText(keys.join("\n")).then(function () {
        toast("已复制 " + keys.length + " 把密钥", true);
      }).catch(function () {
        toast("复制失败，请手动选择", false);
      });
    });
  }
  box.querySelectorAll("[data-copy-one]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var i = parseInt(btn.getAttribute("data-copy-one"), 10);
      if (!keys[i]) return;
      copyText(keys[i]).then(function () {
        toast("已复制第 " + (i + 1) + " 把", true);
      }).catch(function () {
        toast("复制失败，请手动选择", false);
      });
    });
  });
}

function selectedTokenIds() {
  var ids = [];
  document.querySelectorAll("#tokTable input.tok-check:checked").forEach(function (cb) {
    var id = cb.getAttribute("data-id");
    if (id) ids.push(id);
  });
  return ids;
}

function selectedTokenKeys() {
  var keys = [];
  document.querySelectorAll("#tokTable input.tok-check:checked").forEach(function (cb) {
    var k = cb.getAttribute("data-key") || "";
    if (k) keys.push(k);
  });
  return keys;
}

function updateTokSelUI() {
  var n = selectedTokenIds().length;
  var label = $("tokSelCount");
  if (label) label.textContent = "已选 " + n;
  var any = document.querySelectorAll("#tokTable input.tok-check").length > 0;
  var sa = $("tokSelectAll");
  if (sa) sa.disabled = !any;
  var sn = $("tokSelectNone");
  if (sn) sn.disabled = n === 0;
  var cp = $("tokCopySelected");
  if (cp) cp.disabled = selectedTokenKeys().length === 0;
  var bd = $("tokBatchDelete");
  if (bd) bd.disabled = n === 0;
}

function loadTokens() {
  var host = $("tokTable");
  var cnt = $("tokCount");
  if (!host) return;
  api("/admin/tokens").then(function (res) {
    var list = res.tokens || [];
    if (cnt) cnt.textContent = String(list.length);
    if (!list.length) {
      host.innerHTML =
        '<div class="empty"><strong>暂无令牌</strong>' +
        "使用上方表单快速创建；创建后可展开详情查看密钥。</div>";
      updateTokSelUI();
      return;
    }
    var rows = list.map(function (t) {
      var status = t.enabled
        ? '<span class="badge on">启用</span>'
        : '<span class="badge off">禁用</span>';
      var quota = t.unlimited_quota ? "∞" : String(t.remain_quota != null ? t.remain_quota : 0);
      var key = t.api_key || t.plaintext || "";
      var detail = key
        ? '<div class="tok-detail mono">' + esc(key) +
          ' <button type="button" class="btn btn-sm btn-secondary" data-act="copy" data-key="' +
          esc(key) + '">复制</button></div>'
        : '<div class="tok-detail muted">密钥明文仅在创建时返回一次；列表不再回读明文。</div>';
      return '<tr class="tok-row" data-id="' + esc(t.id) + '">' +
        '<td><input type="checkbox" class="tok-check" data-id="' + esc(t.id) +
          '" data-key="' + esc(key) + '" /></td>' +
        '<td><button type="button" class="btn btn-sm btn-ghost tok-expand" data-act="expand" aria-expanded="false">▸</button></td>' +
        '<td class="mono">' + esc(t.id) + "</td>" +
        "<td>" + esc(t.name) + "</td>" +
        '<td class="mono">' + esc(t.key_prefix) + "</td>" +
        "<td>" + status + "</td>" +
        "<td>" + esc(quota) + "</td>" +
        "<td>" + esc(t.max_concurrent ? String(t.max_concurrent) : "—") + "</td>" +
        "<td>" + esc(t.rpm ? String(t.rpm) : "—") + "</td>" +
        "<td>" + esc(String(t.used_quota || 0)) + " / " +
          esc(String(t.request_count || 0)) + "</td>" +
        '<td class="actions">' +
        (t.enabled
          ? '<button type="button" class="btn btn-sm btn-secondary" data-act="dis" data-id="' +
            esc(t.id) + '">禁用</button>'
          : '<button type="button" class="btn btn-sm btn-secondary" data-act="en" data-id="' +
            esc(t.id) + '">启用</button>') +
        '<button type="button" class="btn btn-sm btn-danger" data-act="del" data-id="' +
          esc(t.id) + '">删除</button>' +
        "</td></tr>" +
        '<tr class="tok-detail-row hidden" data-for="' + esc(t.id) + '"><td colspan="11">' +
        detail + "</td></tr>";
    }).join("");
    host.innerHTML =
      '<div class="table-wrap"><table><thead><tr>' +
      '<th><input type="checkbox" id="tokCheckAll" title="全选" /></th>' +
      "<th></th><th>ID</th><th>名称</th><th>前缀</th><th>状态</th><th>额度</th>" +
      "<th>并发</th><th>RPM</th><th>已用/请求</th><th></th>" +
      "</tr></thead><tbody>" + rows + "</tbody></table></div>";

    var master = $("tokCheckAll");
    if (master) {
      master.addEventListener("change", function () {
        var on = !!master.checked;
        host.querySelectorAll("input.tok-check").forEach(function (cb) { cb.checked = on; });
        updateTokSelUI();
      });
    }
    host.querySelectorAll("input.tok-check").forEach(function (cb) {
      cb.addEventListener("change", updateTokSelUI);
    });
    updateTokSelUI();

    host.querySelectorAll("button[data-act]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var act = btn.getAttribute("data-act");
        if (act === "expand") {
          var row = btn.closest("tr");
          var id = row && row.getAttribute("data-id");
          if (!id) return;
          var detailRow = host.querySelector('tr.tok-detail-row[data-for="' + id + '"]');
          if (!detailRow) return;
          var open = detailRow.classList.contains("hidden");
          detailRow.classList.toggle("hidden", !open);
          btn.textContent = open ? "▾" : "▸";
          btn.setAttribute("aria-expanded", open ? "true" : "false");
          return;
        }
        if (act === "copy") {
          var k = btn.getAttribute("data-key") || "";
          if (!k) return;
          copyText(k).then(function () { toast("已复制密钥", true); })
            .catch(function () { toast("复制失败", false); });
          return;
        }
        var id = btn.getAttribute("data-id");
        if (!id) return;
        btn.disabled = true;
        var p;
        var okMsg = "已更新";
        if (act === "del") {
          p = api("/admin/tokens/" + encodeURIComponent(id), { method: "DELETE" });
          okMsg = "已删除";
        } else if (act === "dis") {
          p = api("/admin/tokens/" + encodeURIComponent(id) + "/disable", {
            method: "POST", body: {}
          });
          okMsg = "已禁用";
        } else {
          p = api("/admin/tokens/" + encodeURIComponent(id) + "/enable", {
            method: "POST", body: {}
          });
          okMsg = "已启用";
        }
        p.then(function () {
          toast(okMsg, true);
          loadTokens();
        }).catch(function (e) {
          if (handleAuthError(e)) return;
          toast(e.message || "操作失败", false);
          btn.disabled = false;
        });
      });
    });
  }).catch(function (e) {
    if (handleAuthError(e)) return;
    host.innerHTML =
      '<div class="err-box">加载令牌失败：' + esc(e.message) + "</div>";
    toast(e.message, false);
    updateTokSelUI();
  });
}

export function renderTokens() {
  stopPoll();
  setAuthed(true);
  var d = window.__settings || {};
  var defQ = d.token_default_remain_quota != null ? d.token_default_remain_quota : 1000;
  var defC = d.token_default_max_concurrent != null ? d.token_default_max_concurrent : 5;
  var defR = d.token_default_rpm != null ? d.token_default_rpm : 0;
  var defU = d.token_default_unlimited ? "1" : "0";
  $("main").innerHTML = wrapPage(
    pageHd("令牌", "new-api 风格 · 明文仅在创建时显示一次 · 支持批量操作", "") +
    '<div class="panel token-create-panel"><div class="panel-title">快速创建</div>' +
    '<div class="form-row form-row-token">' +
    '<div class="field"><label for="tName">名称</label><input id="tName" class="input" value="client" /></div>' +
    '<div class="field"><label for="tCount">数量 (1-100)</label><input id="tCount" class="input" type="number" value="1" min="1" max="100" /></div>' +
    '<div class="field"><label for="tQuota">剩余额度</label><input id="tQuota" class="input" type="number" value="' + defQ + '" /></div>' +
    '<div class="field"><label for="tUnlim">无限额度</label><select id="tUnlim" class="input"><option value="0"' + (defU === "0" ? " selected" : "") + '>否</option><option value="1"' + (defU === "1" ? " selected" : "") + '>是</option></select></div>' +
    '<div class="field"><label for="tConc">令牌并发上限 (0=不限)</label><input id="tConc" class="input" type="number" value="' + defC + '" min="0" /></div>' +
    '<div class="field"><label for="tRpm">RPM (0=不限)</label><input id="tRpm" class="input" type="number" value="' + defR + '" min="0" /></div>' +
    "</div>" +
    '<div class="toolbar token-create-actions">' +
    '<button class="btn btn-primary" id="createBtn" type="button">创建并复制密钥</button>' +
    '<button class="page-action-btn" id="tokRefresh" type="button">刷新列表</button></div>' +
    '<div id="onceBox"></div></div>' +
    '<div class="section-head"><div class="section-title">令牌列表<span class="section-count-badge" id="tokCount">0</span></div></div>' +
    '<div class="toolbar acc-batch-bar" style="margin-bottom:8px">' +
    '<button type="button" class="btn btn-sm btn-secondary" id="tokSelectAll" disabled>全选</button>' +
    '<button type="button" class="btn btn-sm btn-secondary" id="tokSelectNone" disabled>清空</button>' +
    '<button type="button" class="btn btn-sm btn-secondary" id="tokCopySelected" disabled>批量复制密钥</button>' +
    '<button type="button" class="btn btn-sm btn-danger" id="tokBatchDelete" disabled>批量删除</button>' +
    '<span class="muted" id="tokSelCount">已选 0</span></div>' +
    '<div id="tokTable">' + skeletonRows(6, "加载令牌") + "</div>"
  );

  $("createBtn").addEventListener("click", function () {
    var body = {
      name: ($("tName").value || "").trim() || "client",
      count: parseInt($("tCount").value, 10) || 1,
      remain_quota: parseInt($("tQuota").value, 10) || 0,
      unlimited_quota: $("tUnlim").value === "1",
      max_concurrent: parseInt($("tConc").value, 10) || 0,
      rpm: parseInt($("tRpm").value, 10) || 0
    };
    var btn = $("createBtn");
    if (btn) btn.disabled = true;
    api("/admin/tokens", { method: "POST", body: body }).then(function (res) {
      var keys = extractPlainKeys(res);
      bindOnceBox(keys);
      if (keys[0]) {
        copyText(keys.join("\n")).then(function () {
          toast("已创建并复制到剪贴板", true);
        }).catch(function () {
          toast("已创建 " + keys.length + " 把密钥（请手动复制）", true);
        });
      } else {
        toast("已创建", true);
      }
      loadTokens();
    }).catch(function (e) {
      if (handleAuthError(e)) return;
      toast(e.message || "创建失败", false);
    }).then(function () {
      if (btn) btn.disabled = false;
    });
  });
  $("tokRefresh").addEventListener("click", loadTokens);
  $("tokSelectAll").addEventListener("click", function () {
    document.querySelectorAll("#tokTable input.tok-check").forEach(function (cb) { cb.checked = true; });
    var master = $("tokCheckAll");
    if (master) master.checked = true;
    updateTokSelUI();
  });
  $("tokSelectNone").addEventListener("click", function () {
    document.querySelectorAll("#tokTable input.tok-check").forEach(function (cb) { cb.checked = false; });
    var master = $("tokCheckAll");
    if (master) master.checked = false;
    updateTokSelUI();
  });
  $("tokCopySelected").addEventListener("click", function () {
    var keys = selectedTokenKeys();
    if (!keys.length) {
      toast("请先勾选有明文密钥的令牌（旧令牌可能无存盘）", false);
      return;
    }
    copyText(keys.join("\n")).then(function () {
      toast("已复制 " + keys.length + " 把密钥", true);
    }).catch(function () {
      toast("复制失败，请手动展开复制", false);
    });
  });
  $("tokBatchDelete").addEventListener("click", function () {
    var ids = selectedTokenIds();
    if (!ids.length) {
      toast("请先勾选令牌", false);
      return;
    }
    var btn = $("tokBatchDelete");
    if (btn) btn.disabled = true;
    var t0 = Date.now();
    api("/admin/tokens/batch", {
      method: "POST",
      body: { action: "delete", ids: ids }
    }).then(function (res) {
      var ok = res && res.ok != null ? res.ok : 0;
      toast("批量删除：成功 " + ok + "（" + (Date.now() - t0) + "ms）", true);
      loadTokens();
    }).catch(function (e) {
      if (handleAuthError(e)) return;
      toast(e.message || "批量删除失败", false);
      updateTokSelUI();
    });
  });
  loadTokens();
}
