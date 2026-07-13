package ssoimport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

func TestParseSSONewlineAndJSON(t *testing.T) {
	// normalizeSSOValue strips a trailing segment only when the line contains >=2 "----" markers
	// (reference importer behavior), e.g. "meta----extra----cookie".
	text := "# comment\n\nsso-cookie-one\nmeta----extra----sso-cookie-two\n"
	vals, err := ParseSSOValues([]byte(text), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 2 || vals[0] != "sso-cookie-one" || vals[1] != "sso-cookie-two" {
		t.Fatalf("got %#v", vals)
	}

	arr := `["alpha", "beta"]`
	vals, err = ParseSSOValues([]byte(arr), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 2 {
		t.Fatalf("array: %#v", vals)
	}

	// Only fields named sso|sso_token|cookie|token|cookies are collected (reference behavior).
	obj := `{"sso":["c1","c2"],"token":"c3","ignored":"x"}`
	vals, err = ParseSSOValues([]byte(obj), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Order follows JSON key visit; require multiset {c1,c2,c3}.
	if len(vals) != 3 {
		t.Fatalf("obj: %#v", vals)
	}
	set := map[string]int{}
	for _, v := range vals {
		set[v]++
	}
	for _, want := range []string{"c1", "c2", "c3"} {
		if set[want] != 1 {
			t.Fatalf("obj missing %q in %#v", want, vals)
		}
	}
	nested := `[{"sso":"n1"},{"cookie":"n2"}]`
	vals, err = ParseSSOValues([]byte(nested), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 2 || vals[0] != "n1" || vals[1] != "n2" {
		t.Fatalf("nested: %#v", vals)
	}
}

func TestParseSSOBounds(t *testing.T) {
	if _, err := ParseSSOValuesBounded([]byte("one\ntwo\n"), 1, 16); !errors.Is(err, ErrEntryLimit) {
		t.Fatalf("expected entry limit, got %v", err)
	}
	if _, err := ParseSSOValuesBounded([]byte("0123456789\n"), 10, 4); err == nil {
		t.Fatal("expected value length limit")
	}
	if _, err := ParseSSOValuesBounded([]byte(`{"sso":"0123456789"}`), 10, 4); err == nil {
		t.Fatal("expected JSON value length limit")
	}
	bypass := `{"sso":"` + strings.Repeat("x", 64) + `----meta----ok"}`
	if _, err := ParseSSOValuesBounded([]byte(bypass), 10, 16); err == nil {
		t.Fatal("expected raw JSON SSO value length limit before normalization")
	}
	vals, err := ParseSSOValuesBounded([]byte("abcd\r\nx"), 10, 4)
	if err != nil || len(vals) != 2 || vals[0] != "abcd" {
		t.Fatalf("exact CRLF limit rejected: vals=%v err=%v", vals, err)
	}
	vals, err = ParseSSOValuesBounded([]byte(strings.Repeat(" ", 32)+"ok\n#"+strings.Repeat("x", 32)), 10, 4)
	if err != nil || len(vals) != 1 || vals[0] != "ok" {
		t.Fatalf("ignored whitespace/comment rejected: vals=%v err=%v", vals, err)
	}
	if _, err := ParseSSOValuesBounded([]byte(`{"sso":"12345"}`), 10, 4); !errors.Is(err, ErrValueLimit) {
		t.Fatalf("expected JSON value limit, got %v", err)
	}
}

func TestConverterMockInsertsAccounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/convert" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		var req struct {
			Items []struct {
				SSO string `json:"sso"`
			} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		type result struct {
			Index      int               `json:"index"`
			OK         bool              `json:"ok"`
			Credential map[string]string `json:"credential"`
		}
		out := make([]result, len(req.Items))
		for i, item := range req.Items {
			out[i] = result{
				Index: i,
				OK:    true,
				Credential: map[string]string{
					"key":            "at-from-" + item.SSO,
					"refresh_token":  "rt-from-" + item.SSO,
					"email":          item.SSO + "@sso.local",
					"user_id":        "uid-" + item.SSO,
					"expires_at":     time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano),
					"oidc_issuer":    "https://auth.x.ai",
					"oidc_client_id": "b1a00492-073a-47ea-816f-4c329264a828",
				},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
	}))
	defer srv.Close()

	client, err := NewClient(Config{
		Endpoint:      srv.URL,
		APIKey:        "test-key",
		AllowInsecure: true,
		Timeout:       5 * time.Second,
		MaxBatch:      10,
		HTTPClient:    srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Ensure no redirect policy blocks; httptest client is fine.
	client.cfg.HTTPClient = srv.Client()
	client.cfg.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	converted, err := client.Convert(context.Background(), []string{"ssoA", "ssoB"})
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 2 || converted[0].Error != "" || converted[1].AccessToken == "" {
		t.Fatalf("converted: %+v", converted)
	}

	dir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dir, "sso.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	var accounts []catalog.Account
	now := time.Now()
	for _, c := range converted {
		a, err := ToAccount(c, now)
		if err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, a)
	}
	if err := cat.UpsertMany(accounts); err != nil {
		t.Fatal(err)
	}
	st, err := cat.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Count != 2 {
		t.Fatalf("count=%d", st.Count)
	}
}

func TestConverterRequiredError(t *testing.T) {
	_, err := NewClient(Config{})
	if err == nil {
		t.Fatal("expected converter required")
	}
}
