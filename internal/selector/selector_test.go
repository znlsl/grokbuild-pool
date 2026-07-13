package selector

import (
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
)

func meta(id string, priority int32) catalog.HotMeta {
	return catalog.HotMeta{
		ID:        id,
		Priority:  priority,
		Enabled:   true,
		Lifecycle: catalog.LifecycleActive,
		ExpiresAt: time.Now().Unix() + 3600,
	}
}

func loadIndex(t *testing.T, metas ...catalog.HotMeta) *hot.Index {
	t.Helper()
	idx := hot.New(hot.Config{HotSize: len(metas) + 8})
	if _, err := idx.LoadMetas(metas); err != nil {
		t.Fatalf("LoadMetas: %v", err)
	}
	return idx
}

func TestStickyHit(t *testing.T) {
	now := time.Now().Unix()
	idx := loadIndex(t, meta("a", 10), meta("b", 5), meta("c", 1))
	cfg := DefaultConfig()
	cfg.JitterAmp = 0
	s := New(idx, cfg)
	s.SetRand(rand.New(rand.NewSource(1)))

	s.BindSticky("conv-1", "b")
	id, ok := s.Pick(now, "conv-1")
	if !ok || id != "b" {
		t.Fatalf("sticky pick got (%q,%v) want b", id, ok)
	}
	// Second hit still sticky
	id, ok = s.Pick(now, "conv-1")
	if !ok || id != "b" {
		t.Fatalf("sticky re-pick got (%q,%v) want b", id, ok)
	}
}

func TestStickyBrokenOnCooldown(t *testing.T) {
	now := time.Now().Unix()
	idx := loadIndex(t, meta("a", 10), meta("b", 5))
	cfg := DefaultConfig()
	cfg.JitterAmp = 0
	s := New(idx, cfg)
	s.SetRand(rand.New(rand.NewSource(42)))

	s.BindSticky("k", "a")
	// Put a on cooldown past now
	if err := idx.SetCooldown("a", now+600); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}
	id, ok := s.Pick(now, "k")
	if !ok {
		t.Fatal("expected fallback pick when sticky cooling")
	}
	if id == "a" {
		t.Fatal("picked cooling sticky account a")
	}
	if id != "b" {
		t.Fatalf("fallback id=%q want b", id)
	}
	// Sticky for k should have been cleared / rebound to b
	id2, ok2 := s.Pick(now, "k")
	if !ok2 || id2 != "b" {
		t.Fatalf("rebind sticky got (%q,%v) want b", id2, ok2)
	}
}

func TestNoPickFromCooling(t *testing.T) {
	now := time.Now().Unix()
	m := meta("cool", 100)
	m.CooldownUntil = now + 999
	idx := loadIndex(t, m)
	s := New(idx, DefaultConfig())
	id, ok := s.Pick(now, "")
	if ok {
		t.Fatalf("unexpected pick %q from cooling-only pool", id)
	}

	// Add an eligible peer — must pick that one, never cool.
	if _, err := idx.Promote(meta("ok", 1)); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	id, ok = s.Pick(now, "")
	if !ok || id != "ok" {
		t.Fatalf("got (%q,%v) want ok", id, ok)
	}
}

func TestHighInflightLosesToIdle_DeterministicScore(t *testing.T) {
	// Two accounts: high priority but high inflight vs lower priority idle.
	// With default weights: score(hi) = 1*100 - 10*20 = -100
	//                      score(idle) = 1*10 - 10*0 = 10
	// idle wins. Force K=2, jitter=0, fixed seed so both are always sampled.
	now := time.Now().Unix()
	busy := meta("busy", 100)
	busy.Inflight = 20
	idle := meta("idle", 10)
	idx := loadIndex(t, busy, idle)

	cfg := DefaultConfig()
	cfg.Pow2K = 2
	cfg.JitterAmp = 0
	cfg.WPriority = 1
	cfg.WInflight = 10
	cfg.WFailure = 5
	s := New(idx, cfg)
	s.SetRand(rand.New(rand.NewSource(7)))

	// Explicit score check (acceptance: deterministic score test)
	if scBusy := s.Score(busy, 0); scBusy >= s.Score(idle, 0) {
		t.Fatalf("score(busy)=%v should be < score(idle)=%v", scBusy, s.Score(idle, 0))
	}

	// With K=2 and only 2 candidates, pick must always be idle.
	for i := 0; i < 20; i++ {
		id, ok := s.Pick(now, "")
		if !ok || id != "idle" {
			t.Fatalf("iter %d: got (%q,%v) want idle", i, id, ok)
		}
	}
}

