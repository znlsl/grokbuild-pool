package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultListenNot8080(t *testing.T) {
	c := Default()
	if c.Listen != "127.0.0.1:18080" {
		t.Fatalf("listen=%q want 127.0.0.1:18080", c.Listen)
	}
	if c.Limits.MaxConcurrent != 120 {
		t.Fatalf("max_concurrent=%d want 120", c.Limits.MaxConcurrent)
	}
	if c.DataDir != DefaultDataDir {
		t.Fatalf("data_dir=%q", c.DataDir)
	}
	if !c.UseMockUpstream() {
		t.Fatal("default empty upstream should use mock")
	}
}

func TestValidateListenRejectsPublic8080(t *testing.T) {
	c := Default()
	if err := c.ValidateListen("0.0.0.0:8080"); err == nil {
		t.Fatal("expected reject public 8080")
	}
	if err := c.ValidateListen("127.0.0.1:18080"); err != nil {
		t.Fatal(err)
	}
	c.AllowPublicListen = true
	if err := c.ValidateListen("0.0.0.0:18080"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDBPathPreference(t *testing.T) {
	dir := t.TempDir()
	// prefer pool-10000.db
	p10 := filepath.Join(dir, "pool-10000.db")
	p := filepath.Join(dir, "pool.db")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p10, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.DataDir = dir
	got, err := cfg.ResolveDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != p10 {
		t.Fatalf("got %s want %s", got, p10)
	}
	// explicit db_path wins
	cfg.DBPath = p
	got, err = cfg.ResolveDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("got %s want %s", got, p)
	}
}

func TestImportDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	if !cfg.Imports.Enabled || cfg.Imports.MaxUploadBytes != 32<<20 || cfg.Imports.MaxEntries != 10_000 {
		t.Fatalf("unexpected import defaults: %+v", cfg.Imports)
	}
	if cfg.Imports.AllowServerPath {
		t.Fatal("server path import must default to disabled")
	}
	cfg.Imports.MaxRequestBytes = cfg.Imports.MaxUploadBytes + DefaultImportRequestOverhead - 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected multipart overhead validation error")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	tests := map[string]string{
		"root":   "listen: 127.0.0.1:18080\nmax_concurent: 5\n",
		"nested": "listen: 127.0.0.1:18080\nlimits:\n  max_concurent: 5\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected unknown field error")
			}
		})
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "listen: 127.0.0.1:18080\n---\nlisten: 127.0.0.1:18081\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected multiple document error")
	}
}

func TestValidateRejectsUnsafeAdminConfiguration(t *testing.T) {
	cfg := Default()
	cfg.AdminKey = "dev-admin-change-me"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected placeholder admin key rejection")
	}

	cfg = Default()
	cfg.Listen = "0.0.0.0:18080"
	cfg.AllowPublicListen = true
	cfg.AdminKey = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected public listen without admin key rejection")
	}

	cfg.AdminKey = "adm-safe-test-value"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("strong public configuration rejected: %v", err)
	}
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := `
listen: "127.0.0.1:18081"
data_dir: "` + dir + `"
hot_size: 100
limits:
  max_concurrent: 5
imports:
  max_upload_bytes: 2097152
  max_request_bytes: 3145728
  max_entries: 123
upstream:
  base_url: "https://example.invalid/v1"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:18081" {
		t.Fatalf("listen=%q", cfg.Listen)
	}
	if cfg.Limits.MaxConcurrent != 5 {
		t.Fatalf("max_concurrent=%d", cfg.Limits.MaxConcurrent)
	}
	if cfg.UseMockUpstream() {
		t.Fatal("base_url set should not force mock")
	}
	if cfg.HotSize != 100 {
		t.Fatalf("hot_size=%d", cfg.HotSize)
	}
	if cfg.Imports.MaxUploadBytes != 2<<20 || cfg.Imports.MaxEntries != 123 {
		t.Fatalf("imports=%+v", cfg.Imports)
	}
}
