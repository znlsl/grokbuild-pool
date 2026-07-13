package synth

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

func TestGenerateCountAndExpirySpread(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	accts, err := Generate(Options{Count: 1000, Seed: 7, Now: now, Spread: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 1000 {
		t.Fatalf("count=%d", len(accts))
	}
	minE, maxE := accts[0].ExpiresAt, accts[0].ExpiresAt
	prios := map[int]int{}
	for _, a := range accts {
		if a.AccessToken == "" || a.RefreshToken == "" {
			t.Fatal("empty token")
		}
		if a.ExpiresAt < minE {
			minE = a.ExpiresAt
		}
		if a.ExpiresAt > maxE {
			maxE = a.ExpiresAt
		}
		prios[a.Priority]++
	}
	// Should span a meaningful portion of 48h for 1000 samples.
	if maxE-minE < int64((24 * time.Hour).Seconds()) {
		t.Fatalf("expires spread too small: %d sec", maxE-minE)
	}
	if len(prios) < 2 {
		t.Fatalf("expected multi priority, got %v", prios)
	}
}

func TestNDJSONRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	accts, err := Generate(Options{Count: 50, Seed: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteNDJSON(&buf, accts); err != nil {
		t.Fatal(err)
	}
	var got []string
	n, err := ReadNDJSONStream(&buf, func(a catalog.Account) error {
		got = append(got, a.ID)
		if a.AccessToken == "" {
			t.Fatal("token lost")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 50 || len(got) != 50 {
		t.Fatalf("got n=%d len=%d", n, len(got))
	}
}

func TestNDJSONLineLengthBoundary(t *testing.T) {
	line := []byte(`{"id":"one","access_token":"a","refresh_token":"r","expires_at":2000000000}`)
	n, err := ReadNDJSONStreamLimit(bytes.NewReader(line), len(line), 1, func(catalog.Account) error { return nil })
	if err != nil || n != 1 {
		t.Fatalf("exact boundary rejected: n=%d err=%v", n, err)
	}
	tooLong := append(append([]byte{}, line...), ' ')
	if _, err := ReadNDJSONStreamLimit(bytes.NewReader(tooLong), len(line), 1, func(catalog.Account) error { return nil }); err == nil {
		t.Fatal("expected overlong NDJSON line rejection")
	}
}

func TestWriteNDJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.ndjson")
	n, err := WriteNDJSONFile(path, Options{Count: 25, Seed: 99, Now: time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if n != 25 {
		t.Fatalf("n=%d", n)
	}
}
