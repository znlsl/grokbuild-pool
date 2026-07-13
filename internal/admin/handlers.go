package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/clients"
	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/importjobs"
)

// HotStats 热池只读指标（供仪表盘）。
type HotStats interface {
	HotLen() int
	PoolStats() (hotSize, cooldown int)
}

// MetricsView 进程指标只读视图（由 httpserver.Metrics 实现，避免循环依赖）。
type MetricsView interface {
	Requests() int64
	Errors() int64
	Rejects() int64
	Inflight() int64
}

// Handlers 管理 API 处理器集合。
type Handlers struct {
	AdminKey  string
	Config    config.Config
	Tokens    *clients.Store
	Hot       HotStats
	Metrics   MetricsView
	Version   string
	StartedAt time.Time
	Settings  *SettingsController
	// Catalog 冷存储（账号列表 / 启停）；可空则相关路由 503。
	Catalog *catalog.Catalog
	// AccountHot 热池同步（启停账号）；可空则只改冷存储。
	AccountHot AccountHot
	// ImportJobs 异步 bulkimport 任务表（P1）；可空则相关路由 503。
	ImportJobs *importjobs.Manager
}

// effectiveAdminKey 优先使用设置页热更新后的密钥。
func (h *Handlers) effectiveAdminKey() string {
	if h != nil && h.Settings != nil {
		if k := strings.TrimSpace(h.Settings.PeekAdminKey()); k != "" {
			return k
		}
	}
	if h == nil {
		return ""
	}
	return strings.TrimSpace(h.AdminKey)
}

// RequireAdmin 校验 admin_key（Bearer 或 x-admin-key）。
func (h *Handlers) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := ""
		if h != nil {
			want = h.effectiveAdminKey()
		}
		if want == "" {
			writeErr(w, http.StatusServiceUnavailable, "未配置 admin_key")
			return
		}
		key := extractAdminKey(r)
		if key == "" || key != want {
			writeErr(w, http.StatusUnauthorized, "admin 鉴权失败")
			return
		}
		next(w, r)
	}
}

