package admin

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/importjobs"
)

func newImportTestHandler(t *testing.T, configure func(*config.Config, *importjobs.Options)) (*Handlers, http.Handler, string) {
	t.Helper()
	dataDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	cfg := config.Default()
	cfg.AdminKey = "adm"
	cfg.DataDir = dataDir
	opts := importjobs.Options{
		MaxConcurrentJobs:  cfg.Imports.MaxConcurrentJobs,
		MaxEntries:         cfg.Imports.MaxEntries,
		MaxNDJSONLineBytes: cfg.Imports.MaxNDJSONLineBytes,
		MaxSSOValueBytes:   cfg.Imports.MaxSSOValueBytes,
		JobTimeout:         time.Minute,
		StagingStaleAfter:  time.Hour,
	}
	if configure != nil {
		configure(&cfg, &opts)
	}
	manager, err := importjobs.NewWithOptions(dataDir, cat, opts)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handlers{AdminKey: "adm", Config: cfg, Catalog: cat, ImportJobs: manager}
	return h, mountAdmin(h), dataDir
}

func multipartImportRequest(t *testing.T, path, key, format, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if format != "" {
		if err := writer.WriteField("format", format); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req
}

func TestMultipartJSONImportAndCleanup(t *testing.T) {
	_, handler, dataDir := newImportTestHandler(t, nil)
	content := []byte(`[{"type":"xai","access_token":"at","refresh_token":"rt","sub":"user","expired":"2026-12-01T00:00:00Z"}]`)
	req := multipartImportRequest(t, "/admin/import/jobs", "adm", "json", "../../accounts.json", content)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte(dataDir)) || bytes.Contains(rr.Body.Bytes(), []byte(`"path"`)) {
		t.Fatalf("response leaked internal path: %s", rr.Body.String())
	}
	var created importjobs.Job
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		getReq := httptest.NewRequest(http.MethodGet, "/admin/import/jobs/"+created.ID, nil)
		getReq.Header.Set("Authorization", "Bearer adm")
		getRR := httptest.NewRecorder()
		handler.ServeHTTP(getRR, getReq)
		if getRR.Code != http.StatusOK {
			t.Fatalf("get status=%d body=%s", getRR.Code, getRR.Body.String())
		}
		var job importjobs.Job
		if err := json.Unmarshal(getRR.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.State == importjobs.StateSucceeded {
			break
		}
		if job.State == importjobs.StateFailed {
			t.Fatalf("job failed: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, ".import-staging"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging files remain: %v", entries)
	}
}

func TestMultipartImportErrors(t *testing.T) {
	_, handler, dataDir := newImportTestHandler(t, func(cfg *config.Config, _ *importjobs.Options) {
		cfg.Imports.MaxUploadBytes = 8
	})

	unauth := multipartImportRequest(t, "/admin/import/jobs", "", "json", "a.json", []byte(`{}`))
	unauthRR := httptest.NewRecorder()
	handler.ServeHTTP(unauthRR, unauth)
	if unauthRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d", unauthRR.Code)
	}

	tooLarge := multipartImportRequest(t, "/admin/import/jobs", "adm", "json", "a.json", []byte("0123456789"))
	largeRR := httptest.NewRecorder()
	handler.ServeHTTP(largeRR, tooLarge)
	if largeRR.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large status=%d body=%s", largeRR.Code, largeRR.Body.String())
	}

	sso := multipartImportRequest(t, "/admin/import/jobs", "adm", "sso", "sso.txt", []byte("cookie"))
	ssoRR := httptest.NewRecorder()
	handler.ServeHTTP(ssoRR, sso)
	if ssoRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("sso status=%d body=%s", ssoRR.Code, ssoRR.Body.String())
	}

	entries, err := os.ReadDir(filepath.Join(dataDir, ".import-staging"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed requests left staging files: %v", entries)
	}

	_, requestLimited, _ := newImportTestHandler(t, func(cfg *config.Config, _ *importjobs.Options) {
		cfg.Imports.MaxUploadBytes = 1024
		cfg.Imports.MaxRequestBytes = 128
	})
	requestTooLarge := multipartImportRequest(t, "/admin/import/jobs", "adm", "json", "a.json", bytes.Repeat([]byte("x"), 64))
	requestRR := httptest.NewRecorder()
	requestLimited.ServeHTTP(requestRR, requestTooLarge)
	if requestRR.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("request limit status=%d body=%s", requestRR.Code, requestRR.Body.String())
	}
}

func TestServerPathProtocolDisabledByDefault(t *testing.T) {
	_, handler, _ := newImportTestHandler(t, nil)
	rr := doJSON(t, handler, http.MethodPost, "/admin/import/jobs", "adm", map[string]any{
		"format": "json", "path": "accounts.json",
	})
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
