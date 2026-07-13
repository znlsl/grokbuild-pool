package clients

import (
	"path/filepath"
	"testing"
)

func TestTokenConcurrencyGateAndPatch(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	max1 := 1
	unlim := true
	remain := int64(0)
	res, err := s.Create(CreateRequest{
		Name:           "t",
		Count:          1,
		MaxConcurrent:  &max1,
		UnlimitedQuota: &unlim,
		RemainQuota:    &remain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 token, got %d", len(res))
	}
	id := res[0].Token.ID
	if res[0].Token.MaxConcurrent != 1 {
		t.Fatalf("max_concurrent=%d want 1", res[0].Token.MaxConcurrent)
	}

	// 第 1 个 slot 应成功
	if err := s.AcquireSlot(id, 1, 0); err != nil {
		t.Fatalf("acquire1: %v", err)
	}
	// 第 2 个应被并发挡住
	if err := s.AcquireSlot(id, 1, 0); err == nil {
		t.Fatal("expected concurrency limit")
	} else if !isConcurrency(err) {
		t.Fatalf("want ErrConcurrencyLimit, got %v", err)
	}
	if s.CurrentInflight(id) != 1 {
		t.Fatalf("inflight=%d want 1", s.CurrentInflight(id))
	}

	// PATCH 提到 3，下一请求应放行
	max3 := 3
	tok, err := s.Patch(id, PatchRequest{MaxConcurrent: &max3})
	if err != nil {
		t.Fatal(err)
	}
	if tok.MaxConcurrent != 3 {
		t.Fatalf("patched max=%d", tok.MaxConcurrent)
	}
	if err := s.AcquireSlot(id, 3, 0); err != nil {
		t.Fatalf("acquire after patch: %v", err)
	}
	if s.CurrentInflight(id) != 2 {
		t.Fatalf("inflight=%d want 2", s.CurrentInflight(id))
	}

	// 显式改 0=不限：仍计数但不再硬拒
	max0 := 0
	if _, err := s.Patch(id, PatchRequest{MaxConcurrent: &max0}); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireSlot(id, 0, 0); err != nil {
		t.Fatalf("unlimited acquire: %v", err)
	}
	// Release 始终减，与 max 无关
	s.ReleaseSlot(id, 0)
	s.ReleaseSlot(id, 0)
	s.ReleaseSlot(id, 0)
	if s.CurrentInflight(id) != 0 {
		t.Fatalf("inflight after release=%d", s.CurrentInflight(id))
	}

	// 创建时显式 0 不被默认（此处无 admin 层；Create 直接写 0）
	z := 0
	res2, err := s.Create(CreateRequest{Name: "u", Count: 1, MaxConcurrent: &z, UnlimitedQuota: &unlim, RemainQuota: &remain})
	if err != nil {
		t.Fatal(err)
	}
	if res2[0].Token.MaxConcurrent != 0 {
		t.Fatalf("explicit 0 became %d", res2[0].Token.MaxConcurrent)
	}
}

func isConcurrency(err error) bool {
	return err != nil && (err == ErrConcurrencyLimit || (err.Error() != "" && contains(err.Error(), "concurrency")))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