func extractAdminKey(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("x-admin-key")); v != "" {
		return v
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// Mount 将管理路由挂到 mux（JSON 需 admin；静态 UI 由 adminui 单独挂载）。
func (h *Handlers) Mount(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /admin/pool/stats", h.RequireAdmin(h.PoolStats))
	mux.HandleFunc("GET /admin/tokens", h.RequireAdmin(h.ListTokens))
	mux.HandleFunc("POST /admin/tokens", h.RequireAdmin(h.CreateTokens))
	mux.HandleFunc("POST /admin/tokens/batch", h.RequireAdmin(h.BatchTokens))
	mux.HandleFunc("DELETE /admin/tokens/{id}", h.RequireAdmin(h.DeleteToken))
	mux.HandleFunc("POST /admin/tokens/{id}/disable", h.RequireAdmin(h.DisableToken))
	mux.HandleFunc("POST /admin/tokens/{id}/enable", h.RequireAdmin(h.EnableToken))
	mux.HandleFunc("PATCH /admin/tokens/{id}", h.RequireAdmin(h.PatchToken))
	mux.HandleFunc("GET /admin/config", h.RequireAdmin(h.SafeConfig))
	mux.HandleFunc("GET /admin/settings", h.RequireAdmin(h.GetSettings))
	mux.HandleFunc("PUT /admin/settings", h.RequireAdmin(h.PutSettings))
	// 账号目录（脱敏分页 / 手动启停 / 批量 / 代理 / 导出）
	mux.HandleFunc("GET /admin/accounts", h.RequireAdmin(h.ListAccounts))
	// 导出与批量路由须先于 /{id}/… 注册
	mux.HandleFunc("GET /admin/accounts/export", h.RequireAdmin(h.ExportAccounts))
	mux.HandleFunc("POST /admin/accounts/batch", h.RequireAdmin(h.BatchAccounts))
	mux.HandleFunc("POST /admin/accounts/{id}/disable", h.RequireAdmin(h.DisableAccount))
	mux.HandleFunc("POST /admin/accounts/{id}/enable", h.RequireAdmin(h.EnableAccount))
	mux.HandleFunc("PATCH /admin/accounts/{id}", h.RequireAdmin(h.PatchAccountProxy))
	// 导入任务（P1 最小接线）
	mux.HandleFunc("GET /admin/import/jobs", h.RequireAdmin(h.ListImportJobs))
	mux.HandleFunc("POST /admin/import/jobs", h.RequireAdmin(h.CreateImportJob))
	mux.HandleFunc("GET /admin/import/jobs/{id}", h.RequireAdmin(h.GetImportJob))
}

// PoolStats 返回仪表盘 KPI（不含密钥明文）。
func (h *Handlers) PoolStats(w http.ResponseWriter, r *http.Request) {
	hotSize, cooldown := 0, 0
	if h.Hot != nil {
		hotSize, cooldown = h.Hot.PoolStats()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	reqTotal, errTotal, reject, inflight := int64(0), int64(0), int64(0), int64(0)
	if h.Metrics != nil {
		reqTotal = h.Metrics.Requests()
		errTotal = h.Metrics.Errors()
		reject = h.Metrics.Rejects()
		inflight = h.Metrics.Inflight()
	}
	successRate := 1.0
	if reqTotal > 0 {
		ok := reqTotal - errTotal
		if ok < 0 {
			ok = 0
		}
		successRate = float64(ok) / float64(reqTotal)
	}
	tokTotal, tokEnabled, tokExhausted := 0, 0, 0
	if h.Tokens != nil {
		tokTotal, tokEnabled, tokExhausted, _ = h.Tokens.Stats()
	}
	uptime := 0.0
	if !h.StartedAt.IsZero() {
		uptime = time.Since(h.StartedAt).Seconds()
	}
	out := map[string]any{
		"version":            h.Version,
		"uptime_seconds":     uptime,
		"requests_total":     reqTotal,
		"errors_total":       errTotal,
		"success_rate":       successRate,
		"proxy_reject_total": reject,
		"proxy_inflight":     inflight,
		"pool_hot_size":      hotSize,
		"pool_cooldown_size": cooldown,
		"process_rss_bytes":  ms.Sys,
		"go_goroutines":      runtime.NumGoroutine(),
		"tokens_total":       tokTotal,
		"tokens_enabled":     tokEnabled,
		"tokens_exhausted":   tokExhausted,
		"listen":             h.Config.Listen,
		"hot_cap":            h.Config.HotSize,
		"max_concurrent":     h.Config.Limits.MaxConcurrent,
	}
	// P1：refresh / quarantine
	if x, ok := h.Metrics.(interface {
		RefreshOK() int64
		RefreshFail() int64
		QuarantineCount() int64
	}); ok {
		out["refresh_ok_total"] = x.RefreshOK()
		out["refresh_fail_total"] = x.RefreshFail()
		out["pool_quarantine_count"] = x.QuarantineCount()
	}
	if h.Catalog != nil {
		if st, err := h.Catalog.Stats(); err == nil {
			out["catalog_count"] = st.Count
			out["catalog_quarantine"] = st.QuarantineCount
			out["pool_quarantine_count"] = st.QuarantineCount
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ListTokens 列出令牌（含 api_key 明文供管理台展开/批量复制；旧令牌可能为空）。
func (h *Handlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	if h.Tokens == nil {
		writeErr(w, http.StatusServiceUnavailable, "令牌存储未启用")
		return
	}
	list, err := h.Tokens.List(200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []clients.Token{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": list})
}

// CreateTokens 快速创建/批量发放密钥（明文仅此响应返回一次）。
func (h *Handlers) CreateTokens(w http.ResponseWriter, r *http.Request) {
	if h.Tokens == nil {
		writeErr(w, http.StatusServiceUnavailable, "令牌存储未启用")
		return
	}
	var req clients.CreateRequest
	if err := decodeJSON(r, 1<<20, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 未显式填写时使用管理台默认模板
	if h.Settings != nil {
		d := h.Settings.Snapshot()
		if req.Name == "" {
			req.Name = "client"
		}
		if req.Count <= 0 {
			req.Count = 1
		}
		if !req.UnlimitedQuota && req.RemainQuota == 0 && d.TokenDefaultRemainQuota > 0 {
			req.RemainQuota = d.TokenDefaultRemainQuota
		}
		if req.UnlimitedQuota == false && d.TokenDefaultUnlimited {
			req.UnlimitedQuota = true
		}
		if req.MaxConcurrent == 0 && d.TokenDefaultMaxConcurrent > 0 {
			req.MaxConcurrent = d.TokenDefaultMaxConcurrent
		}
		if req.RPM == 0 && d.TokenDefaultRPM > 0 {
			req.RPM = d.TokenDefaultRPM
		}
	}
	res, err := h.Tokens.Create(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 单条兼容字段
	out := map[string]any{"created": len(res), "tokens": res}
	if len(res) == 1 {
		out["token"] = res[0].Token
		out["api_key"] = res[0].APIKey
		out["plaintext"] = res[0].Plaintext
	}
	writeJSON(w, http.StatusCreated, out)
}

// DeleteToken 删除令牌。
func (h *Handlers) DeleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.Tokens == nil {
		writeErr(w, http.StatusServiceUnavailable, "令牌存储未启用")
		return
	}
	if err := h.Tokens.Delete(id); err != nil {
		if errors.Is(err, clients.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "令牌不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// BatchTokens POST /admin/tokens/batch
// body: {"action":"delete","ids":["..."]}，ids 上限 500；单事务批量删除。
func (h *Handlers) BatchTokens(w http.ResponseWriter, r *http.Request) {
	if h.Tokens == nil {
		writeErr(w, http.StatusServiceUnavailable, "令牌存储未启用")
		return
	}
	var body struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	if action != "delete" {
		writeErr(w, http.StatusBadRequest, "action 目前仅支持 delete")
		return
	}
	if len(body.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ids 不能为空")
		return
	}
	if len(body.IDs) > 500 {
		writeErr(w, http.StatusBadRequest, "ids 最多 500 个")
		return
	}
	n, err := h.Tokens.DeleteMany(body.IDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"action":  action,
		"ok":      n,
		"failed":  len(body.IDs) - n,
		"deleted": n,
	})
}

// DisableToken 禁用。
func (h *Handlers) DisableToken(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, false)
}

// EnableToken 启用。
func (h *Handlers) EnableToken(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, true)
}

func (h *Handlers) setEnabled(w http.ResponseWriter, r *http.Request, en bool) {
	id := r.PathValue("id")
	if h.Tokens == nil {
		writeErr(w, http.StatusServiceUnavailable, "令牌存储未启用")
		return
	}
	if err := h.Tokens.SetEnabled(id, en); err != nil {
		if errors.Is(err, clients.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "令牌不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "enabled": en})
}

// PatchToken 调整额度/并发/RPM。
func (h *Handlers) PatchToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.Tokens == nil {
		writeErr(w, http.StatusServiceUnavailable, "令牌存储未启用")
		return
	}
	var body struct {
		RemainQuota    *int64 `json:"remain_quota"`
		UnlimitedQuota *bool  `json:"unlimited_quota"`
		MaxConcurrent  *int   `json:"max_concurrent"`
		RPM            *int   `json:"rpm"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.Tokens.PatchQuota(id, body.RemainQuota, body.UnlimitedQuota, body.MaxConcurrent, body.RPM); err != nil {
		if errors.Is(err, clients.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "令牌不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "patched": true})
}

// SafeConfig 返回可展示配置（无密钥）。
func (h *Handlers) SafeConfig(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"listen":         h.Config.Listen,
		"data_dir":       h.Config.DataDir,
		"hot_size":       h.Config.HotSize,
		"max_concurrent": h.Config.Limits.MaxConcurrent,
		"mock_upstream":  h.Config.UseMockUpstream(),
		"api_key_set":    strings.TrimSpace(h.Config.APIKey) != "",
		"admin_key_set":  strings.TrimSpace(h.Config.AdminKey) != "",
		"version":        h.Version,
		"imports": map[string]any{
			"enabled":                  h.Config.Imports.Enabled,
			"max_upload_bytes":         h.Config.Imports.MaxUploadBytes,
			"max_entries":              h.Config.Imports.MaxEntries,
			"allow_server_path":        h.Config.Imports.AllowServerPath,
			"sso_converter_configured": strings.TrimSpace(h.Config.Imports.SSOConverter.Endpoint) != "" && strings.TrimSpace(h.Config.Imports.SSOConverter.APIKey) != "",
		},
		"note": "可热更新参数见 GET/PUT /admin/settings",
	}
	if h.Settings != nil {
		out["runtime"] = h.Settings.Snapshot()
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func decodeJSON(r *http.Request, max int64, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, max))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
