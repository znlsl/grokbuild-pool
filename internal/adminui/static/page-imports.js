/* Import jobs page */
import { state } from "./state.js";
import { $, esc, toast, skeletonRows } from "./util.js";
import { api, handleAuthError } from "./api.js";
import { setAuthed, stopPoll, wrapPage, pageHd } from "./shell.js";

export function renderImportJobs() {
  stopPoll();
  setAuthed(true);
  $("main").innerHTML = wrapPage(
    pageHd("导入任务", "从本地浏览器安全上传，后台异步导入",
      '<button type="button" class="page-action-btn" id="impRefresh">刷新</button>') +
    '<div class="panel">' +
    '<div id="impErr"></div>' +
    '<div class="form-row">' +
    '<div><label for="impFormat">格式</label>' +
    '<select id="impFormat" class="input"><option value="sso" selected>SSO</option><option value="json">JSON</option><option value="ndjson">NDJSON</option></select></div>' +
    '<div><label for="impFile">本地文件</label>' +
    '<input id="impFile" class="input file-input" type="file" accept=".txt,.json,text/plain,application/json" /></div></div>' +
    '<p class="muted panel-note import-limit-note" id="impLimits">默认 SSO→JSON · 最多 10000 条 · 正在读取服务端限制…</p>' +
    '<div class="toolbar form-actions">' +
    '<button type="button" class="btn btn-primary" id="impSubmit">上传并创建任务</button></div></div>' +
    '<div class="section-head"><div class="section-title">任务列表</div></div>' +
    '<div id="impTable">' + skeletonRows(5, "加载导入任务") + "</div>"
  );
  var importMaxUpload = 0;
  var importMaxEntries = 10000;

  function setImportError(message) {
    var host = $("impErr");
    if (!host) return;
    host.innerHTML = message ? '<div class="err-box">' + esc(message) + "</div>" : "";
  }

  function scheduleJobs(list) {
    stopPoll();
    var active = (list || []).some(function (j) {
      return j && (j.state === "queued" || j.state === "running");
    });
    if (active && state.currentPage === "imports") {
      state.pollTimer = setTimeout(loadJobs, 2500);
    }
  }

  function loadJobs() {
    stopPoll();
    return api("/admin/import/jobs").then(function (res) {
      var list = (res && res.jobs) || [];
      var limits = (res && res.limits) || {};
      var note = $("impLimits");
      if (limits.max_upload_bytes != null && limits.max_upload_bytes !== "") {
        var up = Number(limits.max_upload_bytes);
        if (!isNaN(up) && up >= 0) importMaxUpload = up;
      }
      if (limits.max_entries) importMaxEntries = Number(limits.max_entries) || importMaxEntries;
      if (note) {
        var sizeHint = (!importMaxUpload || importMaxUpload <= 0)
          ? "不限体积"
          : ("体积兜底 " + Math.round(importMaxUpload / 1048576) + " MiB");
        note.textContent = "默认 SSO→JSON · 最多 " + importMaxEntries +
          " 条 · " + sizeHint + " · 转换器" +
          (limits.sso_converter_configured ? "已就绪（内置 Go Device Flow）" : "未就绪");
      }
      var host = $("impTable");
      if (!host) return;
      if (!list.length) {
        host.innerHTML = '<div class="empty"><strong>暂无任务</strong>选择本地文件后创建导入任务。</div>';
        scheduleJobs(list);
        return;
      }
      var rows = list.map(function (j) {
        return "<tr><td class=\"mono\">" + esc(j.id) + "</td><td>" + esc(j.source_name || "浏览器上传") +
          "</td><td>" + esc(j.format) + "</td><td>" + esc(j.state) + "</td><td>" +
          esc(String(j.ok != null ? j.ok : "—")) + "/" + esc(String(j.total != null ? j.total : "—")) +
          "</td><td>" + esc(j.error || "—") + "</td></tr>";
      }).join("");
      host.innerHTML = '<div class="table-wrap"><table><thead><tr><th>ID</th><th>来源</th><th>格式</th><th>状态</th><th>OK/Total</th><th>错误</th></tr></thead><tbody>' +
        rows + "</tbody></table></div>";
      scheduleJobs(list);
    }).catch(function (e) {
      if (handleAuthError(e)) return;
      setImportError("加载任务失败：" + (e.message || "未知错误"));
      stopPoll();
      if (state.currentPage === "imports") {
        state.pollTimer = setTimeout(loadJobs, 2500);
      }
    });
  }

  function syncFileAccept() {
    var format = $("impFormat").value;
    var input = $("impFile");
    if (!input) return;
    if (format === "ndjson") input.accept = ".ndjson,.jsonl,application/x-ndjson";
    else if (format === "sso") input.accept = ".txt,.json,text/plain,application/json";
    else input.accept = ".json,application/json";
  }

  $("impFormat").addEventListener("change", syncFileAccept);
  $("impRefresh").addEventListener("click", loadJobs);
  $("impSubmit").addEventListener("click", function () {
    var input = $("impFile");
    var file = input && input.files && input.files[0];
    if (!file) {
      setImportError("请选择要上传的本地文件");
      return;
    }
    if (importMaxUpload > 0 && file.size > importMaxUpload) {
      setImportError("文件超过 " + Math.round(importMaxUpload / 1048576) + " MiB；服务端也会再次校验");
      return;
    }
    setImportError("");
    var data = new FormData();
    data.append("format", $("impFormat").value);
    data.append("file", file, file.name);
    var btn = $("impSubmit");
    btn.disabled = true;
    btn.textContent = "上传中…";
    api("/admin/import/jobs", { method: "POST", body: data }).then(function () {
      input.value = "";
      toast("导入任务已创建", true);
      return loadJobs();
    }).catch(function (e) {
      if (handleAuthError(e)) return;
      var message = e.status === 413 ? "文件超过上传大小限制" :
        e.status === 429 ? "导入任务已满，请稍后重试" :
        e.status === 503 ? "SSO 转换器未配置或导入服务不可用" :
        (e.message || "提交失败");
      setImportError(message);
      toast(message, false);
    }).then(function () {
      btn.disabled = false;
      btn.textContent = "上传并创建任务";
    });
  });
  syncFileAccept();
  loadJobs();
}
