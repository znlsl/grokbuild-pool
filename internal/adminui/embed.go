// Package adminui 提供零构建管理台（深/浅色），通过 //go:embed 打入二进制。
//
// 挂载约定（与 pool-proxy 一致）：
//
//	/admin、/admin/  → index.html（无需 admin_key）
//	/admin/ui/*      → static 下 CSS/JS 等静态资源
//
// JSON 管理 API 仍由 internal/admin 鉴权挂载；本包只负责静态壳。
package adminui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static/*
var staticFS embed.FS

// Mount 挂载管理台静态资源（无需 admin_key；API 另鉴权）。
// 路由保持：/admin、/admin/、/admin/ui/*。
func Mount(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(sub))

	// /admin → /admin/
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})

	mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)

		// SPA 壳
		if r.URL.Path == "/admin/" || r.URL.Path == "/admin" {
			b, err := staticFS.ReadFile("static/index.html")
			if err != nil {
				http.Error(w, "ui missing", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
			return
		}

		// /admin/ui/* → static 根（app.css / app.js）
		if strings.HasPrefix(r.URL.Path, "/admin/ui/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/admin/ui/")
			name = path.Clean("/" + name)
			name = strings.TrimPrefix(name, "/")
			if name == "" || name == "." || strings.Contains(name, "..") {
				http.NotFound(w, r)
				return
			}
			// 类型与缓存
			switch {
			case strings.HasSuffix(name, ".js"):
				w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
			case strings.HasSuffix(name, ".css"):
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
			}
			if _, err := sub.Open(name); err != nil {
				http.NotFound(w, r)
				return
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + name
			fileServer.ServeHTTP(w, r2)
			return
		}

		http.NotFound(w, r)
	})
}

// setSecurityHeaders 统一安全响应头（CSP 禁止 inline script，适配外部 app.js）。
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// 无 inline 事件/脚本；样式走外部 app.css（无 'unsafe-inline'）
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
}

// ReadStatic 读取已嵌入的 static 文件（测试/调试用）。
func ReadStatic(name string) ([]byte, error) {
	return staticFS.ReadFile(path.Join("static", name))
}
