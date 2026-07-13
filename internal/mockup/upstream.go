// Package mockup 为 Scheme B 测试提供 mock HTTP 上游（无真实网络）。
package mockup

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/protocol/upstream"
)

// ResponsesServer 为 POST /v1/responses（及 /responses）的 httptest mock。
type ResponsesServer struct {
	Server *httptest.Server

	// Status 为成功处理器默认状态码（默认 200）。
	Status int
	// Body 为非流式响应的默认 JSON 体。
	Body []byte
	// StreamBody 在 stream=true 时以 text/event-stream 写出。
	StreamBody string
	// Handler 设置时覆盖默认行为。
	Handler http.HandlerFunc

	mu       sync.Mutex
	requests []CapturedRequest

	// FailN 使前 N 个请求返回 FailStatus（默认 429）。
	FailN      int32
	FailStatus int
	// failCount 为已返回的失败次数。
	failCount int32

	// FailHalfByToken 为 true 时对约一半 Authorization token
	//（fnv32(token)&1 == 1）返回 FailStatus。供 M11 G5 使用。
	FailHalfByToken bool

	// StreamChunkDelay 在写流式体前插入停顿
	//（保持 SSE 打开以测并发/RSS）。0 = 无延迟。
	StreamChunkDelay time.Duration
	// StreamRepeat 倍增 StreamBody 写出次数（默认 1）。
	StreamRepeat int

	// TotalHits 在每个请求上递增。
	TotalHits int32
	// FailHits 统计返回 FailStatus 的请求。
	FailHits int32
}

// CapturedRequest 记录 mock 所见请求。
type CapturedRequest struct {
	Method         string
	Path           string
	Auth           string
	Model          string
	ConvID         string
	Stream         bool
	Body           []byte
	AccessToken    string
	IdempotencyKey string
	// Headers 为请求头副本（测试断言 ExtraHeaders）。
	Headers http.Header
}

// NewResponsesServer 启动本地 mock 上游。调用方须 Close()。
func NewResponsesServer() *ResponsesServer {
	m := &ResponsesServer{
		Status: http.StatusOK,
		Body:   []byte(`{"id":"resp_mock","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from mock"}]}]}`),
		StreamBody: "" +
			"event: response.output_text.delta\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
			"event: response.completed\n" +
			"data: {\"type\":\"response.completed\"}\n\n",
		FailStatus: http.StatusTooManyRequests,
	}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	return m
}

// URL 返回 mock 基址（无尾斜杠），适合 upstream.Config.BaseURL + "/v1"。
func (m *ResponsesServer) URL() string {
	if m == nil || m.Server == nil {
		return ""
	}
	return strings.TrimRight(m.Server.URL, "/") + "/v1"
}

// Close 关闭服务。
func (m *ResponsesServer) Close() {
	if m != nil && m.Server != nil {
		m.Server.Close()
	}
}

// Requests 返回已捕获请求的副本。
func (m *ResponsesServer) Requests() []CapturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CapturedRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

// Hits 返回总请求数。
func (m *ResponsesServer) Hits() int {
	return int(atomic.LoadInt32(&m.TotalHits))
}

// FailHitCount 返回失败请求数（FailN 或 FailHalfByToken）。
func (m *ResponsesServer) FailHitCount() int {
	return int(atomic.LoadInt32(&m.FailHits))
}

// tokenHalfFail 在开启 FailHalfByToken 且 token 哈希到失败半区时为 true。
func tokenHalfFail(token string) bool {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	return h.Sum32()&1 == 1
}

func (m *ResponsesServer) writeFail(w http.ResponseWriter) {
	atomic.AddInt32(&m.FailHits, 1)
	st := m.FailStatus
	if st == 0 {
		st = http.StatusTooManyRequests
	}
	w.Header().Set("Content-Type", "application/json")
	if st == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(st)
	_, _ = w.Write([]byte(`{"error":{"message":"mock fail","type":"rate_limit_error"}}`))
}

func (m *ResponsesServer) serve(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&m.TotalHits, 1)
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	_ = r.Body.Close()

	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	token = strings.TrimSpace(token)

	stream := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(raw, &probe)
	if probe.Stream {
		stream = true
	}

	cap := CapturedRequest{
		Method:         r.Method,
		Path:           r.URL.Path,
		Auth:           auth,
		Model:          r.Header.Get("x-grok-model-override"),
		ConvID:         r.Header.Get("x-grok-conv-id"),
		Stream:         stream,
		Body:           append([]byte(nil), raw...),
		AccessToken:    token,
		IdempotencyKey: firstNonEmptyHeader(r.Header, "Idempotency-Key", "X-Idempotency-Key"),
		Headers:        r.Header.Clone(),
	}
	m.mu.Lock()
	m.requests = append(m.requests, cap)
	m.mu.Unlock()

	if m.Handler != nil {
		// 为自定义 handler 重新注入 body。
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		m.Handler(w, r)
		return
	}

	// G5：按 Authorization token 哈希确定性半池 429。
	if m.FailHalfByToken && token != "" && tokenHalfFail(token) {
		m.writeFail(w)
		return
	}

	failN := atomic.LoadInt32(&m.FailN)
	if failN > 0 {
		n := atomic.AddInt32(&m.failCount, 1)
		if n <= failN {
			m.writeFail(w)
			return
		}
	}

	status := m.Status
	if status == 0 {
		status = http.StatusOK
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		if d := m.StreamChunkDelay; d > 0 {
			time.Sleep(d)
		}
		body := m.StreamBody
		if body == "" {
			body = "data: {\"type\":\"response.completed\"}\n\n"
		}
		rep := m.StreamRepeat
		if rep <= 0 {
			rep = 1
		}
		for i := 0; i < rep; i++ {
			_, _ = io.WriteString(w, body)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if d := m.StreamChunkDelay; d > 0 && i+1 < rep {
				time.Sleep(d)
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := m.Body
	if len(body) == 0 {
		body = []byte(`{"id":"resp_mock","status":"completed"}`)
	}
	_, _ = w.Write(body)
}

// Client 构建指向本 mock 的 upstream.Client。
func (m *ResponsesServer) Client() *upstream.Client {
	return upstream.NewClient(upstream.Config{
		BaseURL:    m.URL(),
		HTTPClient: m.Server.Client(),
	})
}

// Poster 为始终打向 mock 服务的薄 UpstreamPoster。
type Poster struct {
	Client *upstream.Client
}

// PostResponses 实现 executor.UpstreamPoster。
func (p *Poster) PostResponses(ctx context.Context, body any, opts upstream.PostResponsesOptions) (*http.Response, error) {
	return p.Client.PostResponses(ctx, body, opts)
}

func firstNonEmptyHeader(h http.Header, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}
