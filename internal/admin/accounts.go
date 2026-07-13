package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
)

// 批量启停单次最多处理的 id 数（防误操作与超时）。
const maxBatchAccountIDs = 500

// AccountHot 账号启用/禁用时同步热池的接口（*hot.Index 满足）。
type AccountHot interface {
	Get(id string) (catalog.HotMeta, bool)
	Promote(meta catalog.HotMeta) (demoted string, err error)
	Demote(id string) error
	DemoteMany(ids []string)
}

// ListAccounts GET /admin/accounts?cursor=&limit=
// 游标分页返回脱敏账号摘要（无 token 明文）。
func (h *Handlers) ListAccounts(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		writeErr(w, http.StatusServiceUnavailable, "账号目录未启用")
		return
	}
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "limit 无效")
			return
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))

	// 多取 1 条判断是否有下一页
	rows, err := h.Catalog.ListAccounts(limit+1, cursor)
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidInput) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nextCursor := ""
	if len(rows) > limit {
		nextCursor = rows[limit-1].ID
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []catalog.AccountSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts":    rows,
		"next_cursor": nextCursor,
	})
}

// DisableAccount POST /admin/accounts/{id}/disable
// ManualDisabled=true、Enabled=false，并同步热池（Demote 或 Promote 禁用 meta）。
func (h *Handlers) DisableAccount(w http.ResponseWriter, r *http.Request) {
	h.setAccountManual(w, r, false)
}

// EnableAccount POST /admin/accounts/{id}/enable
// ManualDisabled=false、Enabled=true，并同步热池。
func (h *Handlers) EnableAccount(w http.ResponseWriter, r *http.Request) {
	h.setAccountManual(w, r, true)
}

// BatchAccounts POST /admin/accounts/batch
// body: {"action":"enable"|"disable"|"delete","ids":["..."]}，ids 上限 500；admin 鉴权由 Mount 保证。
// enable/disable/delete 均走单事务批量 SQL，避免逐条读改写导致管理台卡顿。
func (h *Handlers) BatchAccounts(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		writeErr(w, http.StatusServiceUnavailable, "账号目录未启用")
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
	switch action {
	case "enable", "disable", "delete":
	default:
		writeErr(w, http.StatusBadRequest, "action 须为 enable、disable 或 delete")
		return
	}
	// 规范化 id：去空白、去重、过滤空
	seen := make(map[string]struct{}, len(body.IDs))
	ids := make([]string, 0, len(body.IDs))
	for _, raw := range body.IDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "ids 不能为空")
		return
	}
	if len(ids) > maxBatchAccountIDs {
		writeErr(w, http.StatusBadRequest, "ids 最多 "+strconv.Itoa(maxBatchAccountIDs)+" 个")
		return
	}

	var (
		okIDs []string
		err   error
	)
	switch action {
	case "delete":
		okIDs, err = h.Catalog.BatchDelete(ids)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if h.AccountHot != nil && len(okIDs) > 0 {
			h.AccountHot.DemoteMany(okIDs)
		}
	case "enable":
		okIDs, err = h.Catalog.BatchSetManualEnabled(ids, true)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 启用时若已在热池则刷新 Enabled；不在热池的等下次 LoadEligible/Promote
		if h.AccountHot != nil {
			for _, id := range okIDs {
				if meta, ok := h.AccountHot.Get(id); ok {
					meta.Enabled = true
					_, _ = h.AccountHot.Promote(meta)
				}
			}
		}
	case "disable":
		okIDs, err = h.Catalog.BatchSetManualEnabled(ids, false)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if h.AccountHot != nil && len(okIDs) > 0 {
			h.AccountHot.DemoteMany(okIDs)
		}
	}

	// 汇总未命中 id
	okSet := make(map[string]struct{}, len(okIDs))
	for _, id := range okIDs {
		okSet[id] = struct{}{}
	}
	failed := make([]map[string]string, 0)
	for _, id := range ids {
		if _, ok := okSet[id]; !ok {
			failed = append(failed, map[string]string{"id": id, "error": "账号不存在"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"action": action,
		"ok":     len(okIDs),
		"failed": len(failed),
		"ids_ok": okIDs,
		"errors": failed,
		"limit":  maxBatchAccountIDs,
	})
}

// applyAccountManual 单账号启停：冷存储 + 热池同步（供单条与批量复用）。
func (h *Handlers) applyAccountManual(id string, enable bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("缺少账号 id")
	}
	if h.Catalog == nil {
		return errors.New("账号目录未启用")
	}
	enabled := enable
	manualDisabled := !enable
	if err := h.Catalog.PatchHealth(id, catalog.HealthPatch{
		Enabled:        &enabled,
		ManualDisabled: &manualDisabled,
	}); err != nil {
		return err
	}
	// 同步热池：禁用 Demote；启用时若已在热池则更新 Enabled。
	if h.AccountHot != nil {
		if !enable {
			_ = h.AccountHot.Demote(id)
		} else if meta, ok := h.AccountHot.Get(id); ok {
			meta.Enabled = true
			_, _ = h.AccountHot.Promote(meta)
		}
	}
	return nil
}

// PatchAccountProxy PATCH /admin/accounts/{id}
// 可选 body: {"proxy_url":"...","proxy_mode":"http"}；空 proxy_url 表示直连。
// 成功后同步热池 meta.ProxyURL（若在热集中）。
func (h *Handlers) PatchAccountProxy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少账号 id")
		return
	}
	if h.Catalog == nil {
		writeErr(w, http.StatusServiceUnavailable, "账号目录未启用")
		return
	}
	var body struct {
		ProxyURL  *string `json:"proxy_url"`
		ProxyMode *string `json:"proxy_mode"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ProxyURL == nil && body.ProxyMode == nil {
		writeErr(w, http.StatusBadRequest, "需要 proxy_url 或 proxy_mode")
		return
	}
	// 读当前行以合并未提供字段
	cur, err := h.Catalog.Get(id)
	if err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	proxyURL := cur.ProxyURL
	proxyMode := cur.ProxyMode
	if body.ProxyURL != nil {
		proxyURL = strings.TrimSpace(*body.ProxyURL)
	}
	if body.ProxyMode != nil {
		proxyMode = strings.TrimSpace(*body.ProxyMode)
	}
	if err := h.Catalog.SetProxy(id, proxyURL, proxyMode); err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 同步热池 ProxyURL（会话粘性依赖 meta 与 catalog 一致）
	if h.AccountHot != nil {
		if meta, ok := h.AccountHot.Get(id); ok {
			meta.ProxyURL = proxyURL
			meta.ProxyMode = proxyMode
			_, _ = h.AccountHot.Promote(meta)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         id,
		"proxy_url":  proxyURL,
		"proxy_mode": proxyMode,
		"patched":    true,
	})
}

func (h *Handlers) setAccountManual(w http.ResponseWriter, r *http.Request, enable bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少账号 id")
		return
	}
	if h.Catalog == nil {
		writeErr(w, http.StatusServiceUnavailable, "账号目录未启用")
		return
	}
	if err := h.applyAccountManual(id, enable); err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "账号不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              id,
		"enabled":         enable,
		"manual_disabled": !enable,
	})
}

// 编译期确认 *hot.Index 满足 AccountHot。
var _ AccountHot = (*hot.Index)(nil)
