package httpserver

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/anthropic"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/openai"
)

type fixedHot struct {
	n int
}

func (f fixedHot) HotLen() int { return f.n }
func (f fixedHot) PoolStats() (int, int) {
	return f.n, 0
}

func TestHealthzReadyz(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxConcurrent = 2
	h := New(Options{
		Config:  cfg,
		Hot:     fixedHot{n: 3},
		Version: "test",
		Metrics: &Metrics{},
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz status=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("readyz status=%d body=%s", rr.Code, rr.Body.String())
	}

	hEmpty := New(Options{Config: cfg, Hot: fixedHot{n: 0}, Metrics: &Metrics{}})
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr = httptest.NewRecorder()
	hEmpty.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz empty want 503 got %d", rr.Code)
	}
}

func TestConcurrencyLimit503(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxConcurrent = 2
	cfg.APIKey = "" // auth off

	var entered atomic.Int32
	var release chan struct{}
	release = make(chan struct{})

	// Slow API handler under /v1/
	apiSlow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered.Add(1)
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	metrics := &Metrics{}
	mw := &Middleware{
		MaxConcurrent: 2,
		Metrics:       metrics,
	}
	// Build mux like production: concurrency only on /v1/
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	protected := Chain(apiSlow, mw.LimitConcurrency)
	mux.Handle("/v1/", protected)
	h := mw.Observe(mux)

	var wg sync.WaitGroup
	results := make(chan int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			results <- rr.Code
		}()
	}

	// Wait until 2 handlers hold the slots.
	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if entered.Load() < 2 {
		close(release)
		t.Fatalf("expected 2 entered, got %d", entered.Load())
	}
	// Give rejected requests time to finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)

	var okN, rejectN int
	for code := range results {
		switch code {
		case http.StatusOK:
			okN++
		case http.StatusServiceUnavailable:
			rejectN++
		default:
			t.Errorf("unexpected status %d", code)
		}
	}
	if okN != 2 {
		t.Fatalf("ok=%d want 2", okN)
	}
	if rejectN != 2 {
		t.Fatalf("reject=%d want 2", rejectN)
	}
	if metrics.rejects.Load() < 2 {
		t.Fatalf("metrics rejects=%d", metrics.rejects.Load())
	}

	// Healthz must still work under pressure pattern (no concurrency limit).
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz under limit path: %d", rr.Code)
	}
}

func TestConcurrencyLimitHotUpdate(t *testing.T) {
	mw := &Middleware{MaxConcurrent: 1, Metrics: &Metrics{}}
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	h := mw.LimitConcurrency(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	request := func(results chan<- int) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
		results <- rr.Code
	}
	waitEntered := func() {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("handler did not enter")
		}
	}

	results := make(chan int, 4)
	go request(results)
	waitEntered()
	mw.SetMaxConcurrent(2)
	go request(results)
	waitEntered()

	mw.SetMaxConcurrent(1)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("lowered limit: got %d want 503", rr.Code)
	}

	close(release)
	for i := 0; i < 2; i++ {
		select {
		case code := <-results:
			if code != http.StatusOK {
				t.Fatalf("in-flight request status=%d", code)
			}
		case <-time.After(time.Second):
			t.Fatal("in-flight request hung after limit update")
		}
	}
	if got := mw.Inflight(); got != 0 {
		t.Fatalf("inflight=%d want 0", got)
	}

	mw.SetMaxConcurrent(0)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("unlimited status=%d", rr.Code)
	}
}

func TestNewWiresRuntimeBodyLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxBodyBytes = 4
	oai := &openai.Handlers{}
	anth := &anthropic.Handlers{}
	var mw *Middleware
	_ = New(Options{
		Config:    cfg,
		OpenAI:    oai,
		Anthropic: anth,
		OnMiddleware: func(got *Middleware) {
			mw = got
		},
	})
	if mw == nil || oai.MaxBodyFunc == nil || anth.MaxBodyFunc == nil {
		t.Fatal("runtime body limit callbacks were not wired")
	}
	if got := oai.MaxBodyFunc(); got != 4 {
		t.Fatalf("openai limit=%d", got)
	}
	mw.SetMaxBody(8)
	if got := oai.MaxBodyFunc(); got != 8 {
		t.Fatalf("updated openai limit=%d", got)
	}
	if got := anth.MaxBodyFunc(); got != 8 {
		t.Fatalf("updated anthropic limit=%d", got)
	}
}

func TestMaxBodyHotUpdate(t *testing.T) {
	mw := &Middleware{MaxBody: 4}
	h := mw.LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("12345")))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("small limit status=%d", rr.Code)
	}

	mw.SetMaxBody(8)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("12345")))
	if rr.Code != http.StatusOK {
		t.Fatalf("updated limit status=%d", rr.Code)
	}
}

func TestRequestTimeoutHotUpdate(t *testing.T) {
	mw := &Middleware{RequestTimeout: time.Second}
	h := mw.Timeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Deadline(); ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("initial timeout status=%d", rr.Code)
	}

	mw.SetRequestTimeout(0)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("disabled timeout status=%d", rr.Code)
	}
}

func TestAPIKeyAuth(t *testing.T) {
	cfg := config.Default()
	cfg.APIKey = "secret-key"
	muxAPI := http.NewServeMux()
	muxAPI.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mw := &Middleware{APIKey: cfg.APIKey, MaxConcurrent: 10}
	h := Chain(muxAPI, mw.RequireClient)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMetricsContainsPoolSeries(t *testing.T) {
	m := &Metrics{}
	m.SetPoolGauges(12, 3)
	m.IncReject()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)
	body := rr.Body.String()
	for _, want := range []string{"pool_hot_size", "proxy_reject_total", "proxy_inflight", "process_resident_memory_bytes"} {
		if !contains(body, want) {
			t.Fatalf("metrics missing %q\n%s", want, body)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
