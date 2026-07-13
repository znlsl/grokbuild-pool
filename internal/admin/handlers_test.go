package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/clients"
	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
)

// 构造带临时 tokens.db 的 Handlers。
func testHandlers(t *testing.T, adminKey string) (*Handlers, *clients.Store) {
	t.Helper()
	store, err := clients.Open(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.AdminKey = adminKey
	h := &Handlers{
		AdminKey:  adminKey,
		Config:    cfg,
		Tokens:    store,
		Version:   "test",
		StartedAt: time.Now(),
	}
	return h, store
}

func mountAdmin(h *Handlers) http.Handler {
	mux := http.NewServeMux()
	h.Mount(mux)
	return mux
}

func doJSON(t *testing.T, h http.Handler, method, path, adminKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if adminKey != "" {
		req.Header.Set("Authorization", "Bearer "+adminKey)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestRequireAdmin401 无密钥 / 错误密钥 → 401；空配置 → 503。
func TestRequireAdmin401(t *testing.T) {
	h, _ := testHandlers(t, "secret-admin")
	mux := mountAdmin(h)

	// 无密钥
	rr := doJSON(t, mux, http.MethodGet, "/admin/tokens", "", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no key: want 401 got %d body=%s", rr.Code, rr.Body.String())
	}

	// 错误密钥
	rr = doJSON(t, mux, http.MethodGet, "/admin/tokens", "wrong", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad key: want 401 got %d", rr.Code)
	}

	// x-admin-key 正确
	req := httptest.NewRequest(http.MethodGet, "/admin/tokens", nil)
	req.Header.Set("x-admin-key", "secret-admin")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("x-admin-key: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	// 未配置 admin_key → 503
	hEmpty := &Handlers{AdminKey: "", Tokens: h.Tokens, Config: config.Default()}
	muxEmpty := mountAdmin(hEmpty)
	rr = doJSON(t, muxEmpty, http.MethodGet, "/admin/tokens", "any", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty admin_key: want 503 got %d", rr.Code)
	}
}

// TestCreateListDisable 发放 / 列表（无明文）/ 禁用。
func TestCreateListDisable(t *testing.T) {
	h, store := testHandlers(t, "adm")
	mux := mountAdmin(h)

	// 创建单条
	rr := doJSON(t, mux, http.MethodPost, "/admin/tokens", "adm", map[string]any{
		"name": "demo", "remain_quota": 5, "max_concurrent": 2, "rpm": 60, "count": 1,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	plain, _ := created["api_key"].(string)
	if plain == "" || plain[:3] != "sk-" {
		t.Fatalf("missing plaintext api_key: %#v", created)
	}
	// tokens 数组也带明文
	tokensArr, _ := created["tokens"].([]any)
	if len(tokensArr) != 1 {
		t.Fatalf("tokens len=%d", len(tokensArr))
	}

	// 列表：不得含明文密钥
	rr = doJSON(t, mux, http.MethodGet, "/admin/tokens", "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	listBody := rr.Body.String()
	if bytes.Contains(rr.Body.Bytes(), []byte(plain)) {
		t.Fatalf("list leaked plaintext key")
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"key_prefix"`)) {
		t.Fatalf("list missing key_prefix: %s", listBody)
	}

	// 解析 id
	var listResp struct {
		Tokens []clients.Token `json:"tokens"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Tokens) != 1 {
		t.Fatalf("list n=%d", len(listResp.Tokens))
	}
	id := listResp.Tokens[0].ID
	if id == "" {
		t.Fatal("empty id")
	}

	// 禁用
	rr = doJSON(t, mux, http.MethodPost, "/admin/tokens/"+id+"/disable", "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rr.Code, rr.Body.String())
	}
	// 鉴权应失败（禁用）
	if _, err := store.Authenticate(nil, plain); err != clients.ErrDisabled {
		t.Fatalf("want ErrDisabled got %v", err)
	}

	// 再启用
	rr = doJSON(t, mux, http.MethodPost, "/admin/tokens/"+id+"/enable", "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := store.Authenticate(nil, plain); err != nil {
		t.Fatalf("re-enable auth: %v", err)
	}

	// 批量创建
	rr = doJSON(t, mux, http.MethodPost, "/admin/tokens", "adm", map[string]any{
		"name": "batch", "unlimited_quota": true, "count": 3,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("batch: %d %s", rr.Code, rr.Body.String())
	}
	var batch map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &batch)
	if int(batch["created"].(float64)) != 3 {
		t.Fatalf("created=%v", batch["created"])
	}

	// 删除
	rr = doJSON(t, mux, http.MethodDelete, "/admin/tokens/"+id, "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, mux, http.MethodDelete, "/admin/tokens/"+id, "adm", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("delete again: want 404 got %d", rr.Code)
	}
}

// TestPatchQuota 调整额度后鉴权行为变化。
func TestPatchQuota(t *testing.T) {
	h, store := testHandlers(t, "adm")
	mux := mountAdmin(h)

	rr := doJSON(t, mux, http.MethodPost, "/admin/tokens", "adm", map[string]any{
		"name": "q", "remain_quota": 10, "count": 1,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		APIKey string        `json:"api_key"`
		Token  clients.Token `json:"token"`
		Tokens []struct {
			Token clients.Token `json:"token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Token.ID
	if id == "" && len(created.Tokens) > 0 {
		id = created.Tokens[0].Token.ID
	}
	key := created.APIKey

	// 将额度打到 0
	zero := int64(0)
	rr = doJSON(t, mux, http.MethodPatch, "/admin/tokens/"+id, "adm", map[string]any{
		"remain_quota": zero,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := store.Authenticate(nil, key); err != clients.ErrQuotaExceeded {
		t.Fatalf("want quota exceeded got %v", err)
	}

	// 打开无限额度
	rr = doJSON(t, mux, http.MethodPatch, "/admin/tokens/"+id, "adm", map[string]any{
		"unlimited_quota": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("patch unlimited: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := store.Authenticate(nil, key); err != nil {
		t.Fatalf("unlimited auth: %v", err)
	}
}

// TestPoolStatsAndSafeConfig 仪表盘与安全配置（无密钥明文）。
func TestPoolStatsAndSafeConfig(t *testing.T) {
	h, _ := testHandlers(t, "adm")
	mux := mountAdmin(h)

	rr := doJSON(t, mux, http.MethodGet, "/admin/pool/stats", "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"tokens_total", "pool_hot_size", "success_rate", "version"} {
		if !bytes.Contains(rr.Body.Bytes(), []byte(want)) {
			t.Fatalf("stats missing %s: %s", want, body)
		}
	}

	rr = doJSON(t, mux, http.MethodGet, "/admin/config", "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("config: %d %s", rr.Code, rr.Body.String())
	}
	var cfg map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	// 不得返回密钥明文；仅布尔位
	if _, ok := cfg["admin_key"]; ok {
		t.Fatal("safe config must not include admin_key field")
	}
	if _, ok := cfg["api_key"]; ok {
		t.Fatal("safe config must not include api_key field")
	}
	if cfg["admin_key_set"] != true {
		t.Fatalf("admin_key_set missing/false: %s", rr.Body.String())
	}
}

// TestCreateTokensStoreMissing 未挂载令牌库 → 503。
func TestCreateTokensStoreMissing(t *testing.T) {
	h := &Handlers{AdminKey: "adm", Config: config.Default()}
	mux := mountAdmin(h)
	rr := doJSON(t, mux, http.MethodPost, "/admin/tokens", "adm", map[string]any{"name": "x"})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 got %d", rr.Code)
	}
}

// TestAccountsDisableEnable 账号分页列表（无密钥）+ disable/enable + 热池同步。
func TestAccountsDisableEnable(t *testing.T) {
	catPath := filepath.Join(t.TempDir(), "pool.db")
	cat, err := catalog.Open(catPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })

	now := time.Now().Unix()
	acc := catalog.Account{
		ID:           "acct-1",
		Revision:     1,
		Email:        "a@example.com",
		Name:         "user-a",
		Priority:     10,
		Enabled:      true,
		Lifecycle:    catalog.LifecycleActive,
		AccessToken:  "SECRET_ACCESS_TOKEN_XYZ",
		RefreshToken: "SECRET_REFRESH_TOKEN_XYZ",
		ExpiresAt:    now + 3600,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := cat.UpsertMany([]catalog.Account{acc}); err != nil {
		t.Fatal(err)
	}

	idx := hot.New(hot.Config{HotSize: 10})
	if _, err := idx.LoadEligible(cat); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Get("acct-1"); !ok {
		t.Fatal("expected account in hot")
	}

	h, _ := testHandlers(t, "adm")
	h.Catalog = cat
	h.AccountHot = idx
	mux := mountAdmin(h)

	// 列表：不得含 token 明文
	rr := doJSON(t, mux, http.MethodGet, "/admin/accounts?limit=10", "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("SECRET_")) {
		t.Fatalf("list leaked secret: %s", rr.Body.String())
	}
	var listResp struct {
		Accounts []catalog.AccountSummary `json:"accounts"`
		Next     string                   `json:"next_cursor"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Accounts) != 1 || listResp.Accounts[0].ID != "acct-1" {
		t.Fatalf("list unexpected: %+v", listResp)
	}
	if !listResp.Accounts[0].HasAccess || !listResp.Accounts[0].HasRefresh {
		t.Fatalf("want has_access/has_refresh: %+v", listResp.Accounts[0])
	}

	// 禁用
	rr = doJSON(t, mux, http.MethodPost, "/admin/accounts/acct-1/disable", "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rr.Code, rr.Body.String())
	}
	got, err := cat.Get("acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || !got.ManualDisabled {
		t.Fatalf("after disable: enabled=%v manual=%v", got.Enabled, got.ManualDisabled)
	}
	if _, ok := idx.Get("acct-1"); ok {
		t.Fatal("disabled account should be demoted from hot")
	}

	// 启用
	// 先 Promote 回热池以验证 enable 路径更新 Enabled（或仅冷存储）
	rr = doJSON(t, mux, http.MethodPost, "/admin/accounts/acct-1/enable", "adm", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rr.Code, rr.Body.String())
	}
	got, err = cat.Get("acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.ManualDisabled {
		t.Fatalf("after enable: enabled=%v manual=%v", got.Enabled, got.ManualDisabled)
	}

	// 不存在
	rr = doJSON(t, mux, http.MethodPost, "/admin/accounts/nope/disable", "adm", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d", rr.Code)
	}
}

// TestBatchAccounts 批量 enable/disable：上限 500、鉴权、部分失败、热池同步。
func TestBatchAccounts(t *testing.T) {
	catPath := filepath.Join(t.TempDir(), "pool.db")
	cat, err := catalog.Open(catPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })

	now := time.Now().Unix()
	accs := []catalog.Account{
		{ID: "b1", Revision: 1, Email: "b1@e.com", Enabled: true, Lifecycle: catalog.LifecycleActive, AccessToken: "A1", RefreshToken: "R1", ExpiresAt: now + 3600, CreatedAt: now, UpdatedAt: now},
		{ID: "b2", Revision: 1, Email: "b2@e.com", Enabled: true, Lifecycle: catalog.LifecycleActive, AccessToken: "A2", RefreshToken: "R2", ExpiresAt: now + 3600, CreatedAt: now, UpdatedAt: now},
		{ID: "b3", Revision: 1, Email: "b3@e.com", Enabled: false, ManualDisabled: true, Lifecycle: catalog.LifecycleActive, AccessToken: "A3", RefreshToken: "R3", ExpiresAt: now + 3600, CreatedAt: now, UpdatedAt: now},
	}
	if err := cat.UpsertMany(accs); err != nil {
		t.Fatal(err)
	}
	idx := hot.New(hot.Config{HotSize: 10})
	if _, err := idx.LoadEligible(cat); err != nil {
		t.Fatal(err)
	}

	h, _ := testHandlers(t, "adm")
	h.Catalog = cat
	h.AccountHot = idx
	mux := mountAdmin(h)

	// 无鉴权 → 401
	rr := doJSON(t, mux, http.MethodPost, "/admin/accounts/batch", "", map[string]any{
		"action": "disable", "ids": []string{"b1"},
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: want 401 got %d", rr.Code)
	}

	// 非法 action
	rr = doJSON(t, mux, http.MethodPost, "/admin/accounts/batch", "adm", map[string]any{
		"action": "wipe", "ids": []string{"b1"},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad action: want 400 got %d %s", rr.Code, rr.Body.String())
	}

	// 空 ids
	rr = doJSON(t, mux, http.MethodPost, "/admin/accounts/batch", "adm", map[string]any{
		"action": "disable", "ids": []string{},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty ids: want 400 got %d", rr.Code)
	}

	// 超过 500
	tooMany := make([]string, 501)
	for i := range tooMany {
		tooMany[i] = "x" + strconv.Itoa(i)
	}
	rr = doJSON(t, mux, http.MethodPost, "/admin/accounts/batch", "adm", map[string]any{
		"action": "disable", "ids": tooMany,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("over limit: want 400 got %d %s", rr.Code, rr.Body.String())
	}

	// 批量禁用 b1 + 不存在 + 去重
	rr = doJSON(t, mux, http.MethodPost, "/admin/accounts/batch", "adm", map[string]any{
		"action": "disable", "ids": []string{"b1", "b1", "nope", "b2"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("batch disable: %d %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["ok"].(float64)) != 2 {
		t.Fatalf("ok=%v want 2 body=%v", resp["ok"], resp)
	}
	if int(resp["failed"].(float64)) != 1 {
		t.Fatalf("failed=%v want 1", resp["failed"])
	}
	for _, id := range []string{"b1", "b2"} {
		got, err := cat.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Enabled || !got.ManualDisabled {
			t.Fatalf("%s after disable: enabled=%v manual=%v", id, got.Enabled, got.ManualDisabled)
		}
		if _, ok := idx.Get(id); ok {
			t.Fatalf("%s should be demoted", id)
		}
	}

	// 批量启用 b3
	rr = doJSON(t, mux, http.MethodPost, "/admin/accounts/batch", "adm", map[string]any{
		"action": "enable", "ids": []string{"b3"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("batch enable: %d %s", rr.Code, rr.Body.String())
	}
	got, err := cat.Get("b3")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.ManualDisabled {
		t.Fatalf("b3 after enable: enabled=%v manual=%v", got.Enabled, got.ManualDisabled)
	}
}

// TestSettingsPersist Load/Apply 原子写 JSON。
func TestSettingsPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	ctl := &SettingsController{Path: path}
	in := RuntimeSettings{
		HotSize:               100,
		MaxInflightPerAccount: 4,
		MaxAttempts:           3,
		CooldownBaseSec:       2,
		CooldownCapSec:        60,
		ForbiddenCooldownSec:  30,
		Pow2K:                 2,
		MaxConcurrent:         50,
	}
	out, err := ctl.Apply(in)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out.HotSize != 100 {
		t.Fatalf("out=%+v", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"hot_size": 100`)) {
		t.Fatalf("persist missing hot_size: %s", data)
	}

	ctl2 := &SettingsController{Path: path}
	if err := ctl2.Load(); err != nil {
		t.Fatal(err)
	}
	snap := ctl2.Snapshot()
	if snap.HotSize != 100 || snap.PersistedPath != path {
		t.Fatalf("load snap=%+v", snap)
	}
}

func TestSettingsApplyHTTPLimits(t *testing.T) {
	var gotConc int = -1
	var gotBody int64 = -1
	var gotTimeout time.Duration = -1
	ctl := &SettingsController{
		SetGlobalMaxConcurrent: func(n int) { gotConc = n },
		SetMaxBodyBytes:        func(n int64) { gotBody = n },
		SetRequestTimeout:      func(d time.Duration) { gotTimeout = d },
	}
	in := RuntimeSettings{
		MaxInflightPerAccount: 1,
		CooldownBaseSec:       1,
		CooldownCapSec:        1,
		ForbiddenCooldownSec:  1,
		Pow2K:                 2,
		MaxAttempts:           2,
		MaxConcurrent:         0, // 允许 0
		MaxBodyBytes:          1234,
		RequestTimeoutSec:     9,
		RefreshWorkers:        0,
		RefreshQPS:            0,
		RefreshSkewSec:        0,
	}
	if _, err := ctl.Apply(in); err != nil {
		t.Fatal(err)
	}
	if gotConc != 0 {
		t.Fatalf("max concurrent=%d want 0", gotConc)
	}
	if gotBody != 1234 {
		t.Fatalf("max body=%d", gotBody)
	}
	if gotTimeout != 9*time.Second {
		t.Fatalf("timeout=%v", gotTimeout)
	}
}
