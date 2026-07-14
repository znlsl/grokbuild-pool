package proxypool

import (
	"path/filepath"
	"testing"
)

func TestPickHashStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.json")
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ReplaceAll([]Node{
		{ID: "a", URL: "socks5://127.0.0.1:1", Enabled: true},
		{ID: "b", URL: "socks5://127.0.0.1:2", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	u1, m1, ok := p.Pick("acct-1", AssignHash)
	if !ok || u1 == "" || m1 != "socks5" {
		t.Fatalf("pick1 %q %q %v", u1, m1, ok)
	}
	u2, _, ok := p.Pick("acct-1", AssignHash)
	if !ok || u2 != u1 {
		// hash is stable on healthy set; AssignedAccounts changes don't change hash index
		// but we increment AssignedAccounts — still same index selection
		t.Fatalf("unstable pick %q vs %q", u1, u2)
	}
}

func TestMarkFailCools(t *testing.T) {
	dir := t.TempDir()
	p, _ := Open(filepath.Join(dir, "p.json"))
	_ = p.ReplaceAll([]Node{{ID: "only", URL: "socks5://127.0.0.1:9", Enabled: true}})
	p.MarkFail("socks5://127.0.0.1:9", "dial")
	if p.HealthyCount() != 0 {
		t.Fatal("expected cooled")
	}
	// second node still pickable
	_ = p.ReplaceAll([]Node{
		{ID: "only", URL: "socks5://127.0.0.1:9", Enabled: true},
		{ID: "ok", URL: "socks5://127.0.0.1:8", Enabled: true},
	})
	// re-open health from file after replace preserves only matching id+url health
	// fresh nodes start healthy
	if p.HealthyCount() < 1 {
		t.Fatal("expected healthy")
	}
}

func TestValidateURL(t *testing.T) {
	if err := ValidateURL("socks5://h:1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateURL("http://h:1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateURL("ftp://h"); err == nil {
		t.Fatal("want err")
	}
}
