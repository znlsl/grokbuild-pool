package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/proxypool"
)

// ProxyPool is the optional file-backed proxy node manager.
// Handlers.ProxyPool may be nil → routes return 503.
type ProxyPoolAPI interface {
	Path() string
	Snapshot() []proxypool.Node
	ReplaceAll(nodes []proxypool.Node) error
	Pick(accountID, mode string) (proxyURL, proxyMode string, ok bool)
	HealthyCount() int
	MarkFail(proxyURL, errMsg string)
	MarkOK(proxyURL string)
}

// GetProxyPool GET /admin/proxy-pool
func (h *Handlers) GetProxyPool(w http.ResponseWriter, r *http.Request) {
	if h.ProxyPool == nil {
		writeErr(w, http.StatusServiceUnavailable, "代理池未启用")
		return
	}
	enabled := false
	mode := "hash"
	require := false
	if h.Settings != nil {
		s := h.Settings.Snapshot()
		enabled = s.ProxyPoolEnabled
		mode = s.ProxyAssignMode
		require = s.RequireProxy
		if mode == "" {
			mode = "hash"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":             h.ProxyPool.Path(),
		"enabled":          enabled,
		"require_proxy":    require,
		"assign_mode":      mode,
		"healthy":          h.ProxyPool.HealthyCount(),
		"nodes":            h.ProxyPool.Snapshot(),
		"note":             "nodes 支持 http/https/socks5/socks5h；分配后写入账号 proxy_url 持久绑定",
	})
}

// PutProxyPool PUT /admin/proxy-pool — body: {"nodes":[...]}
func (h *Handlers) PutProxyPool(w http.ResponseWriter, r *http.Request) {
	if h.ProxyPool == nil {
		writeErr(w, http.StatusServiceUnavailable, "代理池未启用")
		return
	}
	var body struct {
		Nodes []proxypool.Node `json:"nodes"`
	}
	if err := decodeJSON(r, 2<<20, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.ProxyPool.ReplaceAll(body.Nodes); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"nodes":   h.ProxyPool.Snapshot(),
		"healthy": h.ProxyPool.HealthyCount(),
	})
}

// AssignProxyPool POST /admin/proxy-pool/assign
// 给无 proxy_url 的账号批量绑定池节点（持久化）。
// body 可选: {"limit":1000,"dry_run":false,"mode":"hash"}
func (h *Handlers) AssignProxyPool(w http.ResponseWriter, r *http.Request) {
	if h.ProxyPool == nil {
		writeErr(w, http.StatusServiceUnavailable, "代理池未启用")
		return
	}
	if h.Catalog == nil {
		writeErr(w, http.StatusServiceUnavailable, "账号库未启用")
		return
	}
	var body struct {
		Limit  int    `json:"limit"`
		DryRun bool   `json:"dry_run"`
		Mode   string `json:"mode"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Limit <= 0 {
		body.Limit = 5000
	}
	if body.Limit > 50000 {
		body.Limit = 50000
	}
	mode := strings.TrimSpace(body.Mode)
	if mode == "" && h.Settings != nil {
		mode = h.Settings.Snapshot().ProxyAssignMode
	}
	if mode == "" {
		mode = proxypool.AssignHash
	}

	assigned, skipped, failed := 0, 0, 0
	cursor := ""
	for assigned+skipped+failed < body.Limit {
		batch := 200
		if left := body.Limit - (assigned + skipped + failed); left < batch {
			batch = left
		}
		if batch <= 0 {
			break
		}
		rows, err := h.Catalog.ListAccounts(batch, cursor, catalog.AccountListFilter{})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(rows) == 0 {
			break
		}
		for _, a := range rows {
			cursor = a.ID
			if strings.TrimSpace(a.ProxyURL) != "" {
				skipped++
				continue
			}
			u, pm, ok := h.ProxyPool.Pick(a.ID, mode)
			if !ok || u == "" {
				failed++
				continue
			}
			if body.DryRun {
				assigned++
				continue
			}
			if err := h.Catalog.SetProxy(a.ID, u, pm); err != nil {
				failed++
				continue
			}
			if h.AccountHot != nil {
				if meta, ok := h.AccountHot.Get(a.ID); ok {
					meta.ProxyURL = u
					meta.ProxyMode = pm
					_, _ = h.AccountHot.Promote(meta)
				}
			}
			assigned++
		}
		if len(rows) < batch {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"assigned": assigned,
		"skipped":  skipped,
		"failed":   failed,
		"dry_run":  body.DryRun,
		"mode":     mode,
		"healthy":  h.ProxyPool.HealthyCount(),
	})
}
