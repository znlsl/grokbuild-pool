// Package outbound 提供按账号 ProxyURL 构造出站 HTTP 客户端（防封号 P0/P1）。
//
// P0：Client(proxyURL) 按代理缓存 Transport，空串=直连且忽略环境代理。
// P1：ClientFor(accountID, proxyURL) 以 accountID+proxyURL 为亲和缓存键，
// 并维护 lastProxyByAccount；Forget / ForgetAccount 用于代理变更或账号下线时失效。
//
// 与 sticky 协同：lease.Acquire 在粘性命中时固定 AccountID，并带上该账号的
// ProxyURL；executor.UpstreamFor 使用 lease.ProxyURL 出站 → 会话粘性 = 代理粘性。
// 详见 phases/ANTIBAN_P1.md。
package outbound
