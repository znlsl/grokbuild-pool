package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/hot"
	"github.com/yshgsh1343/grokbuild2api/internal/lease"
	"github.com/yshgsh1343/grokbuild2api/internal/refresh"
	"github.com/yshgsh1343/grokbuild2api/internal/selector"
)

// RuntimeSettings 管理台可编辑的全部运行参数（密钥类仅显示是否已配置，不明文回传）。
type RuntimeSettings struct {
	// —— 选号 / 热池 ——
	HotSize               int     `json:"hot_size"`
	MaxInflightPerAccount int32   `json:"max_inflight_per_account"`
	StickyTTLSec          int64   `json:"sticky_ttl_sec"`
	StickyMax             int     `json:"sticky_max"`
	Pow2K                 int     `json:"pow2_k"`
	WPriority             float64 `json:"w_priority"`
	WInflight             float64 `json:"w_inflight"`
	WFailure              float64 `json:"w_failure"`
	JitterAmp             float64 `json:"jitter_amp"`
	SelectorMaxAttempts   int     `json:"selector_max_attempts"`

	// —— 租约 / 防封号冷却 ——
	MaxAttempts                 int   `json:"max_attempts"`
	CooldownBaseSec             int64 `json:"cooldown_base_sec"`
	CooldownCapSec              int64 `json:"cooldown_cap_sec"`
	UnauthorizedCooldownSec     int64 `json:"unauthorized_cooldown_sec"`
	PaymentRequiredCooldownSec  int64 `json:"payment_required_cooldown_sec"`
	UnauthorizedQuarantineAfter int   `json:"unauthorized_quarantine_after"`
	ForbiddenCooldownSec        int64 `json:"forbidden_cooldown_sec"`
	ForbiddenQuarantineAfter    int   `json:"forbidden_quarantine_after"`
	CooldownJitterPct           int   `json:"cooldown_jitter_pct"`
	CooldownExpMax              int   `json:"cooldown_exp_max"`

	// —— 进程限制 ——
	MaxConcurrent     int   `json:"max_concurrent"`
	MaxBodyBytes      int64 `json:"max_body_bytes"`
	RequestTimeoutSec int   `json:"request_timeout_sec"`

	// —— 刷新 workers ——
	RefreshWorkers int     `json:"refresh_workers"`
	RefreshQPS     float64 `json:"refresh_qps"`
	RefreshSkewSec int64   `json:"refresh_skew_sec"`

	// —— 令牌创建默认模板 ——
	TokenDefaultRemainQuota   int64 `json:"token_default_remain_quota"`
	TokenDefaultMaxConcurrent int   `json:"token_default_max_concurrent"`
	TokenDefaultRPM           int   `json:"token_default_rpm"`
	TokenDefaultUnlimited     bool  `json:"token_default_unlimited"`

	// —— 只读展示（来自进程配置，热更不改绑定/密钥）——
	Listen             string `json:"listen"`
	AllowPublicListen  bool   `json:"allow_public_listen"`
	DataDir            string `json:"data_dir"`
	DBPath             string `json:"db_path"`
	MockUpstream       bool   `json:"mock_upstream"`
	UpstreamBaseURL    string `json:"upstream_base_url"`
	OAuthRefreshURL    string `json:"oauth_refresh_url"`
	APIKeyConfigured   bool   `json:"api_key_configured"`
	AdminKeyConfigured bool   `json:"admin_key_configured"`
	LoggingLevel       string `json:"logging_level"`
}

// SettingsSnapshot 为 GET 响应：运行时设置 + 可选持久化路径。
type SettingsSnapshot struct {
	RuntimeSettings
	// PersistedPath 非空时表示设置会原子写入该 JSON 文件。
	PersistedPath string `json:"persisted_path,omitempty"`
}

// SettingsController 持有可变运行时参数并应用到 hot/lease。
type SettingsController struct {
	mu sync.RWMutex
	s  RuntimeSettings

	// Path 持久化文件路径（如 data/settings.json）；空则不落盘。
	Path string

	Hot      *hot.Index
	Lease    *lease.Manager
	Selector *selector.Selector
	Refresh  *refresh.Service
	// SetGlobalMaxConcurrent 可选：更新 HTTP 全局并发（0 = 不限制）
	SetGlobalMaxConcurrent func(n int)
	// SetMaxBodyBytes 可选：热更新请求体上限（0 = 不限制）
	SetMaxBodyBytes func(n int64)
	// SetRequestTimeout 可选：热更新整请求超时（0 = 不设）
	SetRequestTimeout func(d time.Duration)
	// ProcessInfo 进程只读信息，每次 Apply 末尾强制盖写（避免 JSON 零值/客户端 PUT 冲掉）
	ProcessInfo RuntimeSettings
}

