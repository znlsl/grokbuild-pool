package selector

import (
	"testing"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
)

func TestStableRRRoundRobinHighestPriority(t *testing.T) {
	idx := hot.New(hot.Config{HotSize: 10, MaxInflightPerAccount: 2})
	metas := []catalog.HotMeta{
		{ID: "a", Priority: 10, Enabled: true, Lifecycle: catalog.LifecycleActive},
		{ID: "b", Priority: 10, Enabled: true, Lifecycle: catalog.LifecycleActive},
		{ID: "c", Priority: 1, Enabled: true, Lifecycle: catalog.LifecycleActive},
	}
	if _, err := idx.LoadMetas(metas); err != nil {
		t.Fatal(err)
	}
	s := New(idx, Config{Strategy: StrategyStableRR, JitterAmp: 0})
	now := int64(1_700_000_000)
	seen := map[string]int{}
	for i := 0; i < 20; i++ {
		id, ok := s.Pick(now, "")
		if !ok {
			t.Fatal("empty")
		}
		if id == "c" {
			t.Fatalf("should not pick lower priority %q", id)
		}
		seen[id]++
	}
	if seen["a"] == 0 || seen["b"] == 0 {
		t.Fatalf("expected both high-priority accounts, got %#v", seen)
	}
}
