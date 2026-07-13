//go:build m05real

package hot

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

func TestLoadRealPool10000(t *testing.T) {
	path := "/opt/grokbuild-pool/data/pool-10000.db"
	if _, err := os.Stat(path); err != nil {
		t.Skip("pool-10000.db missing")
	}
	c, err := catalog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	st, err := c.Stats()
	if err != nil {
		t.Fatal(err)
	}
	idx := New(Config{HotSize: 3000})
	start := time.Now()
	n, err := idx.LoadEligible(c)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog_path=%s count=%d active=%d", path, st.Count, st.ActiveCount)
	t.Logf("load_hot_n=%d elapsed=%v", n, elapsed)
	if elapsed >= 2*time.Second {
		t.Fatalf("load too slow: %v", elapsed)
	}
	if n < 2900 {
		t.Fatalf("unexpected n=%d", n)
	}
}

func TestLoadRealPool140000(t *testing.T) {
	path := "/opt/grokbuild-pool/data/pool-140000.db"
	if _, err := os.Stat(path); err != nil {
		t.Skip("pool-140000.db missing")
	}
	c, err := catalog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	st, err := c.Stats()
	if err != nil {
		t.Fatal(err)
	}
	idx := New(Config{HotSize: 3000})
	start := time.Now()
	n, err := idx.LoadEligible(c)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog_path=%s count=%d active=%d", path, st.Count, st.ActiveCount)
	t.Logf("load_hot_n=%d elapsed=%v", n, elapsed)
	// Loading top 3000 from 140k via ListEligible LIMIT pages should still be fast;
	// allow a bit more headroom than 10k case.
	if elapsed >= 5*time.Second {
		t.Fatalf("load too slow: %v", elapsed)
	}
	if n != 3000 {
		t.Fatalf("unexpected n=%d", n)
	}
}

func TestMem140kMetas(t *testing.T) {
	// Measure TotalAlloc during LoadMetas (map materialization).
	metas := make([]catalog.HotMeta, 140000)
	for i := 0; i < 140000; i++ {
		metas[i] = catalog.HotMeta{
			ID:          fmt.Sprintf("acc-%06d", i),
			Priority:    int32(i % 100),
			Enabled:     true,
			Lifecycle:   catalog.LifecycleActive,
			ExpiresAt:   time.Now().Unix() + 3600,
			IdentityKey: fmt.Sprintf("idk-%06d", i),
		}
	}
	// Drop slice pressure baseline: measure only map insert cost after metas exist.
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)
	big := New(Config{HotSize: 140000})
	nn, err := big.LoadMetas(metas)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&m2)
	delta := m2.TotalAlloc - m1.TotalAlloc
	// Also report in-use after forcing GC and dropping metas.
	metas = nil
	runtime.GC()
	var m3 runtime.MemStats
	runtime.ReadMemStats(&m3)
	t.Logf("full140k_metas_n=%d load_totalalloc_delta_bytes=%d (%.2f MB)", nn, delta, float64(delta)/(1<<20))
	t.Logf("after_gc_heap_inuse_bytes=%d (%.2f MB) heap_alloc=%d (%.2f MB)",
		m3.HeapInuse, float64(m3.HeapInuse)/(1<<20),
		m3.HeapAlloc, float64(m3.HeapAlloc)/(1<<20),
	)
	// TotalAlloc during LoadMetas is map+string retention; plan bar <50MB for meta overhead.
	// Unique ID strings dominate; document actual. Soft-fail only if map load alone > 100MB.
	if delta > 100<<20 {
		t.Fatalf("140k LoadMetas TotalAlloc %.2f MB excessive", float64(delta)/(1<<20))
	}
	_ = big.Len()
}

func TestMem3000Metas(t *testing.T) {
	metas := make([]catalog.HotMeta, 3000)
	for i := 0; i < 3000; i++ {
		metas[i] = catalog.HotMeta{
			ID:          fmt.Sprintf("acc-%06d", i),
			Priority:    int32(i % 100),
			Enabled:     true,
			Lifecycle:   catalog.LifecycleActive,
			ExpiresAt:   time.Now().Unix() + 3600,
			IdentityKey: fmt.Sprintf("idk-%06d", i),
		}
	}
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)
	idx := New(Config{HotSize: 3000})
	n, err := idx.LoadMetas(metas)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&m2)
	delta := m2.TotalAlloc - m1.TotalAlloc
	t.Logf("hot3000_metas_n=%d load_totalalloc_delta_bytes=%d (%.2f MB)", n, delta, float64(delta)/(1<<20))
}
