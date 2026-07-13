package mockup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TokenSet 为 mock 刷新返回的 OAuth 令牌三元组。
// 镜像 catalog.TokenSet 但不导入 catalog（保持 mockup 叶级）。
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// ErrRefreshRejected 在令牌配置为失败时由 MockOAuth 返回。
var ErrRefreshRejected = errors.New("mockup: refresh rejected")

// MockOAuth 为 OAuth 令牌刷新的测试替身。
// 统计类网络 Refresh 调用，并可注入延迟/失败。
type MockOAuth struct {
	// Calls 为 Refresh 调用次数（成功或失败）。
	Calls atomic.Int64

	// Latency 为每次 Refresh 的可选人工延迟（0 = 无）。
	Latency time.Duration

	// TTL 为新 access token 有效时长（默认 3600s）。
	TTL time.Duration

	// FailTokens 将 refresh_token 映射为要返回的 error（无条目 = 成功）。
	// 通过 mu 并发安全。
	FailTokens map[string]error

	// FailAll 非 nil 时每次 Refresh 均返回该错误。
	FailAll error

	// OnRefresh 在计数增加后的可选钩子（测试）。
	OnRefresh func(refreshToken string)

	mu      sync.Mutex
	seq     atomic.Int64
	seen    map[string]int // refreshToken → call count
	lastSet map[string]TokenSet
}

// NewMockOAuth 按默认构造 MockOAuth。
func NewMockOAuth() *MockOAuth {
	return &MockOAuth{
		TTL:        time.Hour,
		FailTokens: make(map[string]error),
		seen:       make(map[string]int),
		lastSet:    make(map[string]TokenSet),
	}
}

// Refresh 实现 refresh.OAuthClient。
func (m *MockOAuth) Refresh(ctx context.Context, refreshToken string) (TokenSet, error) {
	if refreshToken == "" {
		return TokenSet{}, fmt.Errorf("%w: empty refresh token", ErrRefreshRejected)
	}
	if err := ctx.Err(); err != nil {
		return TokenSet{}, err
	}

	m.Calls.Add(1)
	m.mu.Lock()
	m.seen[refreshToken]++
	m.mu.Unlock()

	if m.OnRefresh != nil {
		m.OnRefresh(refreshToken)
	}

	if m.Latency > 0 {
		select {
		case <-ctx.Done():
			return TokenSet{}, ctx.Err()
		case <-time.After(m.Latency):
		}
	}

	if m.FailAll != nil {
		return TokenSet{}, m.FailAll
	}

	m.mu.Lock()
	failErr, hasFail := m.FailTokens[refreshToken]
	m.mu.Unlock()
	if hasFail && failErr != nil {
		return TokenSet{}, failErr
	}

	ttl := m.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	n := m.seq.Add(1)
	set := TokenSet{
		AccessToken:  fmt.Sprintf("mock-access-%d", n),
		RefreshToken: fmt.Sprintf("mock-refresh-%d", n),
		ExpiresAt:    time.Now().Add(ttl).Unix(),
	}
	m.mu.Lock()
	m.lastSet[refreshToken] = set
	m.mu.Unlock()
	return set, nil
}

// CallCount 返回 Refresh 总调用次数。
func (m *MockOAuth) CallCount() int64 {
	return m.Calls.Load()
}

// SeenCount 返回某 refresh token 被出示的次数。
func (m *MockOAuth) SeenCount(refreshToken string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[refreshToken]
}

// SetFail 将某 refresh token 标记为永久失败（返回 err）。
func (m *MockOAuth) SetFail(refreshToken string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailTokens == nil {
		m.FailTokens = make(map[string]error)
	}
	m.FailTokens[refreshToken] = err
}

// ClearFail 移除按 token 的失败注入。
func (m *MockOAuth) ClearFail(refreshToken string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.FailTokens, refreshToken)
}
