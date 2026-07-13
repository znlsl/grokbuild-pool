package refresh_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/mockup"
	"github.com/yshgsh1343/grokbuild2api/internal/refresh"
)

func openCat(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func nearExpiryAccount(id string, expiresAt int64) catalog.Account {
	now := time.Now().Unix()
	return catalog.Account{
		ID:           id,
		Revision:     1,
		IdentityKey:  "idk-" + id,
		Priority:     1,
		Enabled:      true,
		Lifecycle:    catalog.LifecycleActive,
		AccessToken:  "access-" + id,
		RefreshToken: "refresh-" + id,
		ExpiresAt:    expiresAt,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func TestEnsureFreshSingleflight(t *testing.T) {
	cat := openCat(t)
	now := time.Now().Unix()
	// Already inside skew window → will refresh.
	a := nearExpiryAccount("sf1", now+30)
	if err := cat.UpsertMany([]catalog.Account{a}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	mock := mockup.NewMockOAuth()
	// Slow enough that concurrent callers pile up on singleflight.
	mock.Latency = 50 * time.Millisecond
	oauth := refresh.NewMockOAuthAdapter(mock)

	cfg := refresh.DefaultConfig()
	cfg.SkewSec = 300
	cfg.QPS = 1000 // do not throttle this unit test
	svc := refresh.New(cat, oauth, cfg, nil, nil)

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.EnsureFresh(context.Background(), "sf1")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("EnsureFresh: %v", err)
		}
	}

	calls := mock.CallCount()
	if calls != 1 {
		t.Fatalf("oauth calls=%d want 1 (singleflight)", calls)
	}

	got, err := cat.Get("sf1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessToken == "access-sf1" {
		t.Fatal("access token should have been refreshed")
	}
	// UpdateTokens bumps rev; success policy PatchHealth may bump again.
	if got.Revision < 2 {
		t.Fatalf("revision=%d want >=2", got.Revision)
	}
}

func TestEnsureFreshAlreadyFresh(t *testing.T) {
	cat := openCat(t)
	now := time.Now().Unix()
	a := nearExpiryAccount("fresh1", now+7200)
	if err := cat.UpsertMany([]catalog.Account{a}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}
	mock := mockup.NewMockOAuth()
	svc := refresh.New(cat, refresh.NewMockOAuthAdapter(mock), refresh.DefaultConfig(), nil, nil)

	set, err := svc.EnsureFresh(context.Background(), "fresh1")
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if set.AccessToken != "access-fresh1" {
		t.Fatalf("token=%q", set.AccessToken)
	}
	if mock.CallCount() != 0 {
		t.Fatalf("oauth calls=%d want 0", mock.CallCount())
	}
	_, _, hit := svc.Stats()
	if hit != 1 {
		t.Fatalf("ensureHit=%d want 1", hit)
	}
}

func TestRefreshFailureAppliesCooldownAndQuarantine(t *testing.T) {
	cat := openCat(t)
	now := time.Now().Unix()
	a := nearExpiryAccount("bad1", now+10)
	if err := cat.UpsertMany([]catalog.Account{a}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	mock := mockup.NewMockOAuth()
	mock.FailAll = errors.New("oauth 401 invalid_grant")
	cfg := refresh.DefaultConfig()
	cfg.SkewSec = 300
	cfg.QPS = 1000
	cfg.CooldownOnFailSec = 90
	cfg.QuarantineAfter = 3
	svc := refresh.New(cat, refresh.NewMockOAuthAdapter(mock), cfg, nil, nil)

	for i := 0; i < 3; i++ {
		_, err := svc.EnsureFresh(context.Background(), "bad1")
		if err == nil {
			t.Fatalf("iter %d: expected error", i)
		}
		// After first failure account is cooling; still force refresh path by
		// re-writing expires_at into the skew window without clearing cooldown.
		// EnsureFresh only looks at expires_at, so it will still attempt refresh.
		// But revision may bump from PatchHealth — Get uses latest.
	}

	got, err := cat.Get("bad1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CooldownUntil <= now {
		t.Fatalf("cooldown_until=%d want > now=%d", got.CooldownUntil, now)
	}
	if got.FailureCount < 3 {
		t.Fatalf("failure_count=%d want >=3", got.FailureCount)
	}
	if got.Lifecycle != catalog.LifecycleQuarantined {
		t.Fatalf("lifecycle=%q want quarantined", got.Lifecycle)
	}
	if got.Enabled {
		t.Fatal("enabled should be false after quarantine")
	}
	if got.LastError == "" {
		t.Fatal("last_error should be set")
	}
	ok, fail, _ := svc.Stats()
	if fail < 3 || ok != 0 {
		t.Fatalf("stats ok=%d fail=%d", ok, fail)
	}
}

func TestCASConflictRetry(t *testing.T) {
	cat := openCat(t)
	now := time.Now().Unix()
	a := nearExpiryAccount("cas1", now+10)
	if err := cat.UpsertMany([]catalog.Account{a}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	mock := mockup.NewMockOAuth()
	// Inject a concurrent CAS bump just as Refresh returns.
	var once sync.Once
	mock.OnRefresh = func(refreshToken string) {
		once.Do(func() {
			// Another writer advances revision under us.
			_ = cat.UpdateTokens("cas1", 1, catalog.TokenSet{
				AccessToken:  "peer-access",
				RefreshToken: "peer-refresh",
				ExpiresAt:    now + 30, // still inside skew → retry path updates
			})
		})
	}

	cfg := refresh.DefaultConfig()
	cfg.SkewSec = 300
	cfg.QPS = 1000
	svc := refresh.New(cat, refresh.NewMockOAuthAdapter(mock), cfg, nil, nil)

	set, err := svc.EnsureFresh(context.Background(), "cas1")
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	// Either peer tokens (if peer left us outside skew — not here) or mock tokens after retry.
	if set.AccessToken == "" {
		t.Fatal("empty access token")
	}
	got, _ := cat.Get("cas1")
	// Peer wrote rev 2 with expires still in skew; service retries with rev 2.
	if got.Revision < 2 {
		t.Fatalf("revision=%d want >=2", got.Revision)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("calls=%d want 1", mock.CallCount())
	}
}

func TestRefresh1000UnderQPS(t *testing.T) {
	if testing.Short() {
		t.Skip("skip 1000-account refresh in -short")
	}
	cat := openCat(t)
	now := time.Now().Unix()
	const n = 1000
	accounts := make([]catalog.Account, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("acc-%04d", i)
		// Spread slightly inside skew so all need refresh.
		accounts[i] = nearExpiryAccount(id, now+int64(i%60)+1)
	}
	if err := cat.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	mock := mockup.NewMockOAuth()
	cfg := refresh.DefaultConfig()
	cfg.Workers = 4
	cfg.QPS = 30
	cfg.Burst = 5
	cfg.SkewSec = 300
	cfg.ScanLimit = n
	svc := refresh.New(cat, refresh.NewMockOAuthAdapter(mock), cfg, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	start := time.Now()
	okCount, err := svc.RefreshExpiring(ctx, n)
	elapsed := time.Since(start)
	if err != nil && okCount < n {
		// Partial is only ok if context cancelled; we budget 90s for 1000@30qps ≈ 34s.
		t.Fatalf("RefreshExpiring: ok=%d err=%v elapsed=%s", okCount, err, elapsed)
	}
	if okCount != n {
		t.Fatalf("refreshed %d want %d (elapsed=%s calls=%d)", okCount, n, elapsed, mock.CallCount())
	}
	calls := mock.CallCount()
	if calls != int64(n) {
		t.Fatalf("oauth calls=%d want %d", calls, n)
	}

	// QPS cap: 1000 at 30/s needs ≥ ~33s wall; allow some burst headroom.
	// With burst=5, theoretical min ≈ (1000-5)/30 ≈ 33.2s.
	minExpected := 25 * time.Second
	if elapsed < minExpected {
		t.Fatalf("elapsed=%s too fast for QPS=30 (min expected ~%s); limiter may be ineffective", elapsed, minExpected)
	}
	// Upper bound generous for slow CI/host.
	if elapsed > 80*time.Second {
		t.Fatalf("elapsed=%s too slow", elapsed)
	}

	// Spot-check tokens persisted.
	got, err := cat.Get("acc-0000")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessToken == "access-acc-0000" {
		t.Fatal("token not updated")
	}
	if got.Revision < 2 {
		t.Fatalf("revision=%d", got.Revision)
	}

	t.Logf("refreshed %d accounts in %s (%.1f/s effective, oauth_calls=%d)",
		n, elapsed, float64(n)/elapsed.Seconds(), calls)
}

func TestBackgroundWorkersRefresh(t *testing.T) {
	cat := openCat(t)
	now := time.Now().Unix()
	const n = 20
	accounts := make([]catalog.Account, n)
	for i := 0; i < n; i++ {
		accounts[i] = nearExpiryAccount(fmt.Sprintf("bg-%02d", i), now+5)
	}
	if err := cat.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	mock := mockup.NewMockOAuth()
	cfg := refresh.DefaultConfig()
	cfg.Workers = 2
	cfg.QPS = 100
	cfg.SkewSec = 300
	cfg.ScanInterval = 50 * time.Millisecond
	cfg.ScanLimit = n
	svc := refresh.New(cat, refresh.NewMockOAuthAdapter(mock), cfg, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mock.CallCount() >= int64(n) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mock.CallCount() < int64(n) {
		t.Fatalf("background oauth calls=%d want >=%d", mock.CallCount(), n)
	}
}

func TestFailurePolicyHookOnly(t *testing.T) {
	cat := openCat(t)
	now := time.Now().Unix()
	a := nearExpiryAccount("hook1", now+5)
	if err := cat.UpsertMany([]catalog.Account{a}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	var failCalls atomic.Int64
	policy := refresh.FuncPolicy{
		Fail: func(ctx context.Context, cat *catalog.Catalog, accountID string, err error) error {
			failCalls.Add(1)
			// Apply a known cooldown via PatchHealth to prove hook runs.
			until := time.Now().Unix() + 42
			fc := 1
			return cat.PatchHealth(accountID, catalog.HealthPatch{
				CooldownUntil: &until,
				FailureCount:  &fc,
				LastError:     strPtr("hooked"),
			})
		},
	}

	mock := mockup.NewMockOAuth()
	mock.FailAll = errors.New("boom")
	cfg := refresh.DefaultConfig()
	cfg.QPS = 1000
	// Pass custom fail policy; success nil → default (unused on fail path).
	svc := refresh.New(cat, refresh.NewMockOAuthAdapter(mock), cfg, policy, refresh.NewDefaultPolicy(cfg))

	_, err := svc.EnsureFresh(context.Background(), "hook1")
	if err == nil {
		t.Fatal("expected error")
	}
	if failCalls.Load() != 1 {
		t.Fatalf("fail hook calls=%d", failCalls.Load())
	}
	got, _ := cat.Get("hook1")
	if got.LastError != "hooked" {
		t.Fatalf("last_error=%q", got.LastError)
	}
	if got.CooldownUntil == 0 {
		t.Fatal("cooldown not applied by hook")
	}
}

func TestWorkersClamped(t *testing.T) {
	cfg := refresh.Config{Workers: 1, QPS: 10}
	cfg = refresh.DefaultConfig() // sanity
	_ = cfg
	// Construct with out-of-range workers via New.
	cat := openCat(t)
	mock := mockup.NewMockOAuth()
	s := refresh.New(cat, refresh.NewMockOAuthAdapter(mock), refresh.Config{Workers: 99, QPS: 5}, nil, nil)
	if s.Config().Workers != refresh.MaxWorkers {
		t.Fatalf("workers=%d want %d", s.Config().Workers, refresh.MaxWorkers)
	}
	s2 := refresh.New(cat, refresh.NewMockOAuthAdapter(mock), refresh.Config{Workers: 0}, nil, nil)
	if s2.Config().Workers < refresh.MinWorkers || s2.Config().Workers > refresh.MaxWorkers {
		t.Fatalf("workers=%d out of range", s2.Config().Workers)
	}
}

func strPtr(s string) *string { return &s }


func TestForceRefreshAlwaysHitsNetwork(t *testing.T) {
	cat := openCat(t)
	now := time.Now().Unix()
	// Far from expiry — EnsureFresh would short-circuit; ForceRefresh must still network.
	a := nearExpiryAccount("force1", now+3600)
	if err := cat.UpsertMany([]catalog.Account{a}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}
	mock := mockup.NewMockOAuth()
	oauth := refresh.NewMockOAuthAdapter(mock)
	cfg := refresh.DefaultConfig()
	cfg.SkewSec = 300
	cfg.QPS = 1000
	svc := refresh.New(cat, oauth, cfg, nil, nil)

	// EnsureFresh should hit cache.
	if _, err := svc.EnsureFresh(context.Background(), "force1"); err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if mock.CallCount() != 0 {
		t.Fatalf("EnsureFresh should not network when fresh, calls=%d", mock.CallCount())
	}

	set, err := svc.ForceRefresh(context.Background(), "force1")
	if err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	if set.AccessToken == "" {
		t.Fatal("empty access after ForceRefresh")
	}
	if mock.CallCount() != 1 {
		t.Fatalf("ForceRefresh must hit network, calls=%d", mock.CallCount())
	}
}
