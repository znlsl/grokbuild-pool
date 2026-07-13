package hot

import (
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

func TestMaxInflightPerAccountHardFilter(t *testing.T) {
	idx := New(Config{HotSize: 10, MaxInflightPerAccount: 2})
	now := time.Now().Unix()
	metas := []catalog.HotMeta{
		{ID: "a", Enabled: true, Lifecycle: catalog.LifecycleActive, Priority: 1},
		{ID: "b", Enabled: true, Lifecycle: catalog.LifecycleActive, Priority: 1},
	}
	if _, err := idx.LoadMetas(metas); err != nil {
		t.Fatal(err)
	}
	// a inflight 2 → 不可再选
	if err := idx.AddInflight("a"); err != nil {
		t.Fatal(err)
	}
	if err := idx.AddInflight("a"); err != nil {
		t.Fatal(err)
	}
	ma, _ := idx.Get("a")
	if idx.EligibleMeta(ma, now) {
		t.Fatal("a 应被硬上限过滤")
	}
	mb, _ := idx.Get("b")
	if !idx.EligibleMeta(mb, now) {
		t.Fatal("b 应仍可选")
	}
	// 无上限时 a 仍 eligible（仅看基础字段）
	if !IsEligible(ma, now) {
		t.Fatal("IsEligible 不含 inflight 硬限")
	}
}
