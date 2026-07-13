package outbound

import (
	"testing"

	"github.com/yshgsh1343/grokbuild2api/internal/protocol/upstream"
)

func TestClientForAccountAffinityAndForget(t *testing.T) {
	f := NewFactory(upstream.Config{BaseURL: "https://example.invalid"})

	c1, err := f.ClientFor("acc-a", "http://proxy.example:8080")
	if err != nil || c1 == nil {
		t.Fatalf("ClientFor: %v c=%v", err, c1)
	}
	c2, err := f.ClientFor("acc-a", "http://proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Fatal("同一 account+proxy 应命中缓存")
	}
	// 不同账号同 proxy → 不同亲和键
	c3, err := f.ClientFor("acc-b", "http://proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if c3 == c1 {
		t.Fatal("不同 account 不应共享 account 亲和缓存条目")
	}
	// 纯 proxy 键与 account 键分离
	c4, err := f.Client("http://proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if c4 == c1 {
		t.Fatal("Client(proxy) 与 ClientFor(account,proxy) 键不同")
	}

	p, ok := f.LastProxy("acc-a")
	if !ok || p != "http://proxy.example:8080" {
		t.Fatalf("LastProxy acc-a = %q ok=%v", p, ok)
	}

	before := f.Len()
	if before < 3 {
		t.Fatalf("cache len=%d want >=3", before)
	}
	f.Forget("http://proxy.example:8080")
	if f.Len() != 0 {
		t.Fatalf("Forget 后 len=%d want 0", f.Len())
	}
	if _, ok := f.LastProxy("acc-a"); ok {
		t.Fatal("Forget 应清除 lastProxyByAccount")
	}

	// 重建后再 ForgetAccount
	_, _ = f.ClientFor("acc-a", "http://p2.example:9")
	_, _ = f.ClientFor("acc-b", "http://p2.example:9")
	f.ForgetAccount("acc-a")
	if _, ok := f.LastProxy("acc-a"); ok {
		t.Fatal("ForgetAccount 应清除 acc-a")
	}
	if _, ok := f.LastProxy("acc-b"); !ok {
		t.Fatal("ForgetAccount 不应影响 acc-b")
	}
}

func TestClientDirectAndBadScheme(t *testing.T) {
	f := NewFactory(upstream.Config{})
	c, err := f.Client("")
	if err != nil || c == nil {
		t.Fatalf("direct: %v", err)
	}
	if _, err := f.Client("ftp://bad"); err == nil {
		t.Fatal("期望坏 scheme 报错")
	}
}
