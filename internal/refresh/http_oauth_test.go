package refresh_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/refresh"
)

func TestHTTPRefreshClient_Success(t *testing.T) {
	var gotGrant, gotClientID, gotRT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/x-www-form-urlencoded") {
			t.Errorf("content-type=%q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotGrant = r.Form.Get("grant_type")
		gotClientID = r.Form.Get("client_id")
		gotRT = r.Form.Get("refresh_token")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    7200,
			"token_type":    "Bearer",
		})
	}))
	defer srv.Close()

	client := refresh.NewHTTPRefreshClient(refresh.HTTPRefreshConfig{
		RefreshURL: srv.URL,
		ClientID:   "test-client-id",
		HTTPClient: srv.Client(),
	})
	set, err := client.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if set.AccessToken != "new-access" || set.RefreshToken != "new-refresh" {
		t.Fatalf("tokens=%+v", set)
	}
	if set.ExpiresAt < time.Now().Unix()+7000 {
		t.Fatalf("expires_at too soon: %d", set.ExpiresAt)
	}
	if gotGrant != "refresh_token" {
		t.Fatalf("grant_type=%q", gotGrant)
	}
	if gotClientID != "test-client-id" {
		t.Fatalf("client_id=%q", gotClientID)
	}
	if gotRT != "old-refresh" {
		t.Fatalf("refresh_token=%q", gotRT)
	}
}

func TestHTTPRefreshClient_KeepRefreshWhenOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "only-access",
			"expires_in":   60,
		})
	}))
	defer srv.Close()

	client := refresh.NewXaiOAuth(srv.URL, "")
	// inject client via config
	client = refresh.NewHTTPRefreshClient(refresh.HTTPRefreshConfig{
		RefreshURL: srv.URL,
		HTTPClient: srv.Client(),
	})
	set, err := client.Refresh(context.Background(), "keep-me")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if set.AccessToken != "only-access" || set.RefreshToken != "keep-me" {
		t.Fatalf("tokens=%+v", set)
	}
}

func TestHTTPRefreshClient_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "token revoked",
		})
	}))
	defer srv.Close()

	client := refresh.NewHTTPRefreshClient(refresh.HTTPRefreshConfig{
		RefreshURL: srv.URL,
		HTTPClient: srv.Client(),
	})
	_, err := client.Refresh(context.Background(), "bad")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err=%v", err)
	}
}

func TestHTTPRefreshClient_EmptyRefreshToken(t *testing.T) {
	client := refresh.NewHTTPRefreshClient(refresh.HTTPRefreshConfig{
		RefreshURL: "http://127.0.0.1:1/nope",
	})
	_, err := client.Refresh(context.Background(), "  ")
	if err == nil {
		t.Fatal("expected empty token error")
	}
}

func TestRealOAuthAllowed_Gates(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "STATUS.md")

	// default: env off, status false
	t.Setenv(refresh.EnvOAuthEnabled, "")
	if err := os.WriteFile(statusPath, []byte("UNLOCK_M12: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if refresh.RealOAuthAllowed(statusPath) {
		t.Fatal("should be false when env off")
	}

	// env on, unlock false
	t.Setenv(refresh.EnvOAuthEnabled, "1")
	if refresh.RealOAuthAllowed(statusPath) {
		t.Fatal("should be false when unlock false")
	}

	// both true
	if err := os.WriteFile(statusPath, []byte("# STATUS\nUNLOCK_M12: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !refresh.RealOAuthAllowed(statusPath) {
		t.Fatal("should be true when both gates open")
	}

	// env false again
	t.Setenv(refresh.EnvOAuthEnabled, "0")
	if refresh.RealOAuthAllowed(statusPath) {
		t.Fatal("should be false when env 0")
	}
}

func TestStatusUnlockM12_Parse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.md")
	if err := os.WriteFile(p, []byte("foo: bar\nUNLOCK_M12: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !refresh.StatusUnlockM12(p) {
		t.Fatal("want true")
	}
	if err := os.WriteFile(p, []byte("UNLOCK_M12: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if refresh.StatusUnlockM12(p) {
		t.Fatal("want false")
	}
	if refresh.StatusUnlockM12(filepath.Join(dir, "missing.md")) {
		t.Fatal("missing file should be false")
	}
}

func TestOAuthEnvEnabled(t *testing.T) {
	t.Setenv(refresh.EnvOAuthEnabled, "true")
	if !refresh.OAuthEnvEnabled() {
		t.Fatal("true")
	}
	t.Setenv(refresh.EnvOAuthEnabled, "")
	if refresh.OAuthEnvEnabled() {
		t.Fatal("empty")
	}
}
