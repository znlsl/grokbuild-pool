package importjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

func TestSafePathAndSubmitJSON(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 写入合法 CPA 风格 JSON 凭证文件
	jsonPath := filepath.Join(dataDir, "one.json")
	body := `[{"type":"xai","access_token":"at1","refresh_token":"rt1","sub":"user-1","email":"a@x.com","expired":"2026-12-01T00:00:00Z"}]`
	if err := os.WriteFile(jsonPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })

	m := New(dataDir, cat)

	// 逃逸路径拒绝
	if _, err := m.Submit(CreateRequest{Format: "json", Path: "/etc/passwd"}); err == nil {
		t.Fatal("expected path reject")
	}
	if _, err := m.Submit(CreateRequest{Format: "json", Path: filepath.Join(dataDir, "..", "outside.json")}); err == nil {
		t.Fatal("expected .. reject")
	}

	// 相对路径 OK
	job, err := m.Submit(CreateRequest{Format: "json", Path: "one.json"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || job.State != StateQueued && job.State != StateRunning {
		t.Fatalf("job=%+v", job)
	}

	// 等待完成
	deadline := time.Now().Add(5 * time.Second)
	var got Job
	for time.Now().Before(deadline) {
		got, err = m.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == StateSucceeded || got.State == StateFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.State != StateSucceeded {
		t.Fatalf("state=%s err=%s ok=%d fail=%d", got.State, got.Error, got.OK, got.Fail)
	}
	if got.OK < 1 {
		t.Fatalf("ok=%d", got.OK)
	}
	list := m.List(10)
	if len(list) < 1 {
		t.Fatal("list empty")
	}
}

func TestStagedUploadLifecycleAndRedaction(t *testing.T) {
	dataDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	m, err := NewWithOptions(dataDir, cat, Options{MaxEntries: 10, JobTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	stagingInfo, err := os.Stat(filepath.Join(dataDir, ".import-staging"))
	if err != nil {
		t.Fatal(err)
	}
	if stagingInfo.Mode().Perm() != 0o700 {
		t.Fatalf("staging mode=%o", stagingInfo.Mode().Perm())
	}
	body := `[{"type":"xai","access_token":"at1","refresh_token":"rt1","sub":"user-1","expired":"2026-12-01T00:00:00Z"}]`
	upload, err := m.StageUpload(bytes.NewBufferString(body), "../../secret.json", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(upload.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("upload mode=%o", fileInfo.Mode().Perm())
	}
	job, err := m.SubmitUpload("json", upload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(job.SourceName, "..") || strings.Contains(job.SourceName, "/") {
		t.Fatalf("unsafe source name %q", job.SourceName)
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(upload.Path)) || bytes.Contains(encoded, []byte(`"path"`)) {
		t.Fatalf("job leaked staging path: %s", encoded)
	}
	got := waitForJob(t, m, job.ID)
	if got.State != StateSucceeded || got.OK != 1 {
		t.Fatalf("job=%+v", got)
	}
	if _, err := os.Stat(upload.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file was not deleted: %v", err)
	}
}

func TestServerPathSymlinkEscapeRejected(t *testing.T) {
	dataDir := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.json")
	if err := os.WriteFile(outsideFile, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dataDir, "link.json")); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	m := New(dataDir, cat)
	if _, err := m.Submit(CreateRequest{Format: "json", Path: "link.json"}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestUploadReservationLimitsConcurrentStaging(t *testing.T) {
	dataDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	m, err := NewWithOptions(dataDir, cat, Options{MaxConcurrentJobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	permit, err := m.ReserveUpload()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveUpload(); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected busy while upload slot reserved, got %v", err)
	}
	permit.Release()
	if permit2, err := m.ReserveUpload(); err != nil {
		t.Fatal(err)
	} else {
		permit2.Release()
	}
}

func TestCopiedPermitCannotBeUsed(t *testing.T) {
	dataDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	m, err := NewWithOptions(dataDir, cat, Options{MaxConcurrentJobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	permit, err := m.ReserveUpload()
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Release()
	copied := *permit
	if _, err := m.StageReservedUpload(bytes.NewBufferString(`{}`), "copy.json", 1024, &copied); err == nil {
		t.Fatal("copied permit was accepted")
	}
}

func TestOnlyReadyUploadCanBeSubmitted(t *testing.T) {
	dataDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	m, err := NewWithOptions(dataDir, cat, Options{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := m.StageUpload(bytes.NewBufferString(`{}`), "part.json", 1024)
	if err != nil {
		t.Fatal(err)
	}
	partPath := strings.TrimSuffix(upload.Path, ".ready") + ".part"
	if err := os.Rename(upload.Path, partPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(partPath) })
	upload.Path = partPath
	if _, err := m.SubmitUpload("json", upload); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected part rejection, got %v", err)
	}
}

func TestTerminalStateIncludesCleanupAndSlotRelease(t *testing.T) {
	dataDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	m, err := NewWithOptions(dataDir, cat, Options{MaxConcurrentJobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	body := `[{"type":"xai","access_token":"at1","refresh_token":"rt1","sub":"user-1","expired":"2026-12-01T00:00:00Z"}]`
	upload, err := m.StageUpload(bytes.NewBufferString(body), "one.json", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.SubmitUpload("json", upload)
	if err != nil {
		t.Fatal(err)
	}
	got := waitForJob(t, m, job.ID)
	if got.State != StateSucceeded {
		t.Fatalf("job=%+v", got)
	}
	if _, err := os.Stat(upload.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal job retained upload: %v", err)
	}
	permit, err := m.ReserveUpload()
	if err != nil {
		t.Fatalf("terminal job retained slot: %v", err)
	}
	permit.Release()
}

func TestCloseRejectsNewUploads(t *testing.T) {
	dataDir := t.TempDir()
	cat, err := catalog.Open(filepath.Join(dataDir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	m, err := NewWithOptions(dataDir, cat, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveUpload(); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected closed manager, got %v", err)
	}
}

func waitForJob(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == StateSucceeded || job.State == StateFailed {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for import job")
	return Job{}
}
