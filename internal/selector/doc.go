// Package selector 基于粘性亲和与 power-of-two-choices 最小负载打分从热索引选号（模块 M06）。
//
// 选择过程从不拷贝令牌，仅通过 hot.Index 查阅无密钥的 catalog.HotMeta。
// 粘性状态为进程内 stickyKey → accountID 的 LRU。
//
// 防封号 P1：sticky 仅绑定 accountID；代理粘性由账号自身的 HotMeta.ProxyURL
// 与 lease.Acquire 填入的 Lease.ProxyURL 保证（保持账号即保持代理）。
// 429/401/402/403 失败时 lease 会 ClearSticky* 以便切换账号（及对应代理）。
// 详见 phases/ANTIBAN_P1.md。
package selector
