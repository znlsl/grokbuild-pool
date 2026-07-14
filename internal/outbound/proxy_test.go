package outbound

import (
	"net/http"
	"testing"

	"github.com/yshgsh1343/grokbuild2api/internal/protocol/upstream"
)

func TestBuildClientHTTPProxy(t *testing.T) {
	f := NewFactory(upstream.Config{BaseURL: "https://example.com/v1"})
	c, err := f.Client("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
	// transport should use Proxy for http scheme
	hc := c.Config().HTTPClient
	if hc == nil || hc.Transport == nil {
		t.Fatal("missing transport")
	}
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type %T", hc.Transport)
	}
	if tr.Proxy == nil {
		t.Fatal("expected Proxy set for http proxy")
	}
}

func TestBuildClientSOCKS5Dial(t *testing.T) {
	f := NewFactory(upstream.Config{BaseURL: "https://example.com/v1"})
	c, err := f.Client("socks5://127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	hc := c.Config().HTTPClient
	tr := hc.Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Fatal("socks should not use http.Proxy")
	}
	if tr.DialContext == nil {
		t.Fatal("socks requires DialContext")
	}
}

func TestBuildClientSOCKS5h(t *testing.T) {
	f := NewFactory(upstream.Config{})
	if _, err := f.Client("socks5h://user:pass@127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildClientBadScheme(t *testing.T) {
	f := NewFactory(upstream.Config{})
	if _, err := f.Client("ftp://x"); err == nil {
		t.Fatal("expected error")
	}
}
