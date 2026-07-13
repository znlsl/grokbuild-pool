/* Fetch helpers + auth error handling */
import { state } from "./state.js";
import { toast } from "./util.js";

var unauthHandler = null;

export function setUnauthHandler(fn) {
  unauthHandler = typeof fn === "function" ? fn : null;
}

function headers(body) {
  var h = { "Accept": "application/json" };
  var isForm = typeof FormData !== "undefined" && body instanceof FormData;
  if (!isForm) h["Content-Type"] = "application/json";
  if (state.adminKey) h["Authorization"] = "Bearer " + state.adminKey;
  return h;
}

export function api(path, opts) {
  opts = opts || {};
  var body = opts.body;
  var isForm = typeof FormData !== "undefined" && body instanceof FormData;
  return fetch(path, {
    method: opts.method || "GET",
    headers: headers(body),
    body: body !== undefined ? (isForm ? body : JSON.stringify(body)) : undefined
  }).then(function (r) {
    return r.text().then(function (text) {
      var j = {};
      if (text) {
        try { j = JSON.parse(text); } catch (_) { j = {}; }
      }
      if (!r.ok) {
        var msg = (j && j.error) || ("HTTP " + r.status);
        if (typeof msg !== "string") msg = "HTTP " + r.status;
        var err = new Error(msg);
        err.status = r.status;
        throw err;
      }
      return j;
    });
  });
}

export function handleAuthError(e) {
  if (e && e.status === 401) {
    state.adminKey = null;
    if (unauthHandler) unauthHandler();
    location.hash = "#/login";
    toast("鉴权失效，请重新登录", false);
    return true;
  }
  return false;
}

/** 导出等非 JSON 请求用的 admin key 头 */
export function adminAuthHeaders() {
  var h = {};
  if (state.adminKey) {
    h["Authorization"] = "Bearer " + state.adminKey;
    h["X-Admin-Key"] = state.adminKey;
  }
  return h;
}
