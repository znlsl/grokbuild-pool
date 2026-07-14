package outbound

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"

	"github.com/yshgsh1343/grokbuild2api/internal/protocol/upstream"
)

// Factory 按代理（及可选账号）缓存 upstream.Client，避免每次请求新建连接池。
//
// 防封号 P1：同一账号尽量复用同一出站客户端；key 可为
//   - proxyURL（Client）
//   - accountID + "\x00" + proxyURL（ClientFor，账号-代理亲和）
//
// lastProxyByAccount 记录账号最近一次使用的 ProxyURL，便于观测/失效。
// SOCKS5/SOCKS5h 使用 golang.org/x/net/proxy Dialer（http.ProxyURL 对 socks 无效）。
type Factory struct {
	Base upstream.Config

	mu                 sync.Mutex
	cache              map[string]*upstream.Client // key: proxy 或 account\x00proxy
	lastProxyByAccount map[string]string           // accountID → 最近 ProxyURL（空=直连）
}

// NewFactory 创建出站工厂。
func NewFactory(base upstream.Config) *Factory {
	return &Factory{
		Base:               base,
		cache:              make(map[string]*upstream.Client),
		lastProxyByAccount: make(map[string]string),
	}
}

// ApplyBase 热更新上游默认配置（base_url 等）并清空客户端缓存，
// 使后续请求按新 base 重建连接池。
func (f *Factory) ApplyBase(base upstream.Config) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Base = base
	f.cache = make(map[string]*upstream.Client)
}

// UpdateBaseURL 仅改 base_url 并清空缓存；保留其余 Base 字段。
func (f *Factory) UpdateBaseURL(baseURL string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Base.BaseURL = baseURL
	f.cache = make(map[string]*upstream.Client)
}

func cacheKey(accountID, proxyURL string) string {
	proxyURL = strings.TrimSpace(proxyURL)
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return proxyURL
	}
	return accountID + "\x00" + proxyURL
}

// Client 返回指定代理（空=直连，不走环境代理）的 upstream 客户端。
func (f *Factory) Client(proxyURL string) (*upstream.Client, error) {
	return f.ClientFor("", proxyURL)
}

// ClientFor 按账号+代理亲和返回客户端。
func (f *Factory) ClientFor(accountID, proxyURL string) (*upstream.Client, error) {
	if f == nil {
		return nil, fmt.Errorf("outbound: nil factory")
	}
	accountID = strings.TrimSpace(accountID)
	proxyURL = strings.TrimSpace(proxyURL)
	key := cacheKey(accountID, proxyURL)

	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.cache[key]; ok {
		if accountID != "" {
			f.lastProxyByAccount[accountID] = proxyURL
		}
		return c, nil
	}
	c, err := f.buildClient(proxyURL)
	if err != nil {
		return nil, err
	}
	f.cache[key] = c
	if accountID != "" {
		f.lastProxyByAccount[accountID] = proxyURL
	}
	return c, nil
}

// LastProxy 返回账号最近一次 ClientFor 使用的 ProxyURL（空串=直连或未知）。
func (f *Factory) LastProxy(accountID string) (proxyURL string, ok bool) {
	if f == nil {
		return "", false
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.lastProxyByAccount[accountID]
	return p, ok
}

// Forget 丢弃与 proxyURL 相关的全部缓存条目。
func (f *Factory) Forget(proxyURL string) {
	if f == nil {
		return
	}
	proxyURL = strings.TrimSpace(proxyURL)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.cache, proxyURL)
	suffix := "\x00" + proxyURL
	for k := range f.cache {
		if k == proxyURL || strings.HasSuffix(k, suffix) {
			delete(f.cache, k)
		}
	}
	for aid, p := range f.lastProxyByAccount {
		if p == proxyURL {
			delete(f.lastProxyByAccount, aid)
		}
	}
}

// ForgetAccount 丢弃某账号的亲和缓存与 lastProxy 记录。
func (f *Factory) ForgetAccount(accountID string) {
	if f == nil {
		return
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := accountID + "\x00"
	for k := range f.cache {
		if strings.HasPrefix(k, prefix) {
			delete(f.cache, k)
		}
	}
	delete(f.lastProxyByAccount, accountID)
}

// Len 返回当前缓存客户端数量。
func (f *Factory) Len() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cache)
}

func (f *Factory) buildClient(proxyURL string) (*upstream.Client, error) {
	tr := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if proxyURL == "" {
		// 直连：不使用环境 HTTP_PROXY
		tr.Proxy = nil
	} else {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("outbound: bad proxy url: %w", err)
		}
		scheme := strings.ToLower(u.Scheme)
		switch scheme {
		case "http", "https":
			tr.Proxy = http.ProxyURL(u)
		case "socks5", "socks5h":
			// http.ProxyURL 不支持 socks；必须 Dial 走 SOCKS
			dialer, err := xproxy.FromURL(u, xproxy.Direct)
			if err != nil {
				return nil, fmt.Errorf("outbound: socks dialer: %w", err)
			}
			if cd, ok := dialer.(xproxy.ContextDialer); ok {
				tr.DialContext = cd.DialContext
			} else {
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
			}
			tr.Proxy = nil
		default:
			return nil, fmt.Errorf("outbound: unsupported proxy scheme %q", u.Scheme)
		}
	}
	cfg := f.Base
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tr.ResponseHeaderTimeout = timeout
	cfg.HTTPClient = &http.Client{Timeout: 0, Transport: tr}
	return upstream.NewClient(cfg), nil
}
