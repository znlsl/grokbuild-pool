package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ExportAccounts GET /admin/accounts/export?format=json|ndjson&chunk=500
// 后端按 chunk 从 catalog 分页抽取，再在响应里整合成单一文件（流式写出）。
// 大库时避免一次加载全表进内存：边查边写。
func (h *Handlers) ExportAccounts(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		writeErr(w, http.StatusServiceUnavailable, "账号目录未启用")
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "ndjson" {
		writeErr(w, http.StatusBadRequest, "format 须为 json 或 ndjson")
		return
	}
	chunk := 500
	if v := strings.TrimSpace(r.URL.Query().Get("chunk")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "chunk 无效")
			return
		}
		if n > 2000 {
			n = 2000
		}
		chunk = n
	}

	total, _ := h.Catalog.CountAccounts()
	ts := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("accounts-export-%s.%s", ts, map[string]string{"json": "json", "ndjson": "ndjson"}[format])
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Export-Total", strconv.Itoa(total))
	w.Header().Set("X-Export-Chunk", strconv.Itoa(chunk))
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	cursor := ""
	first := true
	if format == "json" {
		// 包装为数组：边查边写，避免整表进内存
		if _, err := w.Write([]byte("[\n")); err != nil {
			return
		}
	}
	written := 0
	for {
		rows, err := h.Catalog.ListExportAccounts(chunk, cursor)
		if err != nil {
			// 流已开始，只能尽力写入错误注释
			_, _ = w.Write([]byte("\n"))
			return
		}
		if len(rows) == 0 {
			break
		}
		for _, a := range rows {
			// 导出为可再导入的 auth 风格对象
			item := map[string]any{
				"key":           a.ID,
				"email":         a.Email,
				"access_token":  a.AccessToken,
				"refresh_token": a.RefreshToken,
				"expires_at":    a.ExpiresAt,
				"proxy_url":     a.ProxyURL,
				"disabled":      !a.Enabled,
			}
			if a.Name != "" {
				item["name"] = a.Name
			}
			if format == "json" {
				if !first {
					if _, err := w.Write([]byte(",\n")); err != nil {
						return
					}
				}
				first = false
				b, err := json.Marshal(item)
				if err != nil {
					return
				}
				if _, err := w.Write(b); err != nil {
					return
				}
			} else {
				if err := enc.Encode(item); err != nil {
					return
				}
			}
			written++
			cursor = a.ID
		}
		if flusher != nil {
			flusher.Flush()
		}
		if len(rows) < chunk {
			break
		}
	}
	if format == "json" {
		_, _ = w.Write([]byte("\n]\n"))
	}
	_ = written
}
