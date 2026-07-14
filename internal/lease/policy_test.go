package lease

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
	"github.com/yshgsh1343/grokbuild2api/internal/selector"
)

func TestRelease402DoesNotQuarantineByDefault(t *testing.T) {
	dir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	acc := catalog.Account{
		ID: "acct-402", IdentityKey: "ik", Priority: 1, Enabled: true,
		Lifecycle: catalog.LifecycleActive, AccessToken: "a", RefreshToken: "r",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := cat.UpsertMany([]catalog.Account{acc}); err != nil {
		t.Fatal(err)
	}
	idx := hot.New(hot.Config{HotSize: 8, MaxInflightPerAccount: 2})
	if _, err := idx.LoadEligible(cat); err != nil {
		t.Fatal(err)
	}
	sel := selector.New(idx, selector.DefaultConfig())
	mgr := New(cat, idx, sel, DefaultConfig())
	if err := idx.AddInflight("acct-402"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Release(context.Background(), Lease{AccountID: "acct-402"}, Result{Success: false, StatusCode: 402}); err != nil {
		t.Fatal(err)
	}
	got, err := cat.Get("acct-402")
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != catalog.LifecycleActive || !got.Enabled {
		t.Fatalf("402 should not quarantine by default: lifecycle=%s enabled=%v", got.Lifecycle, got.Enabled)
	}
	if got.CooldownUntil <= time.Now().Unix() {
		t.Fatalf("expected cooldown, got %d", got.CooldownUntil)
	}
}

func TestRelease429KeepsStickyByDefault(t *testing.T) {
	dir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	acc := catalog.Account{
		ID: "acct-429", IdentityKey: "ik", Priority: 1, Enabled: true,
		Lifecycle: catalog.LifecycleActive, AccessToken: "a", RefreshToken: "r",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := cat.UpsertMany([]catalog.Account{acc}); err != nil {
		t.Fatal(err)
	}
	idx := hot.New(hot.Config{HotSize: 8, MaxInflightPerAccount: 2})
	if _, err := idx.LoadEligible(cat); err != nil {
		t.Fatal(err)
	}
	sel := selector.New(idx, selector.DefaultConfig())
	sel.BindSticky("sess", "acct-429")
	mgr := New(cat, idx, sel, DefaultConfig())
	if err := idx.AddInflight("acct-429"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Release(context.Background(), Lease{AccountID: "acct-429", StickyKey: "sess"}, Result{Success: false, StatusCode: 429}); err != nil {
		t.Fatal(err)
	}
	if sel.StickyLen() != 1 {
		t.Fatalf("sticky should remain by default, len=%d", sel.StickyLen())
	}
}
