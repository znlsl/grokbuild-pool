// Package catalog 实现 Scheme B 的 SQLite WAL 冷存储，用于账号凭证（模块 M03）。
// 令牌仅保存在 Account 行中；ListEligible 返回不含密钥的 HotMeta 供热索引使用。
package catalog
