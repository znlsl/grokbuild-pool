> [!WARNING]
> ## 免责声明
>
> - 本项目仅供学习、研究与个人自用，不提供任何形式的商业服务或可用性保证。
> - 使用者必须确保所使用的账号、凭据及接口访问权限来源合法，并遵守相关服务条款及所在地法律法规。
> - 禁止将本项目用于账号盗用、绕过访问限制、批量滥用、未授权服务转售及其他违法违规用途。
> - 项目不会收集或提供任何上游账号。账号封禁、额度损失、数据泄露、服务中断等风险由使用者自行承担。
> - 上游接口可能随时发生变化，本项目不保证长期兼容性、稳定性或数据完整性。
> - 公网部署前请务必启用强密钥、HTTPS、访问控制和必要的网络隔离。
> - 项目为 **Grok4.5** 编写的 **vibe coding** 玩具，**GPT5.6 sol** 和作者本人进行 **review**。但难免有疏漏之处。同时，本项目因作者日后忙碌 **疏于维护**，需要的可 **自行 Fork 二开**，issue 和 PR 可能无法及时回应。机器人提的pr直接关闭。
> - **下载、部署或使用本项目，即代表你已阅读并同意自行承担相关风险。**

# grokbuild-pool

将 Grok Build 转换为 **OpenAI / Anthropic 兼容 API**，并提供面向大规模账号池的调度、令牌管理、额度限制、出站代理池和 Web 管理后台。

## 功能
- OpenAI Chat Completions / OpenAI Responses / Anthropic Messages 支持
- 多账号池自动调度（会话粘性主/次选、模型级冷却、可用性优先默认）
- 两种部署形态：
  - **单机 SQLite**：`pool-proxy` 一进程
  - **Postgres + Redis 多进程**：Gateway / Worker / ControlPlane / Refresher
- SQLite 冷库与内存热池（默认路径）
- 会话粘性（primary + secondary）与 Power-of-Two / stable_rr 选号
- SOCKS5 / HTTP 出站代理池（账号粘 = 出口粘，可选 `require_proxy`）
- API 令牌、额度、RPM 和并发限制
- React 管理后台（仪表盘 / 账号 / Token / 选号模式 / 代理池 / 导入 / 设置）
- Docker 一键部署，默认服务端口：`8080`

## 与其他项目的区别

不同项目的定位不同，不存在绝对的优劣关系，请根据实际需求选择。

| 对比项              | grokbuild-pool                       | CLIProxyAPI                                  | grok2api                             |
| ---------------- | ------------------------------------ | -------------------------------------------- | ------------------------------------ |
| 主要定位             | 面向 Grok Build 的大规模账号池代理              | 面向多种 AI CLI 订阅的统一 API 代理                     | 面向 Grok Build 与 Grok Web 的完整网关       |
| 上游范围             | 专注 Grok Build                         | Claude Code、OpenAI Codex、Gemini、Grok Build 等 | Grok Build、Grok Web                  |
| 多供应商支持           | ✅                                    | ✅                                            | ✅                                    |
| 图片、图片编辑与视频       | ❌                                    | 主要面向文本及多模态输入                                 | ✅                                    |
| 多账号调度            | ✅                                    | ✅                                            | ✅                                    |
| 调度特点             | SQLite 冷库、内存热池、主/次粘性、模型级冷却、Power-of-Two / stable_rr；可选 Postgres/Redis 多进程骨架 | 多 Provider、多账号轮询与负载均衡                        | 优先级、额度门控、会话粘性、冷却与故障切换                |
| 大规模账号池           | **核心设计目标**                           | 支持，但更侧重多 Provider 统一接入                       | 支持，更侧重完整功能与管理体验                      |
| 客户端令牌管理          | ✅                                    | ✅                                            | ✅                                    |
| 令牌额度与并发限制        | ✅                                    | ❌                                            | ✅                                    |
| 内置管理后台           | ✅ React SPA（轻量，Hash 路由）              | ✅提供 Management API，也可搭配第三方 Dashboard         | ✅ 完整 React 管理后台                      |
| HTTP / SOCKS 代理池 | ✅      | ✅                                            | ✅                                    |
| 数据库              | SQLite（默认）/ PostgreSQL（Scheme 2，未验证） |                                              | SQLite / PostgreSQL                  |
| Redis 支持         |可选     | ❌                                            | ✅                                    |
| 更适合              | **只使用 Grok Build**，重视账号池规模、调度性能、防封出口和轻量部署 | **个人使用，希望统一接入多个 AI CLI / OAuth Provider**    | **需要 Grok Build、Grok Web、媒体生成和完整后台** |

如果你：主要使用 Grok Build 且追求轻量化部署，需要管理较大规模的账号池，需要分发功能与 SOCKS/HTTP 出口绑定，不需要 Grok Web 图片或视频能力，本项目会比较适合。

## 快速开始

### 1. 克隆项目

```bash
git clone https://github.com/yshgsh1343/grokbuild-pool.git
cd grokbuild-pool
```

### 2. 启动服务

生成管理密钥并启动：

```bash
export ADMIN_KEY="$(openssl rand -hex 24)"
docker compose -f docker-compose.sqlite.yml up -d --build
```

### 3. 打开管理台并导入账号

首次启动时账号池为空，可在「导入」页上传 JSON / NDJSON / SSO 数据；也可在开启服务端路径导入后浏览服务器目录提交任务。

数据保存在 Docker Volume `pool-data` 中（库、`settings.json`、`proxy_pool.json`、导入暂存等）。

## 两种部署方式

| 方式 | Compose 文件 | 组件 | 存储 | 适合规模 | 状态 |
| --- | --- | --- | --- | --- | --- |
| **A. 单机 SQLite** | `docker-compose.sqlite.yml` | 仅 `pool-proxy` | SQLite + 进程内热池/粘性 |
| **B. Postgres + Redis** | `docker-compose.postgres-redis.yml` | `gateway` + `worker` + `controlplane` + `refresher` | Postgres 冷库 + Redis 跨进程状态 |

### 总流程（一次请求）

```text
HTTP 请求
  → lease.Acquire(stickyKey?, model?, exclude?)
      → selector.PickExcluding(now, stickyKey, exclude)
          ① sticky primary 命中且仍 Eligible → 直接返回
          ② 否则 sticky secondary 命中且 Eligible → 升主并返回
          ③ 否则 stable_rr / pow2Pick：从热池候选打分
          ④ 若 stickyKey 非空，put 新 primary（旧主可降为 secondary）
      → 模型冷却检查（同号其它模型仍可租）
      → catalog 按 accountID 取 token/proxy（密钥仅租约期）
      → 无 proxy 时可从代理池自动绑定并落盘
      → hot.Inflight++
  → executor 反代 cli-chat-proxy（默认）
  → lease.Release
      成功：Inflight--
      失败：模型/账号冷却、failureScore、可能隔离、ClearSticky、exclude 后再 Acquire
```

## 鸣谢
- Linux.do：新的理想型社区 https://linux.do/
- CLIProxyAPI：转换接口逻辑参照 https://github.com/router-for-me/CLIProxyAPI
- Grok2api：前端设计与 React 管理台栈借鉴 https://github.com/chenyme/grok2api

## 许可证
MIT
