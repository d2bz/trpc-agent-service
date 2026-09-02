# 验收矩阵

> 状态说明：`planned` 表示设计已覆盖但代码未完成，`partial` 表示已有部分实现或验证，`done` 表示代码、测试、文档和演示证据全部齐备。

| ID | 验收要求 | 设计证据 | 代码/测试证据 | 状态 |
| --- | --- | --- | --- | --- |
| A01 | 多租户模型与配置 | [数据模型](data-model.md#31-tenant)、[总体架构](architecture.md#4-控制面) | 已实现 Admin API、不可变 Revision、发布/路由和隔离测试；Repository 有内存与 PostgreSQL 两套实现，由 I10 的进程存储 profile 选择；对话面已有静态 API Key 认证，Admin 认证待实现 | partial |
| A02 | Gateway、Worker、Channel、Storage、Admin、Telemetry 节点协作 | [架构图](architecture.md#2-系统架构图) | 待实现多角色启动和 Compose | planned |
| A03 | 多节点水平扩展与 Session 路由 | [数据面](architecture.md#5-数据面)、[节点部署](architecture.md#6-节点部署) | 已有双 Worker 集成测试：两个独立构建的 Worker 共享一个 PostgreSQL schema 和一个 Redis，经 HTTP 验证同 Session 互斥、异 Session 放行、TTL 接管（见 I11）。多角色节点启动、Compose/K8s 部署与压测仍未实现 | partial |
| A04 | 无 sticky session 与共享 Session/Memory | [设计结论](architecture.md#1-设计结论)、[并发](storage-and-consistency.md#4-同一-session-并发) | 已有跨 Worker 连续会话测试：第一个 Worker 结束后，第二个 Worker 接着同一段会话继续对话并读到完整历史，不需要 sticky session（见 I11）。Memory/Summary 尚未实现，本项只覆盖 Session | partial |
| A05 | 配置、数据、工具、日志、密钥隔离 | [设计原则](project-foundation.md#9-不可破坏的设计原则)、[治理](solution.md#56-治理与安全) | 已实现配置仓库与 Runtime 缓存租户隔离；对话面租户与 Session 归属由凭据推导，静态 API Key 只保留 SHA-256 摘要；Runtime 支持仅 `env:VAR_NAME` 形式的模型 SecretRef 且错误不回显 Secret，并删除三个由进程环境派生的上游请求头（见 A23）；`env:VAR_NAME` 的租户授权、工具、日志脱敏和生产 Secret Manager 待实现 | partial |
| A06 | 多租户选择多种后端 | [统一路由](storage-and-consistency.md#1-统一后端路由)、[数据放置](storage-and-consistency.md#2-数据放置) | 待实现 InMemory/Redis/PostgreSQL Bundle | planned |
| A07 | Session/Memory/Summary/Artifact/Knowledge/Audit 存储 | [核心实体](data-model.md#3-核心实体)、[数据放置](storage-and-consistency.md#2-数据放置) | 待实现 Repository 与 Adapter 测试 | planned |
| A08 | 同 Session 多节点并发一致性 | [并发方案](storage-and-consistency.md#4-同一-session-并发)、[并发时序](sequence.md#3-同一-session-同时收到两条消息) | 已实现并验证 Run 租约（合作型入口互斥、TTL 接管、失去租约取消 Run，见 I11）。**没有**实现有界队列，也**没有**实现 fencing/CAS 写入准入——上游 `AppendEvent` 无 fence/CAS 入口，过期 Worker 的写入不会被原子拒绝 | partial |
| A09 | Event/State/Summary 更新顺序 | [更新顺序](storage-and-consistency.md#5-eventstatesummary-和-memory-顺序)、[时序规则](sequence.md#2-关键顺序规则) | 待实现乱序/旧 Summary 测试 | planned |
| A10 | Memory 跨节点可见性 | [Memory 顺序](storage-and-consistency.md#53-memory) | 待实现双 Worker 可见性测试 | planned |
| A11 | Redis 到 SQL 迁移 | [Session 迁移](storage-and-consistency.md#71-sessionredis-到-sql) | 待实现迁移 Job 与校验测试 | planned |
| A12 | 本地到远端向量库迁移 | [向量迁移](storage-and-consistency.md#72-向量库迁移) | 待实现索引重建与切换测试 | planned |
| A13 | IM 重复投递幂等 | [IM 幂等](storage-and-consistency.md#6-im-消息幂等)、[故障时序](sequence.md#4-worker-故障与重试) | 待实现三次投递单 Run 测试 | planned |
| A14 | 至少两类 IM，包含微信体系 | [IM 差异](solution.md#55-im-接入差异) | 待实现企业微信和飞书 Adapter/E2E | planned |
| A15 | IM 到 Runner 与 Event 到回复转换 | [完整时序](sequence.md#1-企业微信完整链路) | 待实现 InboundEnvelope/Outbox 测试 | planned |
| A16 | Webhook、验签、去重、身份映射 | [身份模型](data-model.md#33-channel-binding-与身份映射)、[IM 差异](solution.md#55-im-接入差异) | 待实现验签向量与身份测试 | planned |
| A17 | 群聊/单聊 Session 规则 | [Session 命名](architecture.md#54-session-命名)、[群聊策略](data-model.md#5-群聊策略) | 待实现键生成与隔离测试 | planned |
| A18 | IM 长度、限频、异步、媒体、失败重试 | [IM 差异](solution.md#55-im-接入差异)、[Outbox](storage-and-consistency.md#62-出站) | 待实现分片、429、媒体和重试测试 | planned |
| A19 | Plugin/Guardrail/Callback 租户治理 | [治理](solution.md#56-治理与安全) | 待实现白名单、预算、审批、脱敏测试 | planned |
| A20 | 指标与租户成本 | [可观测性](solution.md#57-可观测性)、[容量估算](solution.md#6-容量估算方法) | 待实现 OTel Metric 与成本聚合测试 | planned |
| A21 | 全链路 Trace | [完整时序](sequence.md#1-企业微信完整链路) | 待实现 trace 传播集成测试 | planned |
| A22 | 审计字段完整 | [Audit 模型](data-model.md#36-inboxrunoutbox-与-audit) | 待实现 schema/字段完整性测试 | planned |
| A23 | 密钥管理和脱敏 | [控制面配置](architecture.md#42-配置传播)、[治理](solution.md#56-治理与安全) | 已实现模型 `SecretRef` 的 `env:VAR_NAME` 解析、缺失拒绝和错误不泄漏测试；调用上游前删除进程环境派生的 `Authorization`(`OPENAI_API_KEY`，仅无 `SecretRef` 时)、`OpenAI-Organization`(`OPENAI_ORG_ID`) 和 `OpenAI-Project`(`OPENAI_PROJECT_ID`) 三个请求头，并有本地端点测试证明其缺席。这只是单个客户端的请求头抑制，**不是**进程级 Secret 隔离；`env:VAR_NAME` 尚未做租户授权和白名单（见 [Admin API 已知限制](admin-api.md#6-已知限制)）；生产 Secret Manager、轮转、审计和全链路日志脱敏仍未实现 | partial |
| A24 | 节点/IM/数据库/模型/Tool 故障恢复 | [故障恢复](solution.md#58-故障恢复)、[故障降级](storage-and-consistency.md#8-故障与降级) | 待实现故障注入测试 | planned |
| A25 | Context、goroutine、Event Channel 排空 | [并发与故障边界](architecture.md#7-并发与故障边界) | 待实现 goleak/取消/排空测试 | planned |
| A26 | 灰度与租户级回滚 | [发布模型](architecture.md#41-agent-发布模型) | 已实现 HTTP 发布、默认版本切换、旧版本回滚，以及 Session Revision Pin：发布和回滚都不会改变已开始的会话；`postgres` profile 下 Pin 与控制面同库，重启和多进程都能读到同一个 Pin（见 I10），权重灰度待实现 | partial |
| A27 | 容量评估 | [容量估算](solution.md#6-容量估算方法) | 待用压测数据替换示例值 | planned |
| A28 | 最小与生产部署方案 | [节点部署](architecture.md#6-节点部署) | 待实现 Compose/Kubernetes 验证 | planned |
| D01 | 2000-4000 字架构方案 | [正式提交方案](submission-2026-08-27.md) | 1.0 中文正文 3288 字，已完成提交前检查 | partial |
| D02 | 系统架构图 | [正式架构图](submission-2026-08-27.md#3-总体架构) | Mermaid CLI 11.12.0 渲染与视觉检查通过 | partial |
| D03 | 核心时序图 | [正式时序图](submission-2026-08-27.md#4-核心消息链路) | Mermaid CLI 11.12.0 渲染与视觉检查通过 | partial |
| D04 | 数据模型 | [数据模型](data-model.md) | ER 图由 Mermaid CLI 11.12.0 渲染通过；迁移文件待实现 | partial |
| D05 | 同步和幂等策略 | [存储与一致性](storage-and-consistency.md) | 待实现集成测试 | partial |
| D06 | 多后端适配方案 | [后端路由与取舍](storage-and-consistency.md#1-统一后端路由) | 待实现参考 Adapter | partial |
| D07 | 至少 8 个风险与缓解 | [风险清单](submission-2026-08-27.md#8-主要风险与应对) | 12 项已记录并完成复核 | partial |
| D08 | GitHub 实现代码 | 当前仓库 | 已有最小可运行链路，完整平台功能待实现 | partial |
| F01 | 明确上游复用与平台新增 | [能力基线](project-foundation.md#6-上游能力基线与平台新增职责) | 已固定上游依赖并验证 LLMAgent、Runner、Session、OpenAI Server，以及 `model/openai` 的 OpenAI-compatible 模型构造；平台模块待继续实现 | partial |

## 阶段性实现证据

| ID | 能力 | 代码证据 | 自动化证据 | 状态 |
| --- | --- | --- | --- | --- |
| I01 | 最小 Agent Runtime | `trpcservice/agent/agent.go`、`model.go`：真实 `LLMAgent`、`Runner`、InMemory Session、确定性模型和 OpenAI-compatible 模型工厂，并由 Runtime 自有唯一 OpenAI Adapter | `go test -race ./trpcservice/agent`：多轮 Session、确定性路径、OpenAI-compatible 本地端点、SecretRef、生成参数、环境派生请求头在空和显式 SecretRef 两条路径上均缺席（并有一个反向对照测试证明同样的环境本会产生这三个头）、Adapter→Runner→自有 Session 关闭顺序、并发幂等关闭与错误保留、共享 Session 不被关闭 | partial |
| I02 | OpenAI-compatible HTTP 服务 | `trpcservice/agent/agent.go`、`trpcservice/agent/model.go`、`trpcservice/web/platform.go`、`cmd/trpc-service/main.go`：健康检查、非流式/SSE、优雅退出与超时后强制 `Close`；Runtime Revision 可选择 OpenAI-compatible 端点 | `go test -race ./trpcservice/web ./cmd/trpc-service ./trpcservice/agent`：健康、普通对话、SSE `[DONE]`、SSE 期间 Runtime 租约保持、Shutdown 失败后强制关闭、本地 OpenAI-compatible SSE 请求和配置参数断言 | partial |
| I03 | 可构建和启动 | `build.sh`、`start.sh`、`stop.sh`：构建、就绪检查、PID 生命周期 | `./build.sh` 后通过 `/healthz` 和 `/v1/chat/completions` 手工验证 | partial |
| I04 | 多租户配置与发布 | `trpcservice/tenant/tenant.go`、`repository.go`：Tenant、App、Revision、摘要、发布、固定版本和回滚 | `go test ./trpcservice/tenant`：深拷贝不可变、租户隔离、版本路由、错误路径 | partial |
| I05 | Runtime 解析与缓存 | `trpcservice/agent/resolver.go`：三元组缓存、singleflight、入缓存前的完整性与身份复核、生命周期租约，Resolver 是 Runtime 及其 Adapter 的唯一所有者 | `go test -race ./trpcservice/agent`：并发单次构建、取消隔离、关闭等待、跨租户缓存隔离、半成品/身份不符 Runtime 被关闭且不入缓存、关闭时释放缓存内资源 | partial |
| I06 | Admin API 与动态对话 | `trpcservice/web/admin.go`、`platform.go`、`cmd/trpc-service/main.go`：控制面 HTTP、凭据驱动的对话路由、请求期内路由到 Runtime 自有协议适配器和共享 Session | `go test -race ./trpcservice/web`：创建/发布/对话/回滚、跨租户、SSE、CORS、SSE 全程持有 Runtime 租约、Resolver 关闭后返回 `runtime_unavailable` | partial |
| I07 | 可信 Chat 身份 | `trpcservice/identity/`：Identity/Authenticator、静态 API Key 只存 SHA-256 摘要、`RunContext` 经不可导出 context key 传递；`trpcservice/agent/contextrunner.go`：协议层 `userID`/`sessionID` 被丢弃，作用域与 Runtime 不符则 fail closed | `go test -race ./trpcservice/identity ./trpcservice/agent ./trpcservice/web`：摘要映射与拷贝隔离、未知凭据 fail closed、无 RunContext 拒绝执行、包装器 `Close` 不关闭真实 Runner、认证矩阵 401/403、请求体 `user` 无法进入 Session 键空间 | partial |
| I08 | Session Revision Pin | `trpcservice/sessiondir/`：`{tenant, app, principal, session, epoch}` 键、单锁 `EnsurePin` 首写即线性化点；`trpcservice/web/platform.go`：查 Pin → 解析候选 → EnsurePin → 落败方释放 Runtime 租约后改用胜出版本 | `go test -race ./trpcservice/sessiondir ./trpcservice/web`：32 并发候选唯一胜出、租户/主体隔离、发布与回滚不改旧 Pin、相同 hint 放行且不同 hint `409`、屏障化并发首轮两侧同版本且 `resolver.Close` 能完成（证明落败的 Runtime 租约已释放） | partial |
| I09 | 持久化 Session 后端 Spike | `trpcservice/sessionbackend/sessionbackend.go`：`New(Config)` 构造 InMemory/PostgreSQL/Redis 三种 `session.Service`，校验先于会 panic 的上游选项、拒绝会静默回退到 localhost 的空 DSN、对自产错误脱敏连接串口令；`deploy/docker-compose.session.yml` 提供仅监听 `127.0.0.1` 的本地依赖。脱敏逻辑现已导出为 `Scrub(error, string) error`，供 I10 的连接池与迁移错误路径复用。工厂本身不选择后端，进程用哪一个由 I10 决定 | `go test -race ./trpcservice/sessionbackend`（默认不触网）；`TRPC_SERVICE_SESSION_INTEGRATION=1` 下对真实 PostgreSQL 16/Redis 7 验证往返、`GetSession` 缺失返回 `(nil,nil)`、`CreateSession` 二次调用的后端分歧、Delete 后从读路径消失、`Close` 幂等、Redis key 前缀隔离；`Scrub` 的公开测试覆盖 URL userinfo、percent-encoded 拼写、libpq 引号形式、**连接串本身解析失败**（密码含未编码 `/` 时 authority 提前截断，pgx 把 `/` 之前那段当端口原样引用出来的那条路径，已用真实 pgx 输出固定；含单字符片段的按位置脱敏与端口笔误不被误伤两条断言）以及脱敏后不保留 unwrap 链，命令见 [Session 后端 Spike](session-backend.md#7-集成测试的运行方式) | partial |
| I10 | 进程存储 Profile | `cmd/trpc-service/storage.go`、`main.go`：`TRPC_SERVICE_STORAGE_PROFILE` 一次决定三者去向——`inmemory`（默认，零依赖、不触网）或 `postgres`（`tenant/postgres` Repository + `sessiondir/postgres` Directory 共享一个调用方持有的 pgxpool，上游 `sessionbackend` PostgreSQL Session 服务自持另一个池，同一 DSN 与 schema）。整份配置先校验后建资源；顺序为校验监听地址 → 校验存储配置 → 建池并即刻登记关闭 → 有界 `Ping` → 两族 `Migrate` → 构造组件 → `SeedDemo`；关闭严格逆序并用 `errors.Join` 合并关闭错误。DSN 从不写日志，相关错误全部经 `sessionbackend.Scrub`。`SeedDemo` 改为仅在应用尚无默认 Revision 时发布，重启不再回退已发布版本 | `go test -race ./cmd/trpc-service ./trpcservice/config`（默认不触网）：默认与显式 `inmemory` 均忽略环境里遗留的 DSN/schema、未知 profile（`redis`/`Postgres`/`pg`/带空格）被拒并列出合法值、`postgres` 缺失或空白 DSN 在任何构造器之前失败、非法与超长 schema 同样前置失败、连接串解析错误不泄漏明文或 percent-encoded 口令、启动中途失败按逆序关闭且关闭错误不被吞掉、监听地址拒绝仍先于存储；`TRPC_SERVICE_SESSION_INTEGRATION=1` 下对真实 PostgreSQL 走完整 profile 构造路径：两族迁移表与上游 6 张 Session 表齐备、Pin 与真实会话历史跨"重启"存活且 Pin 指向的 Revision 仍可解析、重启后的 `SeedDemo` 不把已发布的 `echo-v2` 改回 `echo-v1`，命令见 [进程存储 Profile](session-backend.md#8-进程存储-profile) | partial |
| I11 | Session Run Lease | `trpcservice/sessionlease/`：`Coordinator`/`Lease`/`Holder` 接口与所有后端共用的续约循环——续约的"未知"结果一律按失败处理并重试到 `expires - SafetyMargin`，本进程先于其他 Worker 被允许接管的时刻停止认领；Run 结束、协调器关闭都只停止续约、**把锁留给 TTL** 而不删除，以覆盖上游 Runner 取消后经 `context.WithoutCancel` 继续写约一秒终态 Event 的尾巴；`redis/redis.go` 用三段 Lua 原子完成获取/续约/释放，key 只含版本化定长 SHA-256 摘要（无租户/主体/Session ID 进入 keyspace），释放从不删 fence；获取的确认迟到时**不交付租约**——`Lifetime.HandOut` 在后端回答之后再量一次实际耗时，超出 `TTL - SafetyMargin - RenewInterval` 就按 `ErrUnavailable` 拒绝并把锁 owner-matched 归还（协调器已关闭、调用方 Context 已结束同样如此，三者返回不同错误），因为这道检查在共享层，任何后端都绕不开、也不依赖客户端是否遵守 deadline；`cmd/trpc-service/storage.go`：`TRPC_SERVICE_SESSION_COORDINATION` 独立于存储 profile，`redis` + `inmemory` 存储被**启动拒绝**（共享锁配不共享 Session 是假安全），未知取值同样拒绝而不回退，租约客户端由本进程建且显式设置 `ContextTimeoutEnabled`（否则本包设的 deadline 到不了 socket），关闭严格逆序、协调器先于它借用的客户端；`trpcservice/web/platform.go`：解析 Pin 与 Runtime **之前**取租约（被拒请求不建 Pin、不进 Runtime 缓存），`409 session_busy` + `Retry-After: 2` 与 `503 coordination_unavailable`（不带 `Retry-After`）分开，只有干净跑完才 `Release`，释放失败不改动已写出的响应，fence 不进响应头或响应体 | `go test -race ./trpcservice/sessionlease/... ./trpcservice/web ./cmd/trpc-service`（默认不触网）：共享一致性套件覆盖两个实现（单赢家、TTL 接管、陈旧释放不打扰新持有者、关闭不删锁、fail closed、未确认续约丢租约；内存实现没有"后端不可达"这一失败模式，相关子测试 `t.Skip` 而非伪造通过）、摘要不可伪造与字段隔离、HTTP 层 409/503/释放规则/不发布 fence；迟到确认的三条分支是确定性单测（把 `acquiredAt` 往前推等价于回复迟到，不 sleep、不触网）：旧 `acquiredAt` 被拒且锁回到 store、调用方已取消的迟到成功不交付且锁仍回到 store、预算内的确认照常交付；租约客户端的 `ContextTimeoutEnabled` 只读回工厂建出的 options，`NewClient` 惰性不发包；`TRPC_SERVICE_SESSION_INTEGRATION=1` 下对真实 Redis 验证契约与 key 布局，并用 `cmd/trpc-service/dualworker_test.go` 跑两个独立构建的 Worker（各自存储栈、Resolver、协调器、HTTP server）共享一个 PostgreSQL schema 与一个 Redis：同 Session 被拒、异 Session 放行、跨 Worker 续聊读到完整历史、停止续约后按 TTL 接管且 fence 前进，并经 `openStorage` 验证 `coordination=redis` 真的接到默认 key 前缀。命令见 [Session Run Lease](session-lease.md#63-集成测试的运行方式) | partial |
| I12 | Revision 驱动的真实模型 | `trpcservice/agent/model.go`：`deterministic` / `openai-compatible` 模型工厂、显式 `base_url`、`env:` SecretRef、生成参数和 ambient header 抑制；`agent.go` 在不可变 Revision 构建 Runtime 时消费这些配置 | `go test -race ./trpcservice/agent` 默认只访问本地 `httptest` 端点并断言 SSE、目标地址、显式凭据、温度和 token 上限；设置 `TRPC_SERVICE_MODEL_INTEGRATION=1` 后可按 [README](../README.md#运行真实模型) 对真实端点执行受门控 smoke test | partial |

## 已知限制

这些限制是当前切片有意接受的，不能在验收中被当作已解决：

- **合法凭据可以制造无界 Session。** Session 目录与 Session Service 都没有配额、TTL 或 LRU，一个有效 key 可以用无限多的 `X-Session-ID` 无限增长。默认 profile 下耗的是进程内存；`postgres` profile 下耗的是磁盘，且上游默认软删、不回收，只是把失败从 OOM 换成了库涨满。
- **首轮 OpenAI 历史可以伪造。** 平台只决定会话归属，不校验请求体 `messages`；新 Session 的第一轮可以注入编造的"历史对话"。
- **Adapter 拒绝的请求也会建立 Pin。** Session 与 Revision 在调用上游 Adapter 之前确定，格式错误的首轮同样会钉住该 Session。
- **默认 profile 的 Pin 只在单进程内存中**，多节点部署会各自 Pin 到各自看到的默认版本。`postgres` profile 下 Pin 落库，这一条才不成立。
- **`postgres` profile 只关掉了一个已知会坏的组合。** 它不允许"只持久化 Session"：控制面、Pin、会话历史绑在同一个 DSN 与 schema 上，[Session 后端 Spike](session-backend.md#61-持久-session-与进程内-pin-的重启不变量破裂) 记录的"Session 还在、Pin 丢了、旧会话被静默换版本"因此无法被配置出来。它本身不解决并发；并发由下一条的 Run 租约在入口处理。仍然没有的：租户级路由、Redis 存储 profile。
- **Run 租约是合作型的，不是写入准入。** 租约把第二个 Worker 挡在 Run 入口（`409 session_busy`），持有者失效后按 TTL 接管，失去租约会取消 Run Context。它**不**阻止已经在写的 Worker 继续写：`Lease.Fence()` 只是观测句柄，**不参与 Session 写入准入**，因此**不能**宣称过期 writer 被原子拒绝，也不能把这套机制叫做 enforcement fencing。上游 `session.Service.AppendEvent` 没有 fence/CAS 参数，`WithAppendEventHook` 与后端写入之间不是原子的，所以这不是"还没做"，是当前上游接口做不出来。具体地说仍然会发生：被暂停或分区的持有者在 TTL 内恢复后照样写；Runner 在 Context 取消后仍通过 `context.WithoutCancel` 写约一秒终态 Event。取消是尽力而为且最终一致的。没有等待队列，也没有 `MaxRunDuration`——一直健康续约的 Worker 可以无限持有租约。详见 [Session Run Lease](session-lease.md#4-这把租约不保证什么准确表述)。
- **单实例 Redis 是唯一验证过的协调部署。** failover 下锁 key 可能随未同步的副本一起丢失、fence 计数器可能回退，因此**不宣称** failover 下仍然互斥或 fence 仍然单调。fence key 永不回收，一个历史 Session 一个小整数。
- **`SeedDemo` 的"不覆盖已发布版本"存在一个已记录的窗口。** 它先读应用当前默认 Revision、再决定是否发布，两步之间不是原子的；Repository 没有"仅在未发布时发布"的原语。并发启动是安全的（各自要么不发布，要么发布同一个 `echo-v1`），但操作员恰好在这两步之间发布新版本时，该次发布可能被启动流程覆盖。
- **对话面认证不等于平台生产安全。** Admin API 仍完全未认证，进程只能绑定本机地址。

## 验收使用方式

每完成一个功能，必须同时补充代码路径、测试命令和演示步骤，再把状态改为 `done`。只有设计文档的条目保持 `planned` 或 `partial`，不能因为“已设计”而宣称功能完成。