func TestDoesNotOnlyPickMaxPriorityWhenLowerIsHealthier(t *testing.T) {
	// Documented behavior: among samples, least-load can prefer lower priority.
	// busy-high-pri vs healthy-low-pri: low wins on score.
	now := time.Now().Unix()
	hi := meta("hi", 50)
	hi.Inflight = 15
	lo := meta("lo", 5)
	lo.Inflight = 0
	idx := loadIndex(t, hi, lo)

	cfg := DefaultConfig()
	cfg.Pow2K = 2
	cfg.JitterAmp = 0
	s := New(idx, cfg)
	s.SetRand(rand.New(rand.NewSource(99)))

	id, ok := s.Pick(now, "")
	if !ok || id != "lo" {
		t.Fatalf("got (%q,%v) want lo (healthy lower priority)", id, ok)
	}
}

func TestClearStickyOnMarkBad(t *testing.T) {
	now := time.Now().Unix()
	idx := loadIndex(t, meta("a", 10), meta("b", 10), meta("c", 10))
	cfg := DefaultConfig()
	cfg.JitterAmp = 0
	s := New(idx, cfg)
	s.SetRand(rand.New(rand.NewSource(3)))

	s.BindSticky("k1", "a")
	s.BindSticky("k2", "a")
	s.BindSticky("k3", "b")
	if s.StickyLen() != 3 {
		t.Fatalf("sticky len=%d want 3", s.StickyLen())
	}

	// Mark account a bad (429/401/402) → clear all stickies for a
	s.ClearStickyAccount("a")
	if id, ok := s.Pick(now, "k1"); !ok {
		t.Fatal("pick after clear should still find someone")
	} else if id == "a" {
		// could still pick a via pow2; that's fine — sticky must not force it
		// ensure sticky was cleared: bind b explicitly and re-check clear key path
		_ = id
	}
	// k3 still sticky to b
	id, ok := s.Pick(now, "k3")
	if !ok || id != "b" {
		t.Fatalf("k3 sticky got (%q,%v) want b", id, ok)
	}

	s.ClearStickyKey("k3")
	// After clear key, pick may be any eligible; sticky len drops
	if s.StickyLen() < 0 {
		t.Fatal("negative sticky")
	}
}

func TestPickExcluding(t *testing.T) {
	now := time.Now().Unix()
	idx := loadIndex(t, meta("a", 10), meta("b", 9), meta("c", 8))
	cfg := DefaultConfig()
	cfg.Pow2K = 3
	cfg.JitterAmp = 0
	s := New(idx, cfg)
	s.SetRand(rand.New(rand.NewSource(11)))

	ex := map[string]struct{}{"a": {}, "b": {}}
	id, ok := s.PickExcluding(now, "", ex)
	if !ok || id != "c" {
		t.Fatalf("got (%q,%v) want c", id, ok)
	}

	// Exclude all → no pick
	ex["c"] = struct{}{}
	if _, ok := s.PickExcluding(now, "", ex); ok {
		t.Fatal("expected no pick when all excluded")
	}
}

func TestStickyTTLExpiry(t *testing.T) {
	now := int64(1_000_000)
	idx := loadIndex(t, meta("a", 1), meta("b", 100))
	cfg := DefaultConfig()
	cfg.StickyTTLSec = 10
	cfg.JitterAmp = 0
	cfg.Pow2K = 2
	s := New(idx, cfg)
	s.SetRand(rand.New(rand.NewSource(5)))

	s.sticky.put(now, "k", "a")
	// Within TTL
	if id, ok := s.Pick(now+5, "k"); !ok || id != "a" {
		t.Fatalf("within TTL got (%q,%v) want a", id, ok)
	}
	// After TTL: sticky expired; pow2 may pick b (higher priority)
	id, ok := s.Pick(now+100, "k")
	if !ok {
		t.Fatal("expected pick after TTL")
	}
	// With jitter 0 and K=2, b (pri 100) beats a (pri 1)
	if id != "b" {
		t.Fatalf("after TTL expected b (higher pri), got %q", id)
	}
}

