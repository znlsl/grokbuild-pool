package adminui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// 确保嵌入的静态壳齐全且非空。
func TestEmbeddedStaticFilesPresent(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "app.css", "theme-boot.js"} {
		b, err := ReadStatic(name)
		if err != nil {
			t.Fatalf("缺少 embed static/%s: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("static/%s 为空", name)
		}
	}
}

func TestStaticFilesAreCleanUTF8(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "app.css", "theme-boot.js"} {
		b, err := ReadStatic(name)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.Valid(b) {
			t.Fatalf("static/%s 不是有效 UTF-8", name)
		}
		if bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}) {
			t.Fatalf("static/%s 不应包含 UTF-8 BOM", name)
		}
		if bytes.Contains(b, []byte{0xef, 0xbf, 0xbd}) {
			t.Fatalf("static/%s 包含 Unicode replacement character", name)
		}
	}

	for _, name := range []string{"index.html", "app.js"} {
		b, err := ReadStatic(name)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte{'<', '\\'}) {
			t.Fatalf("static/%s 包含反斜杠 HTML 标签", name)
		}
	}
}

func TestDashboardP0Markers(t *testing.T) {
	js, err := ReadStatic("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, oldToken := range []string{"--bg2", "--muted", "--ok", "--bad"} {
		if strings.Contains(src, `getPropertyValue("`+oldToken+`")`) {
			t.Fatalf("Dashboard 图表不应继续读取旧变量 %s", oldToken)
		}
	}
	for _, marker := range []string{
		`getPropertyValue("--bg-card")`,
		`getPropertyValue("--fg-muted")`,
		`getPropertyValue("--success")`,
		`getPropertyValue("--error")`,
		`getPropertyValue("--border")`,
		`.admin-nav-link, .mobile-nav a`,
		`aria-current`,
		`function bindMobileMenu(`,
		`ev.key === "Escape"`,
		`setAttribute("inert", "")`,
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("app.js 缺少 P0 标记 %q", marker)
		}
	}
}

