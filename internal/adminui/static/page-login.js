/* Login page */
import { state } from "./state.js";
import { $, toast } from "./util.js";
import { api } from "./api.js";
import { setAuthed, stopPoll, wrapPage } from "./shell.js";

export function renderLogin() {
  stopPoll();
  setAuthed(false);
  state.dashBuilt = false;
  $("main").innerHTML = wrapPage(
    '<div class="login-body-wrap">' +
    '<div class="login-shell"><div class="login-card">' +
    '<div class="login-brand">GROKBUILD-POOL</div>' +
    '<div class="login-title">管理后台</div>' +
    '<div class="login-subtitle">请输入 admin_key 以继续（仅保存在本页内存）</div>' +
    '<div class="login-form">' +
    '<input type="password" id="keyInput" class="input" placeholder="后台密钥" autocomplete="off" spellcheck="false" />' +
    '<button class="btn btn-primary" id="loginBtn" type="button">继续</button>' +
    "</div></div></div></div>"
  );

  function doLogin() {
    var k = ($("keyInput").value || "").trim();
    if (!k) return toast("请输入密钥", false);
    var btn = $("loginBtn");
    if (btn) btn.disabled = true;
    state.adminKey = k;
    Promise.all([api("/admin/pool/stats"), api("/admin/settings")]).then(function (arr) {
      window.__settings = arr[1] || {};
      setAuthed(true);
      location.hash = "#/dashboard";
      toast("登录成功", true);
    }).catch(function (e) {
      state.adminKey = null;
      toast(e.message || "登录失败", false);
    }).then(function () {
      if (btn) btn.disabled = false;
    });
  }

  $("loginBtn").addEventListener("click", doLogin);
  $("keyInput").addEventListener("keydown", function (ev) {
    if (ev.key === "Enter") {
      ev.preventDefault();
      doLogin();
    }
  });
  setTimeout(function () {
    var inp = $("keyInput");
    if (inp) inp.focus();
  }, 0);
}
