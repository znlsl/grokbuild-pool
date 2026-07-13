package hot

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

func openTestCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pool.db")
	c, err := catalog.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sampleAccount(id string, priority int) catalog.Account {
	now := time.Now().Unix()
	return catalog.Account{
		ID:           id,
		Revision:     1,
		IdentityKey:  "idk-" + id,
		Email:        id + "@example.com",
		Name:         "user-" + id,
		Priority:     priority,
		Enabled:      true,
		Lifecycle:    catalog.LifecycleActive,
		AccessToken:  "access-" + id,
		RefreshToken: "refresh-" + id,
		ExpiresAt:    now + 3600,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func meta(id string, priority int32) catalog.HotMeta {
	return catalog.HotMeta{
		ID:        id,
		Priority:  priority,
		Enabled:   true,
		Lifecycle: catalog.LifecycleActive,
		ExpiresAt: time.Now().Unix() + 3600,
	}
}

func TestPromoteDemoteCapacity(t *testing.T) {
	idx := New(Config{HotSize: 3})

	// Fill to capacity with priorities 10, 5, 1
	for _, m := range []catalog.HotMeta{meta("a", 10), meta("b", 5), meta("c", 1)} {
		if demoted, err := idx.Promote(m); err != nil || demoted != "" {
			t.Fatalf("promote %s: demoted=%q err=%v", m.ID, demoted, err)
		}
	}
	if idx.Len() != 3 {
		t.Fatalf("len=%d want 3", idx.Len())
	}

	// Promote high priority should demote lowest priority "c"
	demoted, err := idx.Promote(meta("d", 20))
	if err != nil {
		t.Fatalf("promote d: %v", err)
	}
	if demoted != "c" {
		t.Fatalf("demoted=%q want c", demoted)
	}
	if _, ok := idx.Get("c"); ok {
		t.Fatal("c should be demoted")
	}
	if _, ok := idx.Get("d"); !ok {
		t.Fatal("d should be hot")
	}
	if idx.Len() != 3 {
		t.Fatalf("len=%d want 3", idx.Len())
	}

	// Explicit demote
	if err := idx.Demote("b"); err != nil {
		t.Fatalf("demote b: %v", err)
	}
	if idx.Len() != 2 {
		t.Fatalf("len=%d want 2", idx.Len())
	}
	if _, ok := idx.Get("b"); ok {
		t.Fatal("b still hot after demote")
	}

	// Promote into free slot — no demote
	demoted, err = idx.Promote(meta("e", 0))
	if err != nil || demoted != "" {
		t.Fatalf("promote e: demoted=%q err=%v", demoted, err)
	}
	if idx.Len() != 3 {
		t.Fatalf("len=%d want 3", idx.Len())
	}
}

func TestPromotePreservesInflight(t *testing.T) {
	idx := New(Config{HotSize: 10})
	if _, err := idx.Promote(meta("x", 1)); err != nil {
		t.Fatal(err)
	}
	if err := idx.AddInflight("x"); err != nil {
		t.Fatal(err)
	}
	if err := idx.AddInflight("x"); err != nil {
		t.Fatal(err)
	}
	// Re-promote with fresh meta (Inflight 0) should keep live inflight
	upd := meta("x", 99)
	if _, err := idx.Promote(upd); err != nil {
		t.Fatal(err)
	}
	got, ok := idx.Get("x")
	if !ok || got.Inflight != 2 || got.Priority != 99 {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
	if err := idx.SubInflight("x"); err != nil {
		t.Fatal(err)
	}
	got, _ = idx.Get("x")
	if got.Inflight != 1 {
		t.Fatalf("inflight=%d want 1", got.Inflight)
	}
}

func TestCooldownFilter(t *testing.T) {
	idx := New(Config{HotSize: 10})
	now := time.Now().Unix()

	m1 := meta("ok", 10)
	m2 := meta("cool", 10)
	m2.CooldownUntil = now + 600
	m3 := meta("disabled", 10)
	m3.Enabled = false
	m4 := meta("quarantine", 10)
	m4.Lifecycle = catalog.LifecycleQuarantined

	for _, m := range []catalog.HotMeta{m1, m2, m3, m4} {
		if _, err := idx.Promote(m); err != nil {
			t.Fatal(err)
		}
	}

	elig := idx.Eligible(now)
	if len(elig) != 1 || elig[0].ID != "ok" {
		t.Fatalf("eligible=%v want only ok", elig)
	}

	// Clear cooldown → becomes eligible
	if err := idx.SetCooldown("cool", 0); err != nil {
		t.Fatal(err)
	}
	elig = idx.Eligible(now)
	if len(elig) != 2 {
		t.Fatalf("eligible count=%d want 2", len(elig))
	}

	// Future cooldown filter
	if err := idx.SetCooldown("ok", now+100); err != nil {
		t.Fatal(err)
	}
	if IsEligible(mustGet(t, idx, "ok"), now) {
		t.Fatal("ok should not be eligible while cooling")
	}
	if !IsEligible(mustGet(t, idx, "ok"), now+200) {
		t.Fatal("ok should be eligible after cooldown")
	}
}

func mustGet(t *testing.T, idx *Index, id string) catalog.HotMeta {
	t.Helper()
	m, ok := idx.Get(id)
	if !ok {
		t.Fatalf("missing %s", id)
	}
	return m
}

func TestLoadEligibleFromCatalog(t *testing.T) {
	c := openTestCatalog(t)
	now := time.Now().Unix()

	// 50 eligible, 10 cooling, 5 disabled — hot cap 20
	accounts := make([]catalog.Account, 0, 65)
	for i := 0; i < 50; i++ {
		a := sampleAccount(fmt.Sprintf("e%04d", i), i) // priority 0..49
		accounts = append(accounts, a)
	}
	for i := 0; i < 10; i++ {
		a := sampleAccount(fmt.Sprintf("c%04d", i), 100+i)
		a.CooldownUntil = now + 3600
		accounts = append(accounts, a)
	}
	for i := 0; i < 5; i++ {
		a := sampleAccount(fmt.Sprintf("d%04d", i), 200+i)
		a.Enabled = false
		accounts = append(accounts, a)
	}
	if err := c.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	idx := New(Config{HotSize: 20})
	n, err := idx.LoadEligible(c)
	if err != nil {
		t.Fatalf("LoadEligible: %v", err)
	}
	if n != 20 {
		t.Fatalf("loaded=%d want 20", n)
	}
	// Highest priorities among eligible are e0049..e0030
	for i := 30; i < 50; i++ {
		id := fmt.Sprintf("e%04d", i)
		if _, ok := idx.Get(id); !ok {
			t.Fatalf("expected hot %s", id)
		}
	}
	// Cooling / disabled must not load
	if _, ok := idx.Get("c0000"); ok {
		t.Fatal("cooling account should not be hot")
	}
	if _, ok := idx.Get("d0000"); ok {
		t.Fatal("disabled account should not be hot")
	}

	// No secrets in snapshot
	for _, m := range idx.SnapshotHot() {
		// HotMeta has no token fields; sanity on lifecycle/enabled
		if !m.Enabled || m.Lifecycle != catalog.LifecycleActive {
			t.Fatalf("bad meta in hot: %+v", m)
		}
		if m.ID == "" {
			t.Fatal("empty id")
		}
	}
}

func TestLoadEligiblePreservesInflight(t *testing.T) {
	c := openTestCatalog(t)
	accounts := []catalog.Account{
		sampleAccount("a", 10),
		sampleAccount("b", 9),
		sampleAccount("c", 8),
	}
	if err := c.UpsertMany(accounts); err != nil {
		t.Fatal(err)
	}
	idx := New(Config{HotSize: 3})
	if _, err := idx.LoadEligible(c); err != nil {
		t.Fatal(err)
	}
	if err := idx.AddInflight("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.LoadEligible(c); err != nil {
		t.Fatal(err)
	}
	got, ok := idx.Get("a")
	if !ok || got.Inflight != 1 {
		t.Fatalf("inflight not preserved: %+v ok=%v", got, ok)
	}
}

func TestConcurrentInflight(t *testing.T) {
	idx := New(Config{HotSize: 100})
	const n = 50
	for i := 0; i < n; i++ {
		if _, err := idx.Promote(meta(fmt.Sprintf("id-%d", i), int32(i))); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("id-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if err := idx.AddInflight(id); err != nil {
					t.Errorf("AddInflight: %v", err)
					return
				}
				if err := idx.SubInflight(id); err != nil {
					t.Errorf("SubInflight: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	st := idx.Stats(0)
	if st.InflightSum != 0 {
		t.Fatalf("inflight sum=%d want 0", st.InflightSum)
	}
}

func TestBuild10kHot3000Under2s(t *testing.T) {
	c := openTestCatalog(t)
	const catalogN = 10_000
	const hotN = 3000

	accounts := make([]catalog.Account, catalogN)
	now := time.Now().Unix()
	for i := 0; i < catalogN; i++ {
		a := sampleAccount(fmt.Sprintf("acc-%06d", i), i%100)
		// spread a few cooldowns so ListEligible still has plenty
		if i%50 == 0 {
			a.CooldownUntil = now + 100
		}
		accounts[i] = a
	}
	if err := c.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany 10k: %v", err)
	}

	idx := New(Config{HotSize: hotN})
	start := time.Now()
	n, err := idx.LoadEligible(c)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("LoadEligible: %v", err)
	}
	if n != hotN {
		// eligible count may be slightly under if many cooling — with 1/50 cooling ~9800 eligible
		if n < hotN {
			t.Fatalf("loaded=%d want %d", n, hotN)
		}
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("LoadEligible took %v want < 2s", elapsed)
	}
	t.Logf("LoadEligible 10k→%d hot in %v", n, elapsed)
}

func TestGetThroughput(t *testing.T) {
	idx := New(Config{HotSize: 3000})
	metas := make([]catalog.HotMeta, 3000)
	for i := 0; i < 3000; i++ {
		metas[i] = meta(fmt.Sprintf("g-%d", i), int32(i))
	}
	if _, err := idx.LoadMetas(metas); err != nil {
		t.Fatal(err)
	}

	const workers = 4
	const perWorker = 200_000
	var ops atomic.Int64
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("g-%d", (w*31+i)%3000)
				_, _ = idx.Get(id)
				ops.Add(1)
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	rate := float64(ops.Load()) / elapsed.Seconds()
	t.Logf("Get rate: %.0f ops/s over %v (%d workers)", rate, elapsed, workers)
	if rate < 50_000 {
		t.Fatalf("Get rate %.0f/s < 50k/s bar", rate)
	}
}

func TestVictimTieBreak(t *testing.T) {
	idx := New(Config{HotSize: 2})
	// same priority; higher failure score should demote first
	m1 := meta("low-fail", 1)
	m1.FailureScore = 1
	m2 := meta("high-fail", 1)
	m2.FailureScore = 9
	if _, err := idx.Promote(m1); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Promote(m2); err != nil {
		t.Fatal(err)
	}
	demoted, err := idx.Promote(meta("new", 1))
	if err != nil {
		t.Fatal(err)
	}
	if demoted != "high-fail" {
		t.Fatalf("demoted=%q want high-fail", demoted)
	}
}

// BenchmarkGet documents single-thread Get throughput.
func BenchmarkGet(b *testing.B) {
	idx := New(Config{HotSize: 3000})
	metas := make([]catalog.HotMeta, 3000)
	for i := 0; i < 3000; i++ {
		metas[i] = meta(fmt.Sprintf("b-%d", i), int32(i))
	}
	if _, err := idx.LoadMetas(metas); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = idx.Get(fmt.Sprintf("b-%d", i%3000))
	}
}

// BenchmarkLoadEligible10k measures catalog→hot load for 10k rows, hot=3000.
func BenchmarkLoadEligible10k(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "bench.db")
	c, err := catalog.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()

	accounts := make([]catalog.Account, 10_000)
	now := time.Now().Unix()
	for i := 0; i < 10_000; i++ {
		a := sampleAccount(fmt.Sprintf("acc-%06d", i), i%100)
		a.CreatedAt = now
		a.UpdatedAt = now
		accounts[i] = a
	}
	if err := c.UpsertMany(accounts); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := New(Config{HotSize: 3000})
		n, err := idx.LoadEligible(c)
		if err != nil {
			b.Fatal(err)
		}
		if n != 3000 {
			b.Fatalf("n=%d", n)
		}
	}
}

func TestMemoryNoteHelper(t *testing.T) {
	// Rough heap delta for 140k HotMeta entries (full meta map, not hot=3000).
	// Documented in phases/M05.md; this test only sanity-checks 3k is tiny.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	idx := New(Config{HotSize: 3000})
	metas := make([]catalog.HotMeta, 3000)
	for i := 0; i < 3000; i++ {
		metas[i] = catalog.HotMeta{
			ID:          fmt.Sprintf("mem-%06d", i),
			Priority:    int32(i % 100),
			Enabled:     true,
			Lifecycle:   catalog.LifecycleActive,
			ExpiresAt:   time.Now().Unix() + 3600,
			IdentityKey: fmt.Sprintf("idk-%06d", i),
			ProxyMode:   "inherit",
		}
	}
	if _, err := idx.LoadMetas(metas); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	var delta uint64
	if after.HeapAlloc > before.HeapAlloc {
		delta = after.HeapAlloc - before.HeapAlloc
	}
	t.Logf("approx heap delta for 3000 HotMeta: %d bytes (%.2f MB)", delta, float64(delta)/(1<<20))
	// Soft bound — GC noise; only fail if absurdly large (>50MB for 3k)
	if delta > 50<<20 {
		t.Fatalf("unexpectedly large heap delta: %d", delta)
	}
	_ = idx.Len()
}
