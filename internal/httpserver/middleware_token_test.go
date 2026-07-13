package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/yshgsh1343/grokbuild2api/internal/clients"
)

// TestTokenStoreAuth 覆盖令牌鉴权：401 / 402 额度用尽 / 成功扣额度 / 禁用 403。
func TestTokenStoreAuth(t *testing.T) {
	store, err := clients.Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	res, err := store.Create(clients.CreateRequest{
		Name: "t", RemainQuota: 1, MaxConcurrent: 0, Count: 1,
	})
	if err != nil || len(res) != 1 {
		t.Fatalf("create: %v %#v", err, res)
	}
	key := res[0].APIKey
	tokenID := res[0].Token.ID

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 上下文应带 AuthInfo
		if info, ok := ClientAuthFromContext(r.Context()); !ok || info.TokenID != tokenID {
			t.Errorf("missing auth context: ok=%v info=%+v", ok, info)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	})

	mw := &Middleware{TokenStore: store}
	h := Chain(okHandler, mw.RequireClient)

	// 缺 key → 401
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no key: want 401 got %d body=%s", rr.Code, rr.Body.String())
	}

	// 错误 key → 401
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer sk-not-exist")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad key: want 401 got %d", rr.Code)
	}

	// 有效 key → 200，并扣 1 额度
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("ok: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	// 额度用尽 → 402 Payment Required
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("x-api-key", key)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("quota: want 402 got %d body=%s", rr.Code, rr.Body.String())
	}

	// 禁用 → 403
	if err := store.SetEnabled(tokenID, false); err != nil {
		t.Fatal(err)
	}
	// 先补回额度以便走到 enabled 检查（额度仍为 0 会先 402；补无限）
	unlim := true
	if err := store.PatchQuota(tokenID, nil, &unlim, nil, nil); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("disabled: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestTokenStoreConcurrencyLimit 每令牌并发上限 → 503。
func TestTokenStoreConcurrencyLimit(t *testing.T) {
	store, err := clients.Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	res, err := store.Create(clients.CreateRequest{
		Name: "c", UnlimitedQuota: true, MaxConcurrent: 1, Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := res[0].APIKey
	info, err := store.Authenticate(nil, key)
	if err != nil {
		t.Fatal(err)
	}
	// 占满 1 槽
	if err := store.AcquireSlot(info.TokenID, info.MaxConcurrent, info.RPM); err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseSlot(info.TokenID, info.MaxConcurrent)

	mw := &Middleware{TokenStore: store}
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), mw.RequireClient)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

// TestTokenStoreMasterAPIKeyBypass 静态 master APIKey 在令牌库模式下仍可通行（运维）。
func TestTokenStoreMasterAPIKeyBypass(t *testing.T) {
	store, err := clients.Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mw := &Middleware{TokenStore: store, APIKey: "master-secret"}
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}), mw.RequireClient)

	req := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer master-secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("master bypass: want 200 got %d", rr.Code)
	}
}


// TestTokenUsageFromResponseBody mock 200 body 含 usage 时 used_quota 按 max(1,(in+out)/1000) 增加。
func TestTokenUsageFromResponseBody(t *testing.T) {
	store, err := clients.Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	res, err := store.Create(clients.CreateRequest{
		Name: "u", RemainQuota: 100, UnlimitedQuota: false, Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := res[0].APIKey
	id := res[0].Token.ID

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// 2500 tokens → cost 2
		_, _ = io.WriteString(w, `{"id":"r","usage":{"input_tokens":2000,"output_tokens":500}}`)
	})
	mw := &Middleware{TokenStore: store}
	h := Chain(handler, mw.RequireClient)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	list, err := store.List(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v", err)
	}
	if list[0].ID != id {
		t.Fatalf("id mismatch")
	}
	if list[0].UsedQuota != 2 {
		t.Fatalf("used_quota=%d want 2 (2500 tokens → cost 2)", list[0].UsedQuota)
	}
	if list[0].RequestCount != 1 {
		t.Fatalf("request_count=%d", list[0].RequestCount)
	}
	if list[0].RemainQuota != 98 {
		t.Fatalf("remain=%d want 98", list[0].RemainQuota)
	}

	// 无 usage → fallback +1
	handler2 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	h2 := Chain(handler2, mw.RequireClient)
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr = httptest.NewRecorder()
	h2.ServeHTTP(rr, req)
	list, _ = store.List(10)
	if list[0].UsedQuota != 3 {
		t.Fatalf("after fallback used=%d want 3", list[0].UsedQuota)
	}
}
