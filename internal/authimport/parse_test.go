package authimport

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

func TestParseGrokAuthJSON_MapShape(t *testing.T) {
	const fixture = `{
  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
    "key": "access-token-fixture",
    "auth_mode": "oidc",
    "create_time": "2026-07-09T13:32:31.815457884Z",
    "user_id": "user-fixture-id",
    "email": "fixture@example.com",
    "first_name": "fixture",
    "principal_type": "User",
    "principal_id": "user-fixture-id",
    "team_id": "team-fixture-id",
    "coding_data_retention_opt_out": false,
    "refresh_token": "refresh-token-fixture",
    "expires_at": "2026-07-09T19:32:31.815457884Z",
    "oidc_issuer": "https://auth.x.ai",
    "oidc_client_id": "b1a00492-073a-47ea-816f-4c329264a828"
  }
}`
	creds, err := ParseGrokAuthJSON([]byte(fixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("want 1 cred, got %d", len(creds))
	}
	c := creds[0]
	if c.AccessToken != "access-token-fixture" {
		t.Errorf("access = %q", c.AccessToken)
	}
	if c.RefreshToken != "refresh-token-fixture" {
		t.Errorf("refresh = %q", c.RefreshToken)
	}
	if c.Email != "fixture@example.com" {
		t.Errorf("email = %q", c.Email)
	}
	if c.UserID != "user-fixture-id" {
		t.Errorf("user_id = %q", c.UserID)
	}
	if c.OIDCClientID != DefaultClientID {
		t.Errorf("client_id = %q", c.OIDCClientID)
	}
	if c.OIDCIssuer != Issuer {
		t.Errorf("issuer = %q", c.OIDCIssuer)
	}
	if c.ExpiresAt.IsZero() {
		t.Fatal("expires_at not parsed")
	}
}

func TestParseCPAMinimalAndToAccount(t *testing.T) {
	raw := `{"type":"xai","access_token":"at-cpa-1","refresh_token":"rt-cpa-1","expired":"2026-08-01T00:00:00Z","sub":"sub-1","email":"cpa@example.com"}`
	creds, err := ParseGrokAuthJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("want 1, got %d", len(creds))
	}
	a, err := ToAccount(creds[0], time.Unix(1_700_000_000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.AccessToken != "at-cpa-1" || a.RefreshToken != "rt-cpa-1" {
		t.Fatalf("account: %+v", a)
	}
	if a.IdentityKey == "" {
		t.Fatal("identity empty")
	}
	if !a.Enabled {
		t.Fatal("expected enabled")
	}
}

func TestParseMultiDocJSON(t *testing.T) {
	raw := `{"key":"a1","refresh_token":"r1","email":"a@x.ai","user_id":"u1"}
{"key":"a2","refresh_token":"r2","email":"b@x.ai","user_id":"u2"}`
	creds, err := ParseGrokAuthJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 {
		t.Fatalf("want 2, got %d", len(creds))
	}
	if _, _, err := ParseGrokAuthJSONDetailedLimit([]byte(raw), 1); !errors.Is(err, ErrImportEntryLimit) {
		t.Fatalf("expected bounded multi-doc rejection, got %v", err)
	}
}

func TestParseArrayAndAccountsWrapper(t *testing.T) {
	raw := `{"accounts":[{"key":"k1","refresh_token":"r1","user_id":"u1","email":"e1@x.ai"},{"access_token":"k2","refresh_token":"r2","user_id":"u2","email":"e2@x.ai","type":"xai","sub":"u2"}]}`
	creds, err := ParseGrokAuthJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 {
		t.Fatalf("want 2, got %d", len(creds))
	}
}

func TestUpsertJSONFixtureIntoCatalog(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cat, err := catalog.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()

	raw := `[
  {"type":"xai","access_token":"at-1","refresh_token":"rt-1","sub":"user-1","email":"one@example.com","expired":"2026-09-01T00:00:00Z"},
  {"type":"xai","access_token":"at-2","refresh_token":"rt-2","sub":"user-2","email":"two@example.com","expired":"2026-09-02T00:00:00Z"}
]`
	accounts, _, err := ParseFileBytes([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.UpsertMany(accounts); err != nil {
		t.Fatal(err)
	}
	st, err := cat.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Count != 2 {
		t.Fatalf("Stats.Count=%d want 2", st.Count)
	}
	// Idempotent re-import.
	if err := cat.UpsertMany(accounts); err != nil {
		t.Fatal(err)
	}
	st, _ = cat.Stats()
	if st.Count != 2 {
		t.Fatalf("after re-import Count=%d", st.Count)
	}
}

func TestRejectUntrustedIssuer(t *testing.T) {
	raw := `{"key":"a","refresh_token":"r","oidc_issuer":"https://evil.example"}`
	if _, err := ParseGrokAuthJSON([]byte(raw)); err == nil {
		t.Fatal("expected untrusted issuer error")
	}
}

func TestCPARequiresType(t *testing.T) {
	raw := `{"access_token":"a","refresh_token":"r","sub":"s1","expired":"2026-01-01T00:00:00Z"}`
	if _, err := ParseGrokAuthJSON([]byte(raw)); err == nil {
		t.Fatal("expected type=xai required for CPA-like entry")
	}
}

func TestBoundedCountUsesLastDuplicateTokenField(t *testing.T) {
	raw := `{"access_token":"temporary","access_token":"","first":{"key":"k1"},"second":{"key":"k2"}}`
	if _, _, err := ParseGrokAuthJSONDetailedLimit([]byte(raw), 1); !errors.Is(err, ErrImportEntryLimit) {
		t.Fatalf("expected two mapped credentials to exceed limit, got %v", err)
	}
}
