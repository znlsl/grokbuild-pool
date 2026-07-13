// Package refresh 提供后台 OAuth 预刷新 worker，以及请求路径上的 EnsureFresh
//（singleflight + 全局 QPS 限流）。
//
// 模块 M08（Scheme B）。网络调用经 OAuthClient：
//   - 桩：DisabledOAuth（默认安全路径）
//   - 真实脚手架：HTTPRefreshClient / XaiOAuth（可配置 token URL）
//
// 真实 HTTP 刷新默认不启用：须 POOL_OAUTH_ENABLED=1 且 STATUS UNLOCK_M12=true。
// 单测用 httptest mock token endpoint，勿对公网发真实请求。
package refresh
