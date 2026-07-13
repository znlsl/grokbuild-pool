// Package hot 提供 Scheme B 池选择用的无密钥账号元数据内存热索引（模块 M05）。
//
// HotMeta 定义于 catalog 包并在此复用——不存储任何令牌。
// 索引维护有界热集（默认容量 3000），支持 promote/demote、inflight 计数、
// 冷却与并发读写。
package hot
