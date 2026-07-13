/* 首屏主题：默认浅色（对齐 grok2api），localStorage 可覆盖。UTF-8. */
(function () {
  try {
    var s = localStorage.getItem("pool-admin-theme");
    if (s !== "light" && s !== "dark") s = "light";
    document.documentElement.setAttribute("data-theme", s);
  } catch (e) {
    document.documentElement.setAttribute("data-theme", "light");
  }
})();
