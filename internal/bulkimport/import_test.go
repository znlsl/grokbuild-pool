package bulkimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/ssoimport"
)

type recordingWriter struct {
	batches []int
}

func (w *recordingWriter) UpsertMany(accounts []catalog.Account) error {
	w.batches = append(w.batches, len(accounts))
	return nil
}

func TestJSONImportUpsertStats(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "creds.json")
	// 3 CPA-style accounts.
	var arr []map[string]any
	for i := 0; i < 3; i++ {
		arr = append(arr, map[string]any{
			"type":          "xai",
			"access_token":  fmt.Sprintf("at-%d", i),
			"refresh_token": fmt.Sprintf("rt-%d", i),
			"sub":           fmt.Sprintf("user-%d", i),
			"email":         fmt.Sprintf("u%d@example.com", i),
			"expired":       "2026-12-01T00:00:00Z",
		})
	}
	raw, _ := json.Marshal(arr)
	if err := os.WriteFile(in, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "pool.db")
	cat, err := catalog.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()

	rep, err := ImportPaths(context.Background(), cat, in, Config{
		Format: FormatJSON, Workers: 2, Batch: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK != 3 || rep.Failed != 0 {
		t.Fatalf("report: %+v", rep)
	}
	st, _ := cat.Stats()
	if st.Count != 3 {
		t.Fatalf("count=%d", st.Count)
	}
}

func TestSSOImportWithMockConverter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Items []struct {
				SSO string `json:"sso"`
			} `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type result struct {
			Index      int               `json:"index"`
			OK         bool              `json:"ok"`
			Credential map[string]string `json:"credential"`
		}
		out := make([]result, len(req.Items))
		for i, it := range req.Items {
			out[i] = result{Index: i, OK: true, Credential: map[string]string{
				"key": "at-" + it.SSO, "refresh_token": "rt-" + it.SSO,
				"email": it.SSO + "@t.local", "user_id": "id-" + it.SSO,
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
	}))
	defer srv.Close()

	client, err := ssoimport.NewClient(ssoimport.Config{
		Endpoint: srv.URL, APIKey: "k", AllowInsecure: true,
		HTTPClient: srv.Client(), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	in := filepath.Join(dir, "sso.txt")
	if err := os.WriteFile(in, []byte("cookie1\ncookie2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()

	rep, err := ImportPaths(context.Background(), cat, in, Config{
		Format: FormatSSO, Workers: 2, Converter: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK != 2 {
		t.Fatalf("report: %+v", rep)
	}
	st, _ := cat.Stats()
	if st.Count != 2 {
		t.Fatalf("count=%d", st.Count)
	}
}

func TestConcurrentJSONImport1k(t *testing.T) {
	dir := t.TempDir()
	// Split 1000 accounts across 8 JSON files for worker concurrency.
	const total = 1000
	const files = 8
	per := total / files
	for f := 0; f < files; f++ {
		var arr []map[string]any
		for i := 0; i < per; i++ {
			n := f*per + i
			arr = append(arr, map[string]any{
				"type":          "xai",
				"access_token":  fmt.Sprintf("at-%05d", n),
				"refresh_token": fmt.Sprintf("rt-%05d", n),
				"sub":           fmt.Sprintf("user-%05d", n),
				"email":         fmt.Sprintf("u%05d@example.com", n),
				"expired":       "2026-12-01T00:00:00Z",
			})
		}
		raw, _ := json.Marshal(arr)
		path := filepath.Join(dir, fmt.Sprintf("part-%02d.json", f))
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := filepath.Join(dir, "pool-1k.db")
	cat, err := catalog.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()

	start := time.Now()
	rep, err := ImportPaths(context.Background(), cat, dir, Config{
		Format: FormatJSON, Workers: 4, Batch: 100,
		ProgressEvery: 10_000, // quiet
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK != total {
		t.Fatalf("ok=%d want %d failed=%d report=%+v", rep.OK, total, rep.Failed, rep)
	}
	st, err := cat.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Count != total {
		t.Fatalf("Stats.Count=%d want %d", st.Count, total)
	}
	t.Logf("concurrent 1k import workers=4 elapsed=%s ok=%d", elapsed, rep.OK)
}

func TestSSOWithoutConverterFails(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "s.txt")
	_ = os.WriteFile(in, []byte("x\n"), 0o600)
	cat, err := catalog.Open(filepath.Join(dir, "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rep, err := ImportPaths(context.Background(), cat, in, Config{Format: FormatSSO, Workers: 1})
	if err != nil {
		// File-level errors are in report, not necessarily top-level err.
		t.Log(err)
	}
	if rep.OK != 0 {
		t.Fatalf("expected no ok without converter: %+v", rep)
	}
	if rep.Failed == 0 && rep.Files[0].Error == "" {
		t.Fatalf("expected failure: %+v", rep)
	}
}

func TestDryRunNoWrite(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "one.json")
	_ = os.WriteFile(in, []byte(`{"type":"xai","access_token":"a","refresh_token":"r","sub":"u","expired":"2026-01-01T00:00:00Z"}`), 0o600)
	cat, err := catalog.Open(filepath.Join(dir, "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rep, err := ImportPaths(context.Background(), cat, in, Config{Format: FormatJSON, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK != 1 {
		t.Fatalf("dry-run ok=%d", rep.OK)
	}
	st, _ := cat.Stats()
	if st.Count != 0 {
		t.Fatalf("dry-run wrote %d rows", st.Count)
	}
}

func TestJSONEntryLimit(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "two.json")
	raw := `[{"type":"xai","access_token":"a1","refresh_token":"r1","sub":"u1","expired":"2026-12-01T00:00:00Z"},{"type":"xai","access_token":"a2","refresh_token":"r2","sub":"u2","expired":"2026-12-01T00:00:00Z"}]`
	if err := os.WriteFile(in, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := ImportPaths(context.Background(), nil, in, Config{Format: FormatJSON, DryRun: true, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Files) != 1 || !strings.Contains(rep.Files[0].Error, "entry limit") {
		t.Fatalf("expected JSON entry limit, report=%+v", rep)
	}
}

func TestNDJSONLimits(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "two.ndjson")
	line := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"access_token":"a","refresh_token":"r","expires_at":2000000000}`+"\n", id)
	}
	if err := os.WriteFile(in, []byte(line("one")+line("two")), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := ImportPaths(context.Background(), nil, in, Config{
		Format: FormatNDJSON, DryRun: true, MaxEntries: 1, MaxNDJSONLineBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Files) != 1 || !strings.Contains(rep.Files[0].Error, "entry limit") {
		t.Fatalf("expected NDJSON entry limit, report=%+v", rep)
	}
}

func TestAggregateEntryLimitDoesNotPartiallyWrite(t *testing.T) {
	dir := t.TempDir()
	for fileIndex := 0; fileIndex < 2; fileIndex++ {
		var rows []map[string]any
		for rowIndex := 0; rowIndex < 2; rowIndex++ {
			id := fmt.Sprintf("%d-%d", fileIndex, rowIndex)
			rows = append(rows, map[string]any{
				"type": "xai", "access_token": "a-" + id, "refresh_token": "r-" + id,
				"sub": "u-" + id, "expired": "2026-12-01T00:00:00Z",
			})
		}
		raw, _ := json.Marshal(rows)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", fileIndex)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := catalog.Open(filepath.Join(dir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	if _, err := ImportPaths(context.Background(), cat, dir, Config{
		Format: FormatJSON, Batch: 1, MaxEntries: 3,
	}); err == nil {
		t.Fatal("expected aggregate entry limit")
	}
	stats, err := cat.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 0 {
		t.Fatalf("aggregate limit left %d partially imported rows", stats.Count)
	}
}

