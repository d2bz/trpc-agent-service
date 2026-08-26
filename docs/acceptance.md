# 验收矩阵

> 状态说明：`planned` 表示设计已覆盖但代码未完成，`partial` 表示已有部分实现或验证，`done` 表示代码、测试、文档和演示证据全部齐备。

| ID | 验收要求 | 设计证据 | 代码/测试证据 | 状态 |
| --- | --- | --- | --- | --- |
| A01 | 多租户模型与配置 | [数据模型](data-model.md#31-tenant)、[总体架构](architecture.md#4-控制面) | 已实现 Admin API、内存 Repository、不可变 Revision、发布/路由和隔离测试；认证/PostgreSQL 待实现 | partial |
| A02 | Gateway、Worker、Channel、Storage、Admin、Telemetry 节点协作 | [架构图](architecture.md#2-系统架构图) | 待实现多角色启动和 Compose | planned |
| A03 | 多节点水平扩展与 Session 路由 | [数据面](architecture.md#5-数据面)、[节点部署](architecture.md#6-节点部署) | 待实现双 Worker 集成测试 | planned |
| A04 | 无 sticky session 与共享 Session/Memory | [设计结论](architecture.md#1-设计结论)、[并发](storage-and-consistency.md#4-同一-session-并发) | 待实现跨 Worker 连续会话测试 | planned |
| A05 | 配置、数据、工具、日志、密钥隔离 | [设计原则](project-foundation.md#9-不可破坏的设计原则)、[治理](solution.md#56-治理与安全) | 已实现配置仓库与 Runtime 缓存租户隔离；数据、工具、日志、密钥待实现 | partial |
| A06 | 多租户选择多种后端 | [统一路由](storage-and-consistency.md#1-统一后端路由)、[数据放置](storage-and-consistency.md#2-数据放置) | 待实现 InMemory/Redis/PostgreSQL Bundle | planned |
| A07 | Session/Memory/Summary/Artifact/Knowledge/Audit 存储 | [核心实体](data-model.md#3-核心实体)、[数据放置](storage-and-consistency.md#2-数据放置) | 待实现 Repository 与 Adapter 测试 | planned |
| A08 | 同 Session 多节点并发一致性 | [并发方案](storage-and-consistency.md#4-同一-session-并发)、[并发时序](sequence.md#3-同一-session-同时收到两条消息) | 待实现租约、队列、CAS 测试 | planned |
| A09 | Event/State/Summary 更新顺序 | [更新顺序](storage-and-consistency.md#5-eventstatesummary-和-memory-顺序)、[时序规则](sequence.md#2-关键顺序规则) | 待实现乱序/旧 Summary 测试 | planned |
| A10 | Memory 跨节点可见性 | [Memory 顺序](storage-and-consistency.md#53-memory) | 待实现双 Worker 可见性测试 | planned |
| A11 | Redis 到 SQL 迁移 | [Session 迁移](storage-and-consistency.md#71-sessionredis-到-sql) | 待实现迁移 Job 与校验测试 | planned |
| A12 | 本地到远端向量库迁移 | [向量迁移](storage-and-consistency.md#72-向量库迁移) | 待实现索引重建与切换测试 | planned |
| A13 | IM 重复投递幂等 | [IM 幂等](storage-and-consistency.md#6-im-消息幂等)、[故障时序](sequence.md#4-worker-故障与重试) | 待实现三次投递单 Run 测试 | planned |
| A14 | 至少两类 IM，包含微信体系 | [IM 差异](solution.md#55-im-接入差异) | 待实现企业微信和 Telegram Adapter/E2E | planned |
| A15 | IM 到 Runner 与 Event 到回复转换 | [完整时序](sequence.md#1-企业微信完整链路) | 待实现 InboundEnvelope/Outbox 测试 | planned |
| A16 | Webhook、验签、去重、身份映射 | [身份模型](data-model.md#33-channel-binding-与身份映射)、[IM 差异](solution.md#55-im-接入差异) | 待实现验签向量与身份测试 | planned |
| A17 | 群聊/单聊 Session 规则 | [Session 命名](architecture.md#54-session-命名)、[群聊策略](data-model.md#5-群聊策略) | 待实现键生成与隔离测试 | planned |
| A18 | IM 长度、限频、异步、媒体、失败重试 | [IM 差异](solution.md#55-im-接入差异)、[Outbox](storage-and-consistency.md#62-出站) | 待实现分片、429、媒体和重试测试 | planned |
| A19 | Plugin/Guardrail/Callback 租户治理 | [治理](solution.md#56-治理与安全) | 待实现白名单、预算、审批、脱敏测试 | planned |
| A20 | 指标与租户成本 | [可观测性](solution.md#57-可观测性)、[容量估算](solution.md#6-容量估算方法) | 待实现 OTel Metric 与成本聚合测试 | planned |
| A21 | 全链路 Trace | [完整时序](sequence.md#1-企业微信完整链路) | 待实现 trace 传播集成测试 | planned |
| A22 | 审计字段完整 | [Audit 模型](data-model.md#36-inboxrunoutbox-与-audit) | 待实现 schema/字段完整性测试 | planned |
| A23 | 密钥管理和脱敏 | [控制面配置](architecture.md#42-配置传播)、[治理](solution.md#56-治理与安全) | 待实现 Secret Resolver 和泄漏测试 | planned |
| A24 | 节点/IM/数据库/模型/Tool 故障恢复 | [故障恢复](solution.md#58-故障恢复)、[故障降级](storage-and-consistency.md#8-故障与降级) | 待实现故障注入测试 | planned |
| A25 | Context、goroutine、Event Channel 排空 | [并发与故障边界](architecture.md#7-并发与故障边界) | 待实现 goleak/取消/排空测试 | planned |
| A26 | 灰度与租户级回滚 | [发布模型](architecture.md#41-agent-发布模型) | 已实现 HTTP 发布、默认版本切换、固定版本解析和旧版本回滚；权重灰度待实现 | partial |
| A27 | 容量评估 | [容量估算](solution.md#6-容量估算方法) | 待用压测数据替换示例值 | planned |
| A28 | 最小与生产部署方案 | [节点部署](architecture.md#6-节点部署) | 待实现 Compose/Kubernetes 验证 | planned |
| D01 | 2000-4000 字架构方案 | [方案总稿](solution.md) | 1.0-rc1 中文正文 2403 字，待 8/27 正式提交 | partial |
| D02 | 系统架构图 | [架构图](architecture.md#2-系统架构图) | Mermaid CLI 11.12.0 渲染与视觉检查通过 | partial |
| D03 | 核心时序图 | [时序图](sequence.md#1-企业微信完整链路) | 两张 Mermaid 时序图由 CLI 11.12.0 渲染与视觉检查通过 | partial |
| D04 | 数据模型 | [数据模型](data-model.md) | ER 图由 Mermaid CLI 11.12.0 渲染通过；迁移文件待实现 | partial |
| D05 | 同步和幂等策略 | [存储与一致性](storage-and-consistency.md) | 待实现集成测试 | partial |
| D06 | 多后端适配方案 | [后端路由与取舍](storage-and-consistency.md#1-统一后端路由) | 待实现参考 Adapter | partial |
| D07 | 至少 8 个风险与缓解 | [风险清单](solution.md#9-主要风险) | 12 项已记录，待最终复核 | partial |
| D08 | GitHub 实现代码 | 当前仓库 | 已有最小可运行链路，完整平台功能待实现 | partial |
| F01 | 明确上游复用与平台新增 | [能力基线](project-foundation.md#6-上游能力基线与平台新增职责) | 已固定上游依赖并验证 LLMAgent、Runner、Session、OpenAI Server；平台模块待继续实现 | partial |

## 阶段性实现证据

| ID | 能力 | 代码证据 | 自动化证据 | 状态 |
| --- | --- | --- | --- | --- |
| I01 | 最小 Agent Runtime | `trpcservice/agent/agent.go`：真实 `LLMAgent`、`Runner`、InMemory Session、确定性模型 | `go test ./trpcservice/agent`：多轮 Session 与幂等关闭 | partial |
| I02 | OpenAI-compatible HTTP 服务 | `trpcservice/web/web.go`、`cmd/trpc-service/main.go`：健康检查、非流式/SSE、优雅退出 | `go test ./trpcservice/web`：健康、普通对话、SSE `[DONE]` | partial |
| I03 | 可构建和启动 | `build.sh`、`start.sh`、`stop.sh`：构建、就绪检查、PID 生命周期 | `./build.sh` 后通过 `/healthz` 和 `/v1/chat/completions` 手工验证 | partial |
| I04 | 多租户配置与发布 | `trpcservice/tenant/tenant.go`、`repository.go`：Tenant、App、Revision、摘要、发布、固定版本和回滚 | `go test ./trpcservice/tenant`：深拷贝不可变、租户隔离、版本路由、错误路径 | partial |
| I05 | Runtime 解析与缓存 | `trpcservice/agent/resolver.go`：三元组缓存、singleflight、身份复核、生命周期租约 | `go test -race ./trpcservice/agent`：并发单次构建、取消隔离、关闭等待、跨租户缓存隔离 | partial |
| I06 | Admin API 与动态对话 | `trpcservice/web/admin.go`、`platform.go`、`cmd/trpc-service/main.go`：控制面 HTTP、显式租户路由、动态协议适配和共享 Session | `go test -race ./trpcservice/web`：创建/发布/对话/固定版本/回滚、跨租户、SSE、CORS | partial |

## 验收使用方式

每完成一个功能，必须同时补充代码路径、测试命令和演示步骤，再把状态改为 `done`。只有设计文档的条目保持 `planned` 或 `partial`，不能因为“已设计”而宣称功能完成。
