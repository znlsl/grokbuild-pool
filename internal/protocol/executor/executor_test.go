package executor_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
	"github.com/yshgsh1343/grokbuild2api/internal/lease"
	"github.com/yshgsh1343/grokbuild2api/internal/mockup"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/executor"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/lb"
	"github.com/yshgsh1343/grokbuild2api/internal/selector"
)

func setupLease(t *testing.T, n int) *lease.Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pool.db")
	cat, err := catalog.Open(path)
	if err != nil {
		t.Fatalf("catalog open: %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })

	now := time.Now().Unix()
	accts := make([]catalog.Account, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("acct-%04d", i)
		accts = append(accts, catalog.Account{
			ID:           id,
			Revision:     1,
			IdentityKey:  "idk-" + id,
			Email:        id + "@example.com",
			Name:         "user-" + id,
			Priority:     (n - i) * 10,
			Enabled:      true,
			Lifecycle:    catalog.LifecycleActive,
			AccessToken:  "tok-" + id,
			RefreshToken: "ref-" + id,
			ExpiresAt:    now + 3600,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}
	if err := cat.UpsertMany(accts); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	idx := hot.New(hot.Config{HotSize: n})
	loaded, err := idx.LoadEligible(cat)
	if err != nil {
		t.Fatalf("LoadEligible: %v", err)
	}
	if loaded == 0 {
		t.Fatal("hot set empty")
	}

	scfg := selector.DefaultConfig()
	scfg.JitterAmp = 0
	sel := selector.New(idx, scfg)
	return lease.New(cat, idx, sel, lease.DefaultConfig())
}

func TestExecutor_MockUpstream200(t *testing.T) {
	mgr := setupLease(t, 3)
	mock := mockup.NewResponsesServer()
	t.Cleanup(mock.Close)

	ex := &executor.Executor{
		Leaser:   mgr,
		Upstream: &mockup.Poster{Client: mock.Client()},
	}

	resp, err := ex.Post(context.Background(), "grok-4", "conv-1", []byte(`{"model":"grok-4","input":"hi"}`), false)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) == 0 {
		t.Fatal("empty body")
	}
	if mock.Hits() != 1 {
		t.Fatalf("hits=%d want 1", mock.Hits())
	}
	reqs := mock.Requests()
	if len(reqs) != 1 || reqs[0].AccessToken == "" {
		t.Fatalf("expected token on request: %+v", reqs)
	}
	if len(reqs[0].AccessToken) < 4 || reqs[0].AccessToken[:4] != "tok-" {
		t.Fatalf("unexpected token %q", reqs[0].AccessToken)
	}
}

func TestExecutor_StreamBytesWrittenNoSecondAcquire(t *testing.T) {
	mgr := setupLease(t, 2)
	mock := mockup.NewResponsesServer()
	t.Cleanup(mock.Close)

	ex := &executor.Executor{
		Leaser:      mgr,
		Upstream:    &mockup.Poster{Client: mock.Client()},
		MaxAttempts: 4,
	}

	resp, err := ex.Post(context.Background(), "grok-4", "stream-conv", []byte(`{"model":"grok-4","stream":true,"input":"hi"}`), true)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	buf := make([]byte, 8)
	n, _ := resp.Body.Read(buf)
	if n == 0 {
		t.Fatal("expected stream bytes")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if got := ex.AcquireCount(); got != 1 {
		t.Fatalf("AcquireCount=%d want 1 (no second Acquire after bytes written)", got)
	}
	if mock.Hits() != 1 {
		t.Fatalf("upstream hits=%d want 1", mock.Hits())
	}
}

func TestExecutor_FailoverOn429Then200(t *testing.T) {
	mgr := setupLease(t, 3)
	mock := mockup.NewResponsesServer()
	t.Cleanup(mock.Close)
	mock.FailN = 1 // first request 429, then 200

	ex := &executor.Executor{
		Leaser:      mgr,
		Upstream:    &mockup.Poster{Client: mock.Client()},
		MaxAttempts: 4,
	}

	resp, err := ex.Post(context.Background(), "grok-4", "fail-over", []byte(`{"model":"grok-4","input":"x"}`), false)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if ex.AcquireCount() < 2 {
		t.Fatalf("AcquireCount=%d want >=2 (failover)", ex.AcquireCount())
	}
	if mock.Hits() < 2 {
		t.Fatalf("hits=%d want >=2", mock.Hits())
	}
}

func TestExecutor_NoAccountMapsToErrNoCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	cat, err := catalog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	idx := hot.New(hot.Config{HotSize: 10})
	sel := selector.New(idx, selector.DefaultConfig())
	mgr := lease.New(cat, idx, sel, lease.DefaultConfig())

	mock := mockup.NewResponsesServer()
	t.Cleanup(mock.Close)

	ex := &executor.Executor{
		Leaser:   mgr,
		Upstream: &mockup.Poster{Client: mock.Client()},
	}
	_, err = ex.Post(context.Background(), "m", "c", []byte(`{}`), false)
	if !errors.Is(err, lb.ErrNoCredential) {
		t.Fatalf("err=%v want lb.ErrNoCredential", err)
	}
	if mock.Hits() != 0 {
		t.Fatalf("should not hit upstream")
	}
}

func TestExecutor_FailoverOn402(t *testing.T) {
	mgr := setupLease(t, 3)
	mock := mockup.NewResponsesServer()
	t.Cleanup(mock.Close)
	mock.FailStatus = 402
	mock.FailN = 1 // first account 402, then 200

	ex := &executor.Executor{
		Leaser:      mgr,
		Upstream:    &mockup.Poster{Client: mock.Client()},
		MaxAttempts: 4,
	}

	resp, err := ex.Post(context.Background(), "grok-4", "pay-over", []byte(`{"model":"grok-4","input":"x"}`), false)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if ex.AcquireCount() < 2 {
		t.Fatalf("AcquireCount=%d want >=2 (402 failover)", ex.AcquireCount())
	}
	if mock.Hits() < 2 {
		t.Fatalf("hits=%d want >=2", mock.Hits())
	}
}

// mockRefresher implements executor.Refresher for unit tests.
type mockRefresher struct {
	ensureToken string
	forceToken  string
	forceCalls  int
	ensureCalls int
	forceErr    error
}

func (m *mockRefresher) EnsureFresh(_ context.Context, _ string) (string, uint64, error) {
	m.ensureCalls++
	if m.ensureToken != "" {
		return m.ensureToken, 2, nil
	}
	return "ensure-tok", 2, nil
}

func (m *mockRefresher) ForceRefresh(_ context.Context, _ string) (string, uint64, error) {
	m.forceCalls++
	if m.forceErr != nil {
		return "", 0, m.forceErr
	}
	if m.forceToken != "" {
		return m.forceToken, 3, nil
	}
	return "force-tok", 3, nil
}

func TestExecutor_401ForceRefreshSameAccount(t *testing.T) {
	mgr := setupLease(t, 2)
	mock := mockup.NewResponsesServer()
	t.Cleanup(mock.Close)
	// First post 401, second (after ForceRefresh) 200 — same account, no second Acquire.
	mock.FailStatus = 401
	mock.FailN = 1

	ref := &mockRefresher{forceToken: "refreshed-access"}
	ex := &executor.Executor{
		Leaser:      mgr,
		Upstream:    &mockup.Poster{Client: mock.Client()},
		Refresher:   ref,
		MaxAttempts: 4,
	}

	resp, err := ex.Post(context.Background(), "grok-4", "auth-retry", []byte(`{"model":"grok-4","input":"x"}`), false)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if ref.forceCalls != 1 {
		t.Fatalf("ForceRefresh calls=%d want 1", ref.forceCalls)
	}
	if ref.ensureCalls != 1 {
		t.Fatalf("EnsureFresh calls=%d want 1", ref.ensureCalls)
	}
	// Same lease path: one Acquire only; 401 refresh retry does not re-acquire.
	if got := ex.AcquireCount(); got != 1 {
		t.Fatalf("AcquireCount=%d want 1 (same-account 401 refresh)", got)
	}
	if mock.Hits() != 2 {
		t.Fatalf("hits=%d want 2 (401 then retry)", mock.Hits())
	}
	reqs := mock.Requests()
	if len(reqs) != 2 {
		t.Fatalf("captured=%d", len(reqs))
	}
	// Second post must use the force-refreshed token.
	if reqs[1].AccessToken != "refreshed-access" {
		t.Fatalf("retry token=%q want refreshed-access", reqs[1].AccessToken)
	}
	// Same idempotency key on both attempts.
	if reqs[0].IdempotencyKey == "" || reqs[0].IdempotencyKey != reqs[1].IdempotencyKey {
		t.Fatalf("idempotency keys: %q vs %q", reqs[0].IdempotencyKey, reqs[1].IdempotencyKey)
	}
}

func TestExecutor_IdempotencyKeyOnUpstream(t *testing.T) {
	mgr := setupLease(t, 3)
	mock := mockup.NewResponsesServer()
	t.Cleanup(mock.Close)
	mock.FailN = 1 // force failover so we see key shared across accounts

	ex := &executor.Executor{
		Leaser:      mgr,
		Upstream:    &mockup.Poster{Client: mock.Client()},
		MaxAttempts: 4,
	}

	resp, err := ex.Post(context.Background(), "grok-4", "idem", []byte(`{"model":"grok-4","input":"x"}`), false)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	reqs := mock.Requests()
	if len(reqs) < 2 {
		t.Fatalf("want >=2 requests, got %d", len(reqs))
	}
	key := reqs[0].IdempotencyKey
	if key == "" || !strings.HasPrefix(key, "grokbuild-") {
		t.Fatalf("missing/bad Idempotency-Key: %q", key)
	}
	for i, r := range reqs {
		if r.IdempotencyKey != key {
			t.Fatalf("req[%d] key=%q want %q", i, r.IdempotencyKey, key)
		}
		if r.Headers.Get("Idempotency-Key") != key {
			t.Fatalf("req[%d] header Idempotency-Key missing", i)
		}
		if r.Headers.Get("X-Idempotency-Key") != key {
			t.Fatalf("req[%d] header X-Idempotency-Key missing", i)
		}
	}
}