// Snapshot 返回当前设置副本（含可选 PersistedPath）。
func (c *SettingsController) Snapshot() SettingsSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return SettingsSnapshot{
		RuntimeSettings: c.s,
		PersistedPath:   c.Path,
	}
}

// Load 从 Path 读取 JSON 并覆盖内存设置（不触发 hot/lease 副作用；
// 调用方应在启动时 Load 后再 Apply 以应用运行时副作用）。
// 文件不存在时返回 nil（保持当前默认）。
func (c *SettingsController) Load() error {
	if c == nil {
		return nil
	}
	path := c.Path
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("settings: load %s: %w", path, err)
	}
	var in RuntimeSettings
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("settings: parse %s: %w", path, err)
	}
	c.mu.Lock()
	c.s = in
	c.mu.Unlock()
	return nil
}

// persistLocked 原子写入 JSON：先写临时文件再 rename。
// 调用方须在持锁或持有稳定快照后调用；本方法自行读 c.s。
func (c *SettingsController) persist() error {
	path := c.Path
	if path == "" {
		return nil
	}
	c.mu.RLock()
	snap := c.s
	c.mu.RUnlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: marshal: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("settings: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("settings: temp: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("settings: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("settings: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("settings: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("settings: rename: %w", err)
	}
	ok = true
	return nil
}

// Apply 校验并应用设置（部分参数即时生效）；成功后原子落盘。
func (c *SettingsController) Apply(in RuntimeSettings) (RuntimeSettings, error) {
	// 边界
	if in.MaxInflightPerAccount < 0 {
		in.MaxInflightPerAccount = 0
	}
	if in.MaxInflightPerAccount > 64 {
		in.MaxInflightPerAccount = 64
	}
	if in.CooldownBaseSec < 1 {
		in.CooldownBaseSec = 1
	}
	if in.CooldownCapSec < in.CooldownBaseSec {
		in.CooldownCapSec = in.CooldownBaseSec
	}
	if in.ForbiddenCooldownSec < 1 {
		in.ForbiddenCooldownSec = 1
	}
	if in.CooldownJitterPct < 0 {
		in.CooldownJitterPct = 0
	}
	if in.CooldownJitterPct > 50 {
		in.CooldownJitterPct = 50
	}
	if in.MaxConcurrent < 0 {
		in.MaxConcurrent = 0
	}
	if in.MaxConcurrent > 10000 {
		in.MaxConcurrent = 10000
	}
	if in.Pow2K < 1 {
		in.Pow2K = 1
	}
	if in.Pow2K > 16 {
		in.Pow2K = 16
	}
	if in.MaxAttempts < 1 {
		in.MaxAttempts = 1
	}
	if in.MaxAttempts > 32 {
		in.MaxAttempts = 32
	}
	if in.StickyMax < 0 {
		in.StickyMax = 0
	}
	if in.StickyMax > 1_000_000 {
		in.StickyMax = 1_000_000
	}
	if in.StickyTTLSec < 0 {
		in.StickyTTLSec = 0
	}
	if in.ForbiddenQuarantineAfter < 0 {
		in.ForbiddenQuarantineAfter = 0
	}
	if in.RefreshWorkers < 0 {
		in.RefreshWorkers = 0
	}
	if in.RefreshWorkers > 4 {
		in.RefreshWorkers = 4
	}
	if in.RefreshQPS < 0 {
		in.RefreshQPS = 0
	}
	if in.RefreshQPS > 1000 {
		in.RefreshQPS = 1000
	}
	if in.MaxBodyBytes < 0 {
		in.MaxBodyBytes = 0
	}
	if in.RequestTimeoutSec < 0 {
		in.RequestTimeoutSec = 0
	}
	// 保留只读展示字段：从旧快照合并（避免 PUT 丢 listen 等）
	prev := c.Snapshot().RuntimeSettings
	if in.Listen == "" {
		in.Listen = prev.Listen
	}
	if in.DataDir == "" {
		in.DataDir = prev.DataDir
	}
	if in.DBPath == "" {
		in.DBPath = prev.DBPath
	}
	if in.UpstreamBaseURL == "" && prev.UpstreamBaseURL != "" {
		in.UpstreamBaseURL = prev.UpstreamBaseURL
	}
	if in.OAuthRefreshURL == "" && prev.OAuthRefreshURL != "" {
		in.OAuthRefreshURL = prev.OAuthRefreshURL
	}
	if in.LoggingLevel == "" {
		in.LoggingLevel = prev.LoggingLevel
	}
	c.mu.Lock()
	// 强制盖写进程只读信息
	pi := c.ProcessInfo
	c.mu.Unlock()
	if pi.Listen != "" {
		in.Listen = pi.Listen
	}
	in.AllowPublicListen = pi.AllowPublicListen
	if pi.DataDir != "" {
		in.DataDir = pi.DataDir
	}
	if pi.DBPath != "" {
		in.DBPath = pi.DBPath
	}
	in.MockUpstream = pi.MockUpstream
	in.UpstreamBaseURL = pi.UpstreamBaseURL
	in.OAuthRefreshURL = pi.OAuthRefreshURL
	in.APIKeyConfigured = pi.APIKeyConfigured
	in.AdminKeyConfigured = pi.AdminKeyConfigured
	if pi.LoggingLevel != "" {
		in.LoggingLevel = pi.LoggingLevel
	}

	c.mu.Lock()
	c.s = in
	c.mu.Unlock()

	// 即时生效：账号 inflight 硬限
	if c.Hot != nil {
		c.Hot.SetMaxInflightPerAccount(in.MaxInflightPerAccount)
	}
	// 即时生效：租约冷却
	if c.Lease != nil {
		lc := c.Lease.Config()
		lc.MaxAttempts = in.MaxAttempts
		lc.CooldownBaseSec = in.CooldownBaseSec
		lc.CooldownCapSec = in.CooldownCapSec
		lc.UnauthorizedCooldownSec = in.UnauthorizedCooldownSec
		lc.PaymentRequiredCooldownSec = in.PaymentRequiredCooldownSec
		lc.UnauthorizedQuarantineAfter = in.UnauthorizedQuarantineAfter
		lc.ForbiddenCooldownSec = in.ForbiddenCooldownSec
		lc.ForbiddenQuarantineAfter = in.ForbiddenQuarantineAfter
		lc.CooldownJitterPct = in.CooldownJitterPct
		lc.CooldownExpMax = in.CooldownExpMax
		c.Lease.ApplyConfig(lc)
	}
	// 即时生效：选号权重 / sticky
	if c.Selector != nil {
		sc := c.Selector.Config()
		sc.HotSize = in.HotSize
		sc.StickyTTLSec = in.StickyTTLSec
		sc.StickyMax = in.StickyMax
		sc.Pow2K = in.Pow2K
		sc.MaxAttempts = in.SelectorMaxAttempts
		if sc.MaxAttempts <= 0 {
			sc.MaxAttempts = in.MaxAttempts
		}
		sc.WPriority = in.WPriority
		sc.WInflight = in.WInflight
		sc.WFailure = in.WFailure
		sc.JitterAmp = in.JitterAmp
		sc.MaxInflightPerAccount = in.MaxInflightPerAccount
		c.Selector.ApplyConfig(sc)
	}
	// 即时生效：刷新 QPS/Skew
	if c.Refresh != nil {
		rc := c.Refresh.Config()
		rc.Workers = in.RefreshWorkers
		rc.QPS = in.RefreshQPS
		rc.SkewSec = in.RefreshSkewSec
		c.Refresh.ApplyConfig(rc)
	}
	// HTTP 全局并发 / body / 超时（允许 0）
	if c.SetGlobalMaxConcurrent != nil {
		c.SetGlobalMaxConcurrent(in.MaxConcurrent)
	}
	if c.SetMaxBodyBytes != nil {
		c.SetMaxBodyBytes(in.MaxBodyBytes)
	}
	if c.SetRequestTimeout != nil {
		c.SetRequestTimeout(time.Duration(in.RequestTimeoutSec) * time.Second)
	}

	// 成功后原子落盘（失败不回滚内存，但返回错误以便调用方感知）
	if err := c.persist(); err != nil {
		return in, err
	}
	return in, nil
}

// GetSettings GET /admin/settings
func (h *Handlers) GetSettings(w http.ResponseWriter, r *http.Request) {
	if h.Settings == nil {
		writeErr(w, http.StatusServiceUnavailable, "设置控制器未启用")
		return
	}
	writeJSON(w, http.StatusOK, h.Settings.Snapshot())
}

// PutSettings PUT /admin/settings — 全量更新可热改参数
func (h *Handlers) PutSettings(w http.ResponseWriter, r *http.Request) {
	if h.Settings == nil {
		writeErr(w, http.StatusServiceUnavailable, "设置控制器未启用")
		return
	}
	var in RuntimeSettings
	if err := decodeJSON(r, 1<<20, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.Settings.Apply(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// persisted:true 表示已原子写入 Path（Path 为空时为 false，仅内存生效）
	persisted := strings.TrimSpace(h.Settings.Path) != ""
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"persisted": persisted,
		"settings":  out,
	})
}
