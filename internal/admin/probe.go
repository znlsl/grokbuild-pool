package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/outbound"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/upstream"
)

// AccountProbeClient 账号测活所需的上游客户端工厂（*outbound.Factory 满足）。
type AccountProbeClient interface {
	ClientFor(accountID, proxyURL string) (*upstream.Client, error)
}

// ProbeAccount POST /admin/accounts/{id}/probe
// 用账号 access_token 拉上游 /billing（+ credits），写回 billing_json，返回脱敏额度与 probe 结果。
// 不因测活失败自动隔离/禁用；仅更新 last_error / billing 快照，供管理台决策。
func (h *Handlers) ProbeAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少账号 id")
		return
	}
	out, err := h.probeOne(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, catalog.ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, errNoAccessToken) {
			status = http.StatusBadRequest
		} else if errors.Is(err, errProbeNotConfigured) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// BatchProbeAccounts POST /admin/accounts/probe
// body: {"ids":["..."]}；缺省 ids 时不可用（防误扫全库）。
// 并发有界（默认 8），返回每条结果。
func (h *Handlers) BatchProbeAccounts(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		writeErr(w, http.StatusServiceUnavailable, "账号目录未启用")
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, 4<<20, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
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
	if len(ids) > 100 {
		writeErr(w, http.StatusBadRequest, "单次最多测活 100 个账号")
		return
	}

	type item struct {
		ID  string         `json:"id"`
		OK  bool           `json:"ok"`
		Err string         `json:"error,omitempty"`
		Res map[string]any `json:"result,omitempty"`
	}
	results := make([]item, len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, err := h.probeOne(ctx, id)
			if err != nil {
				results[i] = item{ID: id, OK: false, Err: err.Error()}
				return
			}
			ok := false
			if v, okb := out["probe_ok"].(bool); okb {
				ok = v
			}
			results[i] = item{ID: id, OK: ok, Res: out}
		}(i, id)
	}
	wg.Wait()
	okN, failN := 0, 0
	for _, it := range results {
		if it.OK {
			okN++
		} else {
			failN++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      okN,
		"failed":  failN,
		"total":   len(results),
		"results": results,
	})
}

var (
	errNoAccessToken     = errors.New("账号无 access_token，无法测活")
	errProbeNotConfigured = errors.New("上游测活客户端未配置")
)

func (h *Handlers) probeOne(ctx context.Context, id string) (map[string]any, error) {
	if h == nil || h.Catalog == nil {
		return nil, errors.New("账号目录未启用")
	}
	if h.Outbound == nil {
		return nil, errProbeNotConfigured
	}
	acc, err := h.Catalog.Get(id)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(acc.AccessToken)
	if token == "" {
		return nil, errNoAccessToken
	}
	cli, err := h.Outbound.ClientFor(acc.ID, acc.ProxyURL)
	if err != nil {
		return nil, fmt.Errorf("出站客户端: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	now := time.Now().Unix()
	snap, snapErr := cli.GetBillingSnapshot(pctx, token)

	probeOK := snapErr == nil
	probeStatus := 0
	probeErr := ""
	if snapErr != nil {
		probeErr = snapErr.Error()
		var he *upstream.HTTPStatusError
		if errors.As(snapErr, &he) && he != nil {
			probeStatus = he.StatusCode
		}
	}

	// 组装落库 JSON（兼容 BillingSnapshot + probe 元数据）
	store := map[string]any{
		"probed_at":  now,
		"updated_at": now,
		"probe_ok":   probeOK,
	}
	if probeStatus > 0 {
		store["probe_status"] = probeStatus
	}
	if probeErr != "" {
		store["probe_error"] = truncateStr(probeErr, 400)
	}
	if snap != nil {
		if snap.Monthly != nil {
			store["monthly"] = map[string]any{
				"monthlyLimit": snap.Monthly.MonthlyLimit,
				"used":         snap.Monthly.Used,
				"onDemandCap":  snap.Monthly.OnDemandCap,
			}
		}
		if snap.Weekly != nil {
			store["weekly"] = map[string]any{
				"creditUsagePercent": snap.Weekly.CreditUsagePercent,
				"billingPeriodEnd":   snap.Weekly.BillingPeriodEnd,
			}
		}
		store["grok_build"] = snap.GrokBuild
		if snap.MonthlyError != "" {
			store["monthly_error"] = snap.MonthlyError
		}
		if snap.WeeklyError != "" {
			store["weekly_error"] = snap.WeeklyError
		}
	}
	raw, _ := json.Marshal(store)
	billingStr := string(raw)

	// 健康补丁：写 billing；失败时记 last_error（不自动隔离）
	patch := catalog.HealthPatch{BillingJSON: &billingStr}
	if probeOK {
		patch.ClearLastError = true
		// 轻量成功计数 +1（测活也算一次可用探测）
		sc := acc.SuccessCount + 1
		patch.SuccessCount = &sc
		ls := now
		patch.LastSuccessAt = &ls
	} else {
		msg := "probe: " + truncateStr(probeErr, 200)
		patch.LastError = &msg
		// 401/403 记失败但不自动隔离（管理台可见）
		if probeStatus == http.StatusUnauthorized || probeStatus == http.StatusForbidden ||
			probeStatus == http.StatusPaymentRequired {
			fc := acc.FailureCount + 1
			patch.FailureCount = &fc
		}
	}
	if err := h.Catalog.PatchHealth(id, patch); err != nil {
		return nil, err
	}

	view := catalog.ParseAccountBillingView(billingStr)
	out := map[string]any{
		"id":         id,
		"probe_ok":   probeOK,
		"probed_at":  now,
		"proxy_url":  acc.ProxyURL,
		"proxy_mode": acc.ProxyMode,
	}
	if probeStatus > 0 {
		out["probe_status"] = probeStatus
	}
	if probeErr != "" {
		out["probe_error"] = truncateStr(probeErr, 400)
	}
	if view != nil {
		out["billing"] = view
	}
	// 额外额度字段扁平化，方便前端
	if snap != nil && snap.Monthly != nil {
		out["monthly_used"] = snap.Monthly.Used
		out["monthly_limit"] = snap.Monthly.MonthlyLimit
	}
	if snap != nil && snap.Weekly != nil {
		out["weekly_usage_percent"] = snap.Weekly.CreditUsagePercent
	}
	if snap != nil {
		out["grok_build"] = snap.GrokBuild
	}
	return out, nil
}

func truncateStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Ensure outbound.Factory 实现 AccountProbeClient（编译期检查用）。
var _ AccountProbeClient = (*outbound.Factory)(nil)
