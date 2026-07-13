package clients

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCreateAuthQuotaConcurrency(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res, err := s.Create(CreateRequest{
		Name: "demo", RemainQuota: 2, MaxConcurrent: 1, RPM: 0, Count: 1,
	})
	if err != nil || len(res) != 1 {
		t.Fatalf("create: %v %#v", err, res)
	}
	key := res[0].APIKey
	if key == "" || res[0].Token.KeyPrefix == "" {
		t.Fatal("missing plaintext/prefix")
	}

	info, err := s.Authenticate(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if info.TokenID != res[0].Token.ID {
		t.Fatalf("id %s", info.TokenID)
	}

	if err := s.AcquireSlot(info.TokenID, info.MaxConcurrent, info.RPM); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireSlot(info.TokenID, info.MaxConcurrent, info.RPM); err == nil {
		t.Fatal("expected concurrency limit")
	}
	s.ReleaseSlot(info.TokenID, info.MaxConcurrent)

	if err := s.RecordUsage(info.TokenID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(info.TokenID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(context.Background(), key); err != ErrQuotaExceeded {
		t.Fatalf("want quota exceeded, got %v", err)
	}

	list, err := s.List(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %d", err, len(list))
	}
	if list[0].KeyPrefix == "" {
		t.Fatal("missing prefix")
	}
	if list[0].UsedQuota != 2 {
		t.Fatalf("used=%d", list[0].UsedQuota)
	}
}

func TestBatchCreate(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Create(CreateRequest{Name: "batch", UnlimitedQuota: true, Count: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 5 {
		t.Fatalf("got %d", len(res))
	}
	keys := map[string]struct{}{}
	for _, r := range res {
		keys[r.APIKey] = struct{}{}
	}
	if len(keys) != 5 {
		t.Fatal("duplicate keys")
	}
}

func TestReserveQuotaNoOverspend(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Create(CreateRequest{Name: "q", RemainQuota: 1, UnlimitedQuota: false, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	id := res[0].Token.ID

	var okCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.ReserveQuota(id, 1); err == nil {
				okCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := okCount.Load(); got != 1 {
		t.Fatalf("reserved ok=%d want 1", got)
	}
	list, err := s.List(1)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].RemainQuota != 0 {
		t.Fatalf("remain=%d", list[0].RemainQuota)
	}
	// settle actual=1 after reserve
	if err := s.SettleUsage(id, 1, 1); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List(1)
	if list[0].UsedQuota != 1 || list[0].RequestCount != 1 {
		t.Fatalf("after settle used=%d req=%d", list[0].UsedQuota, list[0].RequestCount)
	}
}

func TestSettleUsageRefundAndExtra(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Create(CreateRequest{Name: "s", RemainQuota: 5, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	id := res[0].Token.ID
	if err := s.ReserveQuota(id, 1); err != nil {
		t.Fatal(err)
	}
	// actual 1: no change to remain (already reserved)
	if err := s.SettleUsage(id, 1, 1); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(1)
	if list[0].RemainQuota != 4 {
		t.Fatalf("remain after settle1=%d", list[0].RemainQuota)
	}
	if err := s.ReserveQuota(id, 1); err != nil {
		t.Fatal(err)
	}
	// refund path via settle actual < reserved? use RefundQuota
	if err := s.RefundQuota(id, 1); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List(1)
	if list[0].RemainQuota != 4 {
		t.Fatalf("remain after refund=%d", list[0].RemainQuota)
	}
}
