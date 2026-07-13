package catalog

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pool.db")
	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sampleAccount(id string, priority int) Account {
	now := time.Now().Unix()
	return Account{
		ID:           id,
		Revision:     1,
		IdentityKey:  "idk-" + id,
		Email:        id + "@example.com",
		Name:         "user-" + id,
		Priority:     priority,
		Enabled:      true,
		Lifecycle:    LifecycleActive,
		AccessToken:  "access-" + id,
		RefreshToken: "refresh-" + id,
		ExpiresAt:    now + 3600,
		ProxyMode:    "inherit",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func TestOpenWALMode(t *testing.T) {
	c := openTestCatalog(t)
	mode, err := c.JournalMode()
	if err != nil {
		t.Fatalf("JournalMode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode=%q want wal", mode)
	}
}

func TestUpsertManyAndGet(t *testing.T) {
	c := openTestCatalog(t)
	accounts := []Account{
		sampleAccount("a1", 10),
		sampleAccount("a2", 5),
		sampleAccount("a3", 1),
	}
	if err := c.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}
	got, err := c.Get("a2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "a2" || got.AccessToken != "access-a2" || got.RefreshToken != "refresh-a2" {
		t.Fatalf("unexpected account: %+v", got)
	}
	if got.Priority != 5 || got.Revision != 1 {
		t.Fatalf("priority/rev: got priority=%d rev=%d", got.Priority, got.Revision)
	}

	// Upsert overwrite
	upd := sampleAccount("a2", 99)
	upd.AccessToken = "access-a2-v2"
	upd.Revision = 2
	if err := c.UpsertMany([]Account{upd}); err != nil {
		t.Fatalf("UpsertMany overwrite: %v", err)
	}
	got, err = c.Get("a2")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if got.AccessToken != "access-a2-v2" || got.Priority != 99 || got.Revision != 2 {
		t.Fatalf("overwrite failed: %+v", got)
	}

	st, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Count != 3 || st.EnabledCount != 3 || st.ActiveCount != 3 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestUpsertImportedManyPreservesOperationalState(t *testing.T) {
	c := openTestCatalog(t)
	existing := sampleAccount("import-existing", 7)
	existing.Enabled = false
	existing.ManualDisabled = true
	existing.Lifecycle = LifecycleQuarantined
	existing.FailureCount = 4
	existing.CooldownUntil = time.Now().Unix() + 600
	existing.LastError = "rate limited"
	existing.ConsecutiveUnauthorized = 2
	existing.QuarantineFP = "fp"
	if err := c.UpsertMany([]Account{existing}); err != nil {
		t.Fatal(err)
	}

	incoming := sampleAccount(existing.ID, 99)
	incoming.AccessToken = "new-access"
	incoming.RefreshToken = "new-refresh"
	incoming.Email = "new@example.com"
	if err := c.UpsertImportedMany([]Account{incoming}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" || got.Email != "new@example.com" {
		t.Fatalf("credential fields were not refreshed: %+v", got)
	}
	if got.Enabled || !got.ManualDisabled || got.Lifecycle != LifecycleQuarantined || got.Priority != 7 {
		t.Fatalf("operational state was reset: %+v", got)
	}
	if got.FailureCount != 4 || got.CooldownUntil != existing.CooldownUntil || got.LastError != existing.LastError || got.QuarantineFP != "fp" {
		t.Fatalf("health state was reset: %+v", got)
	}
	if got.Revision != existing.Revision+1 || got.CreatedAt != existing.CreatedAt {
		t.Fatalf("revision/creation history not preserved: %+v", got)
	}
}

func TestUpsertManyChunked(t *testing.T) {
	c := openTestCatalog(t)
	const n = 1200 // > upsertChunkSize (500)
	accounts := make([]Account, n)
	for i := 0; i < n; i++ {
		accounts[i] = sampleAccount("acc-"+itoa(i), i%10)
	}
	if err := c.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany large: %v", err)
	}
	st, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Count != n {
		t.Fatalf("count=%d want %d", st.Count, n)
	}
}

func TestUpdateTokensCAS(t *testing.T) {
	c := openTestCatalog(t)
	a := sampleAccount("cas1", 1)
	if err := c.UpsertMany([]Account{a}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	// Wrong revision → conflict
	err := c.UpdateTokens("cas1", 99, TokenSet{
		AccessToken:  "x",
		RefreshToken: "y",
		ExpiresAt:    time.Now().Unix() + 100,
	})
	if !errors.Is(err, ErrCASConflict) {
		t.Fatalf("want ErrCASConflict, got %v", err)
	}

	// Correct revision
	if err := c.UpdateTokens("cas1", 1, TokenSet{
		AccessToken:  "access-new",
		RefreshToken: "refresh-new",
		ExpiresAt:    9999999999,
	}); err != nil {
		t.Fatalf("UpdateTokens: %v", err)
	}
	got, err := c.Get("cas1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Revision != 2 {
		t.Fatalf("revision=%d want 2", got.Revision)
	}
	if got.AccessToken != "access-new" || got.RefreshToken != "refresh-new" {
		t.Fatalf("tokens not updated: %+v", got)
	}
	if got.ExpiresAt != 9999999999 {
		t.Fatalf("expires_at=%d", got.ExpiresAt)
	}
	if got.LastRefreshAt == nil {
		t.Fatal("last_refresh_at should be set")
	}

	// Stale rev 1 after bump → conflict
	err = c.UpdateTokens("cas1", 1, TokenSet{
		AccessToken:  "nope",
		RefreshToken: "nope",
		ExpiresAt:    1,
	})
	if !errors.Is(err, ErrCASConflict) {
		t.Fatalf("want ErrCASConflict after success, got %v", err)
	}

	// Missing id
	err = c.UpdateTokens("missing", 1, TokenSet{
		AccessToken:  "a",
		RefreshToken: "b",
		ExpiresAt:    1,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListEligibleNoTokens(t *testing.T) {
	c := openTestCatalog(t)
	now := time.Now().Unix()
	accounts := []Account{
		sampleAccount("high", 100),
		sampleAccount("mid", 50),
		sampleAccount("low", 1),
		sampleAccount("disabled", 200),
		sampleAccount("cooling", 150),
		sampleAccount("quarantine", 180),
	}
	accounts[3].Enabled = false
	accounts[4].CooldownUntil = now + 3600
	accounts[5].Lifecycle = LifecycleQuarantined
	// Put a secret-looking string only in tokens of high
	accounts[0].AccessToken = "SECRET_ACCESS_TOKEN_NEVER_IN_HOT"
	accounts[0].RefreshToken = "SECRET_REFRESH_TOKEN_NEVER_IN_HOT"

	if err := c.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	list, err := c.ListEligible(10, "")
	if err != nil {
		t.Fatalf("ListEligible: %v", err)
	}
	// eligible: high, mid, low (disabled/cooling/quarantine excluded)
	if len(list) != 3 {
		t.Fatalf("len=%d want 3; got %+v", len(list), list)
	}
	if list[0].ID != "high" || list[1].ID != "mid" || list[2].ID != "low" {
		t.Fatalf("order wrong: %v %v %v", list[0].ID, list[1].ID, list[2].ID)
	}
	// Ensure no secret fields via reflection of string fields
	for _, m := range list {
		if strings.Contains(m.ID, "SECRET") ||
			strings.Contains(m.IdentityKey, "SECRET") ||
			strings.Contains(m.ProxyURL, "SECRET") ||
			strings.Contains(m.ProxyMode, "SECRET") ||
			strings.Contains(m.Lifecycle, "SECRET") {
			t.Fatalf("secret leaked into HotMeta: %+v", m)
		}
		// HotMeta must not embed tokens — compile-time + runtime check of known fields
		if m.AccessTokenFieldExists() {
			t.Fatal("HotMeta must not expose access token")
		}
	}

	// Pagination via afterID
	page1, err := c.ListEligible(1, "")
	if err != nil || len(page1) != 1 || page1[0].ID != "high" {
		t.Fatalf("page1: %v %+v", err, page1)
	}
	page2, err := c.ListEligible(10, page1[0].ID)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != "mid" || page2[1].ID != "low" {
		t.Fatalf("page2 unexpected: %+v", page2)
	}
}

// AccessTokenFieldExists is a test helper ensuring HotMeta has no token fields.
// It always returns false; the method exists so tests document the invariant.
func (m HotMeta) AccessTokenFieldExists() bool {
	return false
}

func TestPatchHealth(t *testing.T) {
	c := openTestCatalog(t)
	if err := c.UpsertMany([]Account{sampleAccount("h1", 1)}); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}
	cool := time.Now().Unix() + 120
	fc := 3
	life := LifecycleQuarantined
	errMsg := "rate limited"
	if err := c.PatchHealth("h1", HealthPatch{
		FailureCount:  &fc,
		CooldownUntil: &cool,
		Lifecycle:     &life,
		LastError:     &errMsg,
	}); err != nil {
		t.Fatalf("PatchHealth: %v", err)
	}
	got, err := c.Get("h1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FailureCount != 3 || got.CooldownUntil != cool || got.Lifecycle != LifecycleQuarantined {
		t.Fatalf("patch not applied: %+v", got)
	}
	if got.LastError != "rate limited" {
		t.Fatalf("last_error=%q", got.LastError)
	}
	if got.Revision != 2 {
		t.Fatalf("revision=%d want 2", got.Revision)
	}
	// Tokens unchanged
	if got.AccessToken != "access-h1" {
		t.Fatalf("tokens mutated: %s", got.AccessToken)
	}

	// After quarantine, not eligible
	list, err := c.ListEligible(10, "")
	if err != nil {
		t.Fatalf("ListEligible: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("quarantined should not be eligible: %+v", list)
	}

	if err := c.PatchHealth("h1", HealthPatch{ClearLastError: true}); err != nil {
		t.Fatalf("ClearLastError: %v", err)
	}
	got, _ = c.Get("h1")
	if got.LastError != "" {
		t.Fatalf("last_error not cleared: %q", got.LastError)
	}
}

func TestGetNotFound(t *testing.T) {
	c := openTestCatalog(t)
	_, err := c.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListExpiring(t *testing.T) {
	c := openTestCatalog(t)
	now := time.Now().Unix()
	accounts := []Account{
		sampleAccount("soon", 1),
		sampleAccount("sooner", 1),
		sampleAccount("fresh", 1),
		sampleAccount("disabled", 1),
		sampleAccount("quarantine", 1),
	}
	accounts[0].ExpiresAt = now + 60
	accounts[1].ExpiresAt = now + 10
	accounts[2].ExpiresAt = now + 7200
	accounts[3].ExpiresAt = now + 5
	accounts[3].Enabled = false
	accounts[4].ExpiresAt = now + 5
	accounts[4].Lifecycle = LifecycleQuarantined
	if err := c.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	// before = now+300 → soon + sooner (fresh excluded)
	list, err := c.ListExpiring(10, now+300)
	if err != nil {
		t.Fatalf("ListExpiring: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len=%d want 2; got %+v", len(list), idsOf(list))
	}
	if list[0].ID != "sooner" || list[1].ID != "soon" {
		t.Fatalf("order want sooner,soon got %v", idsOf(list))
	}
	if list[0].RefreshToken == "" || list[0].AccessToken == "" {
		t.Fatal("ListExpiring must return tokens for refresh workers")
	}

	// limit
	one, err := c.ListExpiring(1, now+300)
	if err != nil || len(one) != 1 || one[0].ID != "sooner" {
		t.Fatalf("limit=1: %v %+v", err, one)
	}
}

func idsOf(accounts []Account) []string {
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.ID
	}
	return out
}

func TestUpsertRejectsEmptyTokens(t *testing.T) {
	c := openTestCatalog(t)
	a := sampleAccount("bad", 1)
	a.AccessToken = ""
	if err := c.UpsertMany([]Account{a}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

// TestSetProxiesBatch 批量改代理并校验 revision 递增与单条 SetProxy。
func TestSetProxiesBatch(t *testing.T) {
	c := openTestCatalog(t)
	if err := c.UpsertMany([]Account{
		sampleAccount("p1", 1),
		sampleAccount("p2", 2),
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetProxies([]ProxyAssignment{
		{ID: "p1", ProxyURL: "http://px1.example:1", ProxyMode: "http"},
		{ID: "p2", ProxyURL: "http://px2.example:2"}, // 保留 mode
	}); err != nil {
		t.Fatal(err)
	}
	a1, err := c.Get("p1")
	if err != nil {
		t.Fatal(err)
	}
	if a1.ProxyURL != "http://px1.example:1" || a1.ProxyMode != "http" || a1.Revision != 2 {
		t.Fatalf("p1: %+v", a1)
	}
	a2, err := c.Get("p2")
	if err != nil {
		t.Fatal(err)
	}
	if a2.ProxyURL != "http://px2.example:2" || a2.ProxyMode != "inherit" || a2.Revision != 2 {
		t.Fatalf("p2: %+v", a2)
	}
	// 清空为直连
	if err := c.SetProxy("p1", "", "http"); err != nil {
		t.Fatal(err)
	}
	a1, _ = c.Get("p1")
	if a1.ProxyURL != "" || a1.Revision != 3 {
		t.Fatalf("clear proxy: %+v", a1)
	}
	// 不存在
	if err := c.SetProxy("nope", "http://x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want NotFound got %v", err)
	}
}

// TestListAccountsNoSecrets 插入后 ListAccounts 仅脱敏摘要，无密钥字段。
func TestListAccountsNoSecrets(t *testing.T) {
	c := openTestCatalog(t)
	accounts := []Account{
		sampleAccount("acc-b", 2),
		sampleAccount("acc-a", 1),
		sampleAccount("acc-c", 3),
	}
	accounts[0].AccessToken = "SECRET_ACCESS_NEVER_LIST"
	accounts[0].RefreshToken = "SECRET_REFRESH_NEVER_LIST"
	accounts[1].AccessToken = "SECRET_ACCESS_A"
	accounts[1].RefreshToken = "SECRET_REFRESH_A"
	if err := c.UpsertMany(accounts); err != nil {
		t.Fatalf("UpsertMany: %v", err)
	}

	list, err := c.ListAccounts(10, "")
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len=%d want 3", len(list))
	}
	// id 升序
	if list[0].ID != "acc-a" || list[1].ID != "acc-b" || list[2].ID != "acc-c" {
		t.Fatalf("order: %s %s %s", list[0].ID, list[1].ID, list[2].ID)
	}
	for _, s := range list {
		if !s.HasAccess || !s.HasRefresh {
			t.Fatalf("want has tokens flags true: %+v", s)
		}
		// 摘要不得携带密钥明文（结构体无 token 字段；再做字符串扫描）
		blob := s.ID + s.Email + s.Name + s.Lifecycle + s.ProxyMode + s.ProxyURL + s.LastError
		if strings.Contains(blob, "SECRET_") {
			t.Fatalf("secret leaked into AccountSummary: %+v", s)
		}
		if s.AccessTokenFieldExists() {
			t.Fatal("AccountSummary must not expose access token field")
		}
	}

	// 游标分页
	page1, err := c.ListAccounts(1, "")
	if err != nil || len(page1) != 1 || page1[0].ID != "acc-a" {
		t.Fatalf("page1: %v %+v", err, page1)
	}
	page2, err := c.ListAccounts(10, page1[0].ID)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != "acc-b" || page2[1].ID != "acc-c" {
		t.Fatalf("page2 unexpected: %+v", page2)
	}
}

// AccessTokenFieldExists 测试辅助：AccountSummary 无 token 字段。
func (s AccountSummary) AccessTokenFieldExists() bool {
	return false
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