func TestBoundedMultiFileErrorDoesNotWriteValidSibling(t *testing.T) {
	dir := t.TempDir()
	valid := `[{"type":"xai","access_token":"a1","refresh_token":"r1","sub":"u1","expired":"2026-12-01T00:00:00Z"}]`
	oversized := `[{"type":"xai","access_token":"a2","refresh_token":"r2","sub":"u2","expired":"2026-12-01T00:00:00Z"},{"type":"xai","access_token":"a3","refresh_token":"r3","sub":"u3","expired":"2026-12-01T00:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(dir, "a-valid.json"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b-oversized.json"), []byte(oversized), 0o600); err != nil {
		t.Fatal(err)
	}
	writer := &recordingWriter{}
	if _, err := ImportPaths(context.Background(), writer, dir, Config{Format: FormatJSON, Batch: 1, MaxEntries: 1}); err == nil {
		t.Fatal("expected bounded multi-file validation error")
	}
	if len(writer.batches) != 0 {
		t.Fatalf("validation failure wrote batches %v", writer.batches)
	}
}

func TestBoundedImportStillHonorsBatchSize(t *testing.T) {
	dir := t.TempDir()
	var rows []map[string]any
	for i := 0; i < 5; i++ {
		rows = append(rows, map[string]any{
			"type": "xai", "access_token": fmt.Sprintf("a%d", i), "refresh_token": fmt.Sprintf("r%d", i),
			"sub": fmt.Sprintf("u%d", i), "expired": "2026-12-01T00:00:00Z",
		})
	}
	raw, _ := json.Marshal(rows)
	path := filepath.Join(dir, "accounts.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	writer := &recordingWriter{}
	if _, err := ImportPaths(context.Background(), writer, path, Config{Format: FormatJSON, Batch: 2, MaxEntries: 5}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(writer.batches); got != "[2 2 1]" {
		t.Fatalf("batches=%s", got)
	}
}

func TestJSONLFileUsesNDJSONAutoDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.jsonl")
	line := `{"id":"one","access_token":"a","refresh_token":"r","expires_at":2000000000}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := ImportPaths(context.Background(), nil, path, Config{Format: FormatAuto, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK != 1 || len(rep.Files) != 1 || rep.Files[0].Format != FormatNDJSON {
		t.Fatalf("report=%+v", rep)
	}
}

func TestSSOValueLengthLimit(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "sso.txt")
	if err := os.WriteFile(in, []byte("0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := ImportPaths(context.Background(), nil, in, Config{
		Format: FormatSSO, DryRun: true, MaxEntries: 10, MaxSSOValueBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Files) != 1 || !strings.Contains(rep.Files[0].Error, "value limit exceeded") {
		t.Fatalf("expected SSO value limit, report=%+v", rep)
	}
}