func TestBrowserUploadAndSpacingMarkers(t *testing.T) {
	js, err := ReadStatic("app.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := ReadStatic("app.css")
	if err != nil {
		t.Fatal(err)
	}
	jsSrc := string(js)
	for _, marker := range []string{
		`type="file"`,
		`new FormData()`,
		`data.append("format"`,
		`data.append("file"`,
		`body instanceof FormData`,
		`source_name`,
	} {
		if !strings.Contains(jsSrc, marker) {
			t.Fatalf("app.js 缺少浏览器上传标记 %q", marker)
		}
	}
	for _, forbidden := range []string{"服务端路径（data/ 下）", `id="impPath"`, `id="impURL"`} {
		if strings.Contains(jsSrc, forbidden) {
			t.Fatalf("app.js 不应包含旧/不安全导入入口 %q", forbidden)
		}
	}
	cssSrc := string(css)
	for _, marker := range []string{
		".spark-wrap {",
		".spark-head {",
		".acc-batch-bar {",
		".form-row > * {",
		".file-input {",
		".stat-grid {",
	} {
		if !strings.Contains(cssSrc, marker) {
			t.Fatalf("app.css 缺少间距/上传标记 %q", marker)
		}
	}
}

// 主题键与 admin_key 不落盘约束（源码级守卫）。
func TestThemeKeyAndNoAdminKeyPersistence(t *testing.T) {
	js, err := ReadStatic("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, `localStorage.getItem("pool-admin-theme")`) &&
		!strings.Contains(src, `localStorage.getItem('pool-admin-theme')`) {
		// 兼容 getItem("pool-admin-theme")
		if !strings.Contains(src, "pool-admin-theme") {
			t.Fatal("app.js 应使用 localStorage 键 pool-admin-theme")
		}
	}
	// admin_key 不得写入 storage
	for _, bad := range []string{
		`localStorage.setItem("admin`,
		`localStorage.setItem('admin`,
		`sessionStorage.setItem("admin`,
		`sessionStorage.setItem('admin`,
		`localStorage.setItem("admin_key`,
		`localStorage.setItem('admin_key`,
		`sessionStorage.setItem("admin_key`,
		`sessionStorage.setItem('admin_key`,
	} {
		if strings.Contains(src, bad) {
			t.Fatalf("app.js 不得将 admin_key 写入 storage，命中 %q", bad)
		}
	}
	// 确认 adminKey 仅内存
	if !strings.Contains(src, "var adminKey") {
		t.Fatal("app.js 应使用内存变量 adminKey")
	}
}

// CSP 下禁止 HTML inline 事件处理器。
func TestNoInlineEventHandlersInHTML(t *testing.T) {
	html, err := ReadStatic("index.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	// 常见 inline 事件
	for _, bad := range []string{"onclick=", "onload=", "onerror=", "onsubmit=", "onchange="} {
		if strings.Contains(strings.ToLower(s), bad) {
			t.Fatalf("index.html 不得含 inline 事件 %q（CSP script-src 'self'）", bad)
		}
	}
	if !strings.Contains(s, "/admin/ui/app.js") {
		t.Fatal("index.html 应引用 /admin/ui/app.js")
	}
	if !strings.Contains(s, "/admin/ui/app.css") {
		t.Fatal("index.html 应引用 /admin/ui/app.css")
	}
	// 无内联 <script> 块
	if strings.Contains(s, "<script>") && !strings.Contains(s, `<script src=`) {
		// 允许 <script src=...>，禁止裸 script 体
	}
	if strings.Contains(s, "<script>") {
		// 检查是否存在无 src 的 script
		lower := s
		idx := 0
		for {
			i := strings.Index(strings.ToLower(lower[idx:]), "<script")
			if i < 0 {
				break
			}
			i += idx
			end := strings.Index(lower[i:], ">")
			if end < 0 {
				break
			}
			tag := lower[i : i+end+1]
			if !strings.Contains(strings.ToLower(tag), "src=") {
				t.Fatalf("index.html 存在无 src 的 inline script: %s", tag)
			}
			idx = i + end + 1
		}
	}
}

// 批量创建多 key 展示与 toast 反馈相关标记。
func TestBatchKeyAndToastMarkers(t *testing.T) {
	js, err := ReadStatic("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, marker := range []string{
		"bindOnceBox",
		"copyAllKeys",
		"data-copy-one",
		"function toast(",
		"明文密钥仅显示一次",
		"暂无令牌",
		// 禁用/删除走 toast，无 confirm
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("app.js 缺少标记 %q", marker)
		}
	}
	if strings.Contains(src, "confirm(") {
		t.Fatal("禁用/删除应简化为 toast，不应使用 confirm()")
	}
}

// 账号页：导航、路由、列表 API、启停、设置持久化 toast。
func TestAccountsPageMarkers(t *testing.T) {
	html, err := ReadStatic("index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := string(html)
	if !strings.Contains(h, `data-route="accounts"`) || !strings.Contains(h, "账号") {
		t.Fatal("index.html 导航应含「账号」")
	}
	js, err := ReadStatic("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, marker := range []string{
		`page === "accounts"`,
		"function renderAccounts(",
		"function loadAccounts(",
		`/admin/accounts`,
		"/admin/accounts/batch",
		"function runBatchAccounts(",
		"acc-check",
		"批量启用",
		"批量禁用",
		"/disable",
		"/enable",
		"暂无账号",
		"已持久化",
		"persisted",
		// 仪表盘 sparkline（localStorage KPI 历史）
		"pool-admin-kpi-history",
		"function drawSparkline(",
		"rateSpark",
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("app.js 账号页缺少标记 %q", marker)
		}
	}
}

func TestMountServesIndexAndAssets(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)

	// /admin → 302 /admin/
	{
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("/admin status=%d want 302", rec.Code)
		}
		loc := rec.Header().Get("Location")
		if loc != "/admin/" {
			t.Fatalf("/admin Location=%q want /admin/", loc)
		}
	}

	// /admin/ → HTML + 安全头
	{
		req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/admin/ status=%d want 200", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/html") || !strings.Contains(strings.ToLower(ct), "charset=utf-8") {
			t.Fatalf("Content-Type=%q want text/html; charset=utf-8", ct)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control=%q want no-store", rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatal("缺少 X-Frame-Options: DENY")
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("缺少 nosniff")
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") {
			t.Fatalf("CSP 应限制 script-src 'self'，got %q", csp)
		}
		if strings.Contains(csp, "unsafe-inline") && strings.Contains(csp, "script-src") {
			// style 也不应再依赖 unsafe-inline；整体禁止即可
			if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") ||
				strings.Contains(csp, "script-src 'unsafe-inline'") {
				t.Fatal("CSP 不得允许 script unsafe-inline")
			}
		}
		body := rec.Body.String()
		if !strings.Contains(body, "grokbuild-pool") {
			t.Fatal("index 正文应含 grokbuild-pool")
		}
		if !strings.Contains(body, "/admin/ui/app.js") {
			t.Fatal("index 应引用 /admin/ui/app.js")
		}
	}

	// 静态资源
	assets := []struct {
		path        string
		contentType string
	}{
		{"/admin/ui/app.js", "application/javascript; charset=utf-8"},
		{"/admin/ui/app.css", "text/css; charset=utf-8"},
		{"/admin/ui/theme-boot.js", "application/javascript; charset=utf-8"},
	}
	for _, asset := range assets {
		req := httptest.NewRequest(http.MethodGet, asset.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", asset.path, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("%s 空响应", asset.path)
		}
		if ct := strings.ToLower(rec.Header().Get("Content-Type")); ct != asset.contentType {
			t.Fatalf("%s Content-Type=%q want %q", asset.path, ct, asset.contentType)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s 缺少 nosniff", asset.path)
		}
	}

	// 404
	{
		req := httptest.NewRequest(http.MethodGet, "/admin/ui/nope.js", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("missing asset status=%d want 404", rec.Code)
		}
	}

	// 未知 /admin/xxx
	{
		req := httptest.NewRequest(http.MethodGet, "/admin/not-a-page", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("/admin/not-a-page status=%d want 404", rec.Code)
		}
	}
}

func TestReadStaticHelper(t *testing.T) {
	b, err := ReadStatic("app.js")
	if err != nil || len(b) < 100 {
		t.Fatalf("ReadStatic(app.js) err=%v len=%d", err, len(b))
	}
}

func TestMountNilSafe(t *testing.T) {
	Mount(nil) // 不应 panic
}
