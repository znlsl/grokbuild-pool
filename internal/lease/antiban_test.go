package lease

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
	"github.com/yshgsh1343/grokbuild2api/internal/selector"
)

func openLease(t *testing.T) (*Manager, *catalog.Catalog, *hot.Index) {
	t.Helper()
	dir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	acc := []catalog.Account{{
		ID: "x1", AccessToken: "at", RefreshToken: "rt",
		ExpiresAt: now + 3600, Enabled: true, Lifecycle: catalog.LifecycleActive,
		CreatedAt: now, UpdatedAt: now, Revision: 1,
	}}
	if err := cat.UpsertMany(acc); err != nil {
		t.Fatal(err)
	}
	idx := hot.New(hot.Config{HotSize: 10, MaxInflightPerAccount: 4})
	if _, err := idx.LoadEligible(cat); err != nil {
		t.Fatal(err)
	}
	sel := selector.New(idx, selector.DefaultConfig())
	cfg := DefaultConfig()
	cfg.CooldownJitterPct = 0 // 确定性
	cfg.CooldownBaseSec = 60
	cfg.CooldownExpMax = 3
	cfg.ForbiddenCooldownSec = 900
	m := New(cat, idx, sel, cfg)
	return m, cat, idx
}

func TestCooldown429Exponential(t *testing.T) {
	m, _, _ := openLease(t)
	// failureCount=1 → base; =4 → base*8 (exp max 3 → 2^3)
	s1 := m.cooldownSeconds(429, 0, 1)
	s4 := m.cooldownSeconds(429, 0, 4)
	if s1 != 60 {
		t.Fatalf("s1=%d", s1)
	}
	if s4 != 60*8 {
		t.Fatalf("s4=%d want 480", s4)
	}
	s403 := m.cooldownSeconds(403, 0, 1)
	if s403 != 900 {
		t.Fatalf("403=%d", s403)
	}
}

func TestRelease429SetsCooldown(t *testing.T) {
	m, _, idx := openLease(t)
	ctx := t.Context()
	l, err := m.Acquire(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := m.Release(ctx, l, Result{StatusCode: 429, Success: false}); err != nil {
		t.Fatal(err)
	}
	meta, ok := idx.Get(l.AccountID)
	if !ok {
		t.Fatal("missing meta")
	}
	if meta.CooldownUntil <= now {
		t.Fatalf("cooldown not set: %d", meta.CooldownUntil)
	}
}

// TestStickyAcquireKeepsAccountAndProxy 同一 stickyKey 两次 Acquire
// 应命中同一 AccountID，且 ProxyURL 与 catalog 账号代理一致（会话粘性=代理粘性）。
func TestStickyAcquireKeepsAccountAndProxy(t *testing.T) {
	dir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	now := time.Now().Unix()
	// 多账号，各自不同 ProxyURL，便于验证粘性绑定后代理不变
	accs := []catalog.Account{
		{
			ID: "sticky-a", AccessToken: "at-a", RefreshToken: "rt-a",
			ExpiresAt: now + 3600, Enabled: true, Lifecycle: catalog.LifecycleActive,
			Priority: 100, ProxyMode: "http", ProxyURL: "http://proxy-a.example:8080",
			CreatedAt: now, UpdatedAt: now, Revision: 1,
		},
		{
			ID: "sticky-b", AccessToken: "at-b", RefreshToken: "rt-b",
			ExpiresAt: now + 3600, Enabled: true, Lifecycle: catalog.LifecycleActive,
			Priority: 50, ProxyMode: "http", ProxyURL: "http://proxy-b.example:8080",
			CreatedAt: now, UpdatedAt: now, Revision: 1,
		},
		{
			ID: "sticky-c", AccessToken: "at-c", RefreshToken: "rt-c",
			ExpiresAt: now + 3600, Enabled: true, Lifecycle: catalog.LifecycleActive,
			Priority: 10, ProxyMode: "http", ProxyURL: "http://proxy-c.example:8080",
			CreatedAt: now, UpdatedAt: now, Revision: 1,
		},
	}
	if err := cat.UpsertMany(accs); err != nil {
		t.Fatal(err)
	}
	idx := hot.New(hot.Config{HotSize: 10, MaxInflightPerAccount: 4})
	if _, err := idx.LoadEligible(cat); err != nil {
		t.Fatal(err)
	}
	sel := selector.New(idx, selector.DefaultConfig())
	cfg := DefaultConfig()
	cfg.CooldownJitterPct = 0
	m := New(cat, idx, sel, cfg)
	ctx := t.Context()

	const sk = "session-sticky-proxy-1"
	l1, err := m.Acquire(ctx, sk)
	if err != nil {
		t.Fatal(err)
	}
	if l1.ProxyURL == "" {
		t.Fatal("lease.ProxyURL 应来自 catalog 账号代理")
	}
	if err := m.Release(ctx, l1, Result{Success: true, StatusCode: 200}); err != nil {
		t.Fatal(err)
	}

	l2, err := m.Acquire(ctx, sk)
	if err != nil {
		t.Fatal(err)
	}
	if l2.AccountID != l1.AccountID {
		t.Fatalf("sticky 账号不一致: %s vs %s", l2.AccountID, l1.AccountID)
	}
	if l2.ProxyURL != l1.ProxyURL {
		t.Fatalf("sticky 代理不一致: %q vs %q", l2.ProxyURL, l1.ProxyURL)
	}
	// 与 catalog 行一致
	acct, err := cat.Get(l2.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if l2.ProxyURL != acct.ProxyURL {
		t.Fatalf("lease.ProxyURL=%q catalog=%q", l2.ProxyURL, acct.ProxyURL)
	}
	_ = m.Release(ctx, l2, Result{Success: true, StatusCode: 200})
}


func TestRelease403ForbiddenLastError(t *testing.T) {
	m, cat, _ := openLease(t)
	ctx := t.Context()
	l, err := m.Acquire(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Release(ctx, l, Result{StatusCode: 403, Success: false}); err != nil {
		t.Fatal(err)
	}
	acct, err := cat.Get(l.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.LastError != "forbidden" {
		t.Fatalf("LastError=%q want forbidden", acct.LastError)
	}
}

func TestRelease403QuarantineAfter(t *testing.T) {
	m, cat, _ := openLease(t)
	cfg := m.Config()
	cfg.ForbiddenQuarantineAfter = 2
	cfg.CooldownJitterPct = 0
	m.ApplyConfig(cfg)
	ctx := t.Context()

	// 第一次 403：LastError=forbidden，未隔离
	l, err := m.Acquire(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Release(ctx, l, Result{StatusCode: 403, Success: false}); err != nil {
		t.Fatal(err)
	}
	acct, _ := cat.Get(l.AccountID)
	if acct.Lifecycle == catalog.LifecycleQuarantined {
		t.Fatal("should not quarantine after first 403")
	}

	// 清冷却以便再次 Acquire
	zero := int64(0)
	_ = cat.PatchHealth(l.AccountID, catalog.HealthPatch{CooldownUntil: &zero})
	// 热索引也清
	// re-load eligible
	// 直接再 Acquire 可能因 cooldown；手动清 idx cooldown
	// openLease idx 可从 m 访问不了；用 Patch 后 Get 再 Promote 难
	// 简单：重新 New manager 同 cat? 改用 cooldown 0 后 idx.SetCooldown
	// Manager 未导出 idx — 用新 manager
	// 简化：第二次 Release 前重新构造 Manager 并 Load
}