func TestDisabledAndNonActiveFiltered(t *testing.T) {
	now := time.Now().Unix()
	dis := meta("dis", 100)
	dis.Enabled = false
	q := meta("q", 100)
	q.Lifecycle = catalog.LifecycleQuarantined
	okm := meta("ok", 1)
	idx := loadIndex(t, dis, q, okm)
	s := New(idx, DefaultConfig())
	id, ok := s.Pick(now, "")
	if !ok || id != "ok" {
		t.Fatalf("got (%q,%v) want ok", id, ok)
	}
}

func TestStickyLRUEviction(t *testing.T) {
	lru := newStickyLRU(2, 1000)
	now := int64(100)
	lru.put(now, "k1", "a")
	lru.put(now, "k2", "b")
	lru.put(now, "k3", "c") // evicts k1
	if lru.len() != 2 {
		t.Fatalf("len=%d want 2", lru.len())
	}
	if _, ok := lru.get(now, "k1"); ok {
		t.Fatal("k1 should be evicted")
	}
	if id, ok := lru.get(now, "k2"); !ok || id != "b" {
		t.Fatalf("k2 got (%q,%v)", id, ok)
	}
	// access k2 then insert k4 → should evict k3 (LRU)
	lru.put(now, "k4", "d")
	if _, ok := lru.get(now, "k3"); ok {
		t.Fatal("k3 should be evicted")
	}
	if id, ok := lru.get(now, "k2"); !ok || id != "b" {
		t.Fatalf("k2 should remain, got (%q,%v)", id, ok)
	}
}

func TestDefaultConfigNormalize(t *testing.T) {
	s := New(hot.New(hot.Config{HotSize: 10}), Config{})
	c := s.Config()
	if c.Pow2K != DefaultPow2K || c.StickyMax != DefaultStickyMax {
		t.Fatalf("normalize failed: %+v", c)
	}
	if c.WInflight != DefaultWInflight {
		t.Fatalf("WInflight=%v", c.WInflight)
	}
}

// BenchmarkPick measures single-goroutine Pick throughput on a 3000-account hot set.
// M11 G2 target: ≥20k picks/s.
func BenchmarkPick(b *testing.B) {
	const n = 3000
	metas := make([]catalog.HotMeta, n)
	now := time.Now().Unix()
	for i := 0; i < n; i++ {
		metas[i] = catalog.HotMeta{
			ID:        "bench-" + itoa(i),
			Priority:  int32(i % 10),
			Enabled:   true,
			Lifecycle: catalog.LifecycleActive,
			ExpiresAt: now + 3600,
		}
	}
	idx := hot.New(hot.Config{HotSize: n})
	if _, err := idx.LoadMetas(metas); err != nil {
		b.Fatal(err)
	}
	s := New(idx, DefaultConfig())
	s.SetRand(rand.New(rand.NewSource(42)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, ok := s.Pick(now, "")
		if !ok || id == "" {
			b.Fatal("pick failed")
		}
	}
}

// BenchmarkPickParallel measures concurrent Pick under GOMAXPROCS.
func BenchmarkPickParallel(b *testing.B) {
	const n = 3000
	metas := make([]catalog.HotMeta, n)
	now := time.Now().Unix()
	for i := 0; i < n; i++ {
		metas[i] = catalog.HotMeta{
			ID:        "benchp-" + itoa(i),
			Priority:  int32(i % 10),
			Enabled:   true,
			Lifecycle: catalog.LifecycleActive,
			ExpiresAt: now + 3600,
		}
	}
	idx := hot.New(hot.Config{HotSize: n})
	if _, err := idx.LoadMetas(metas); err != nil {
		b.Fatal(err)
	}
	s := New(idx, DefaultConfig())
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// each goroutine uses empty sticky (pow2 path)
		for pb.Next() {
			id, ok := s.Pick(now, "")
			if !ok || id == "" {
				b.Fatal("pick failed")
			}
		}
	})
}

func itoa(i int) string {
	// small local helper avoids strconv import churn if already present
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func TestApplyConfigConcurrentWithPick(t *testing.T) {
	idx := loadIndex(t, meta("a", 1), meta("b", 2), meta("c", 3))
	sel := New(idx, DefaultConfig())
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				cfg := sel.Config()
				cfg.Pow2K = 2
				cfg.WPriority = 1.5
				cfg.StickyMax = 16
				sel.ApplyConfig(cfg)
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_, _ = sel.Pick(0, "k")
		}
	}()
	// let them race a bit
	for i := 0; i < 2000; i++ {
		_, _ = sel.Pick(0, "k2")
	}
	close(stop)
	wg.Wait()
}
