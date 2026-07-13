package executor_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yshgsh1343/grokbuild2api/internal/mockup"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/anthropic"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/config"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/executor"
)

// Integration: Anthropic /v1/messages → lease Acquire → mock upstream 200 → translate.
func TestAnthropicMessages_ThroughLeaseMockUpstream(t *testing.T) {
	mgr := setupLease(t, 2)
	mock := mockup.NewResponsesServer()
	t.Cleanup(mock.Close)

	// Upstream returns Responses-shaped JSON that TranslateResponse can map.
	mock.Body = []byte(`{
		"id":"resp_int1",
		"model":"grok-4.5",
		"status":"completed",
		"output":[{"type":"message","content":[{"type":"output_text","text":"leased hello"}]}],
		"usage":{"input_tokens":3,"output_tokens":2}
	}`)

	ex := &executor.Executor{
		Leaser:   mgr,
		Upstream: &mockup.Poster{Client: mock.Client()},
	}
	h := &anthropic.Handlers{
		Cfg:  config.Default().Anthropic,
		Post: ex.Post,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-sonnet-4","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":false}`,
	))
	req.Header.Set("x-claude-code-session-id", "sess-lease-int")
	rr := httptest.NewRecorder()
	h.HandleMessages(rr, req)

	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var msg anthropic.MessageResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Content) == 0 || msg.Content[0].Text != "leased hello" {
		t.Fatalf("content=%+v", msg.Content)
	}
	if mock.Hits() != 1 {
		t.Fatalf("hits=%d want 1", mock.Hits())
	}
	if ex.AcquireCount() != 1 {
		t.Fatalf("acquires=%d", ex.AcquireCount())
	}
	reqs := mock.Requests()
	if len(reqs) != 1 || !strings.HasPrefix(reqs[0].AccessToken, "tok-") {
		t.Fatalf("expected lease token on upstream: %+v", reqs)
	}
}
