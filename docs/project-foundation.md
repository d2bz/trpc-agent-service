# 参赛项目背景与实现基础信息

> 本文是参赛实现的工程基线，用于记录对题目的理解、技术选择和实施顺序，不修改或替代仓库 README 中的原始题目与验收标准。两者出现冲突时，以原始题目为准。
>
> 基线日期：2026-08-21

## 1. 项目基本信息

| 项目项 | 内容 |
| --- | --- |
| 项目名称 | `trpc-agent-service` |
| 项目性质 | tRPC-Agent-Go 开源项目实战题的参赛实现 |
| 项目定位 | 基于 tRPC-Agent-Go 的多租户、节点化 Agent 部署与运行平台 |
| 题目源仓库 | <https://github.com/liuzengh/trpc-agent-service> |
| 参赛实现仓库 | <https://github.com/d2bz/trpc-agent-service> |
| 上游框架 | <https://github.com/trpc-group/trpc-agent-go> |
| 上游模块 | `trpc.group/trpc-go/trpc-agent-go` |
| 初始上游基线 | `v1.11.2` |
| 开发语言 | Go 1.21 或更高版本 |
| 当前阶段 | Phase 2 起步：Tenant/Agent App/Revision 与 Runtime 路由 |
| 许可证 | 题目仓库当前未提供；提交或发布前按赛事与仓库要求确认 |

上游依赖采用明确版本，不直接跟随 `main` 分支。版本升级必须经过编译、单元测试、集成测试和核心消息链路回归测试。

## 2. 项目背景

tRPC-Agent-Go 已提供 Agent 编排、Runner、Tool/MCP、Session、Memory、Knowledge、Artifact、Plugin/Guardrail、服务协议和 OpenTelemetry 等运行时能力。这些能力可以支持单个 Agent 应用，但企业级落地还需要解决平台层问题：

- 多个部门或客户共用平台时，配置、身份、数据、工具、密钥和成本需要按租户隔离。
- Agent Worker 需要水平扩展，任意节点都应能够继续同一会话，不能依赖单机内存状态。
- 企业微信、微信客服等 IM 平台存在验签、重复投递、乱序、回调超时、限流和媒体消息等差异。
- Session、Memory、向量数据和 Artifact 的一致性、生命周期及迁移方式不同，不能用单一存储策略处理。
- 模型、工具和外部系统构成跨组件调用链，需要统一治理、审计、成本核算与故障恢复。

本项目负责把 tRPC-Agent-Go 的框架能力组织成可管理、可扩展、可观测和可运维的服务平台，而不是重新实现 Agent 框架本身。

## 3. 参赛实现目标

本参赛实现以覆盖全部功能要求为目标，并按依赖关系分步完成以下能力：

1. 多租户 Agent 的创建、配置、发布、路由、运行和回滚。
2. Gateway、Worker、Channel Adapter、Storage Adapter、Admin API 和 Telemetry 等节点化组件。
3. 无状态 Worker 的水平扩展，以及跨节点 Session 并发控制和共享状态。
4. Session、Memory、Summary、Knowledge、Artifact 与 Audit Log 的多后端选择和迁移。
5. 至少两类 IM Channel，其中至少包含微信或企业微信体系的一种通道。
6. 工具权限、Guardrail、预算、敏感信息、密钥和审计治理。
7. 贯穿消息接入、Runner、模型、工具、存储和消息回复的指标与 Trace。
8. 超时、重试、降级、节点故障、配置回滚、容量评估和生产部署。

分阶段只表示我们的实现顺序，不改变原题的范围和验收口径。每个阶段结束时，仓库都应保持可构建、可测试，并对已实现链路提供可复现的演示。

## 4. 目标用户与角色

| 角色 | 主要职责 |
| --- | --- |
| 平台管理员 | 管理租户、平台资源、后端能力、全局策略和平台版本 |
| 租户管理员 | 管理本租户 Agent、模型、通道、知识库、工具权限和预算 |
| Agent 开发者 | 开发 Agent、Tool、MCP、Skill 和工作流，并发布版本 |
| 终端用户 | 通过 IM 或 HTTP 客户端与已发布的 Agent 交互 |
| 运维与安全人员 | 监控容量和故障，审计访问与工具决策，处理密钥及合规策略 |

同一自然人可以拥有多个角色，但所有管理操作和运行请求都必须解析出明确的租户作用域。

## 5. 系统边界

### 5.1 控制面

控制面负责低频、强治理的管理操作，包括：

- Tenant、Agent App 和不可变 Agent Revision 管理。
- 模型、Channel Binding、数据后端、知识库和工具策略配置。
- 密钥引用、预算、限流、审计策略和配置版本管理。
- 发布、灰度、回滚以及节点和后端健康状态查询。

### 5.2 数据面

数据面负责高频运行流量，包括：

- HTTP/IM 消息接入、验签、鉴权、去重和身份映射。
- 根据 `tenant_id`、`agent_id` 和 `session_id` 定位运行配置。
- 调用 tRPC-Agent-Go Runner，消费流式 Event 并执行 Tool/MCP。
- 读写 Session、Memory、Summary、Knowledge 和 Artifact。
- 生成 IM/HTTP 回复，并记录指标、Trace、成本和审计日志。

### 5.3 不重复建设的能力

Agent 基础运行时、内置编排、通用 Session/Memory 接口、模型适配和协议实现优先复用 tRPC-Agent-Go。只有当上游扩展点无法满足多租户隔离、平台路由或一致性要求时，才在本项目增加适配层；不复制并私有化维护上游实现。

## 6. 上游能力基线与平台新增职责

以下包路径以 tRPC-Agent-Go `v1.11.2` 为准。

| 领域 | 可直接复用的上游能力 | 本项目新增职责 |
| --- | --- | --- |
| Agent 编排 | `agent/llmagent`、`agent/graphagent`、`agent/chainagent`、`agent/parallelagent`、`agent/cycleagent` | 租户级注册、版本、发布、灰度和路由 |
| 执行 | `runner`、流式 Event、`context.Context` 取消 | Worker 调度、并发配额、会话串行化和运行生命周期 |
| Session | `session/inmemory`、`session/redis`、`session/mysql`、`session/postgres`、`session/sqlite`、`session/mongodb` 等 | 租户级后端选择、键空间隔离、并发写和迁移 |
| Memory | `memory/inmemory`、`memory/redis`、`memory/mysql`、`memory/postgres`、`memory/pgvector` 等 | 跨节点可见性、租户路由、保留策略和迁移 |
| Knowledge | `knowledge` 及 `knowledge/vectorstore/*` | 知识库权限、索引任务、版本和租户隔离 |
| Artifact | `artifact/inmemory`、`artifact/s3`、`artifact/cos` | 配额、对象命名、生命周期、病毒检查和访问控制 |
| Tool/Skill | `tool`、MCP、`skill` | 白名单、租户密钥注入、审批和沙箱策略 |
| 治理 | `plugin`、`plugin/guardrail`、Callbacks | 租户策略编排、预算、身份检查和审计决策 |
| 服务协议 | `server/openai`、`server/agui`、`server/a2a`、`server/trpcagent` | 统一 Gateway、Admin API、租户鉴权和路由 |
| IM 参考 | `openclaw` Gateway、Channel 和 delivery 模型 | 企业微信/微信通道、账号绑定、验签、去重和回复适配 |
| 可观测性 | `telemetry` 和 OpenTelemetry | 统一资源属性、租户指标、成本统计、告警和审计关联 |

## 7. 初始技术基线

| 领域 | 基线选择 | 说明 |
| --- | --- | --- |
| Go | 1.21+ | 与上游 `v1.11.2` 的 `go.mod` 保持一致 |
| Agent 框架 | tRPC-Agent-Go `v1.11.2` | 初始版本固定，按兼容性测试升级 |
| 服务形态 | 控制面与数据面逻辑分离 | 早期可同进程部署，接口和所有权边界保持独立 |
| Worker 状态 | 默认无状态 | 不要求 sticky session，运行状态写入共享后端 |
| 关系数据 | PostgreSQL | 作为租户、配置、版本、绑定和审计元数据的参考实现 |
| 热状态与协调 | Redis | 用于 Session 参考后端、幂等键、限流和分布式协调 |
| 向量数据 | PGVector | 参考实现复用 PostgreSQL 运维体系，并保留 Qdrant/Milvus 适配与迁移能力 |
| 对象数据 | S3-compatible storage | 存储 Artifact 和外置的多模态大对象 |
| 可观测性 | OpenTelemetry | Trace、Metric 和 Log 使用统一资源与关联标识 |
| 本地开发 | InMemory/SQLite + 可选 Docker Compose | 降低启动成本，但不得作为生产默认后端 |

这些选择定义参考实现，不限制租户使用题目要求覆盖的其他后端。所有后端都必须通过显式能力声明参与路由，不能在不支持某项语义时静默降级。

## 8. 核心术语

| 术语 | 定义 |
| --- | --- |
| Tenant | 平台中配置、身份、数据、工具、密钥、预算和审计的最高隔离单元 |
| Agent App | 租户拥有的逻辑 Agent 应用，具有稳定标识和多个发布版本 |
| Agent Revision | Agent App 的不可变配置快照，包含模型、Prompt、Tool、Skill、Knowledge 和策略引用 |
| Channel Binding | 外部 IM 账号/应用与 Tenant、Agent App 之间的绑定关系 |
| External Principal | IM 或 API 来源的外部用户/群身份，经映射后形成租户内用户身份 |
| Session | 由应用、租户用户和会话标识共同限定的多轮交互上下文 |
| Event | 用户消息、Agent 输出、Tool 调用结果等按顺序写入 Session 的事实记录 |
| Memory | 可跨 Session 检索的长期用户或业务记忆 |
| Summary | 从 Session Event 派生的上下文压缩结果，必须记录覆盖范围和版本 |
| Knowledge Base | 由租户管理、供 Agent 检索的文档与索引集合 |
| Artifact | 图片、音频、文件等独立存储的大对象及其元数据 |
| Run | 一次从输入消息到最终结果或终止状态的 Agent 执行实例 |
| Node | 可独立部署和扩缩容的 Gateway、Worker、Channel 或后台任务进程 |

## 9. 不可破坏的设计原则

1. `tenant_id` 是强制上下文，缺失或解析失败时请求必须拒绝，不能回退到公共租户。
2. 所有业务主键、缓存键、对象路径、指标和审计记录都必须包含或可确定映射到租户作用域。
3. Agent Revision 发布后不可原地修改；变更生成新版本，以支持灰度和回滚。
4. Worker 不依赖本地会话状态；节点重启或扩缩容不能破坏已持久化会话。
5. 同一 Session 默认只允许一个活动 Run；并发必须通过锁、版本号或有序队列显式处理。
6. IM 入站以平台事件 ID 建立幂等记录，出站发送使用稳定幂等键并可安全重试。
7. 密钥只保存引用或密文，不能出现在配置快照、日志、Trace、错误信息和审计明文中。
8. `trace_id` 和 `request_id` 必须贯穿入站、Runner、模型、Tool、存储及出站回复。
9. 所有 goroutine 都必须有所有者、退出条件和 `context.Context`；Runner Event 通道必须排空或安全取消。
10. 后端一致性、TTL、分页、事务和迁移能力必须显式声明，不以最低能力做无提示兼容。

## 10. 分阶段实施基线

| 阶段 | 主要产出 | 阶段完成条件 |
| --- | --- | --- |
| Phase 0 | 项目基础、架构契约、术语、ADR 和验收矩阵 | 范围无冲突，关键决策有记录 |
| Phase 1 | 单节点最小执行链路 | HTTP 请求可真实调用 Runner，支持流式响应、取消和持久化 |
| Phase 2 | 多租户控制面与隔离 | 可创建、发布和路由多个租户 Agent，隔离测试通过 |
| Phase 3 | 多节点与多后端 | 无状态 Worker、共享 Session、并发控制、幂等和迁移链路可验证 |
| Phase 4 | IM Channel | 企业微信/微信体系通道及第二通道完成端到端测试 |
| Phase 5 | 治理与可观测性 | 权限、Guardrail、预算、审计、指标和 Trace 完整串联 |
| Phase 6 | 故障恢复与生产部署 | 降级、回滚、容量报告、Docker Compose 和 Kubernetes 验证完成 |
| Phase 7 | 全量验收 | 所有功能、文档、测试、演示和风险项闭环 |

后续任务计划应引用阶段和验收项。Phase 7 之前允许存在尚未实现的功能，但不允许删除、模糊化或标记为永久可选。

## 11. 完成定义

一个功能只有同时满足以下条件才视为完成：

- 存在可运行的真实实现，而非伪代码、空接口或仅有设计说明。
- 关键逻辑有单元测试，跨组件链路有集成或端到端测试。
- 文档描述与当前行为一致，配置和运行步骤可以复现。
- 暴露必要的日志、指标和 Trace，并能定位失败阶段。
- 明确租户隔离、权限、密钥、审计和失败处理行为。
- 进入持续集成检查，后续变更能够发现回归。

## 12. 待确认的项目级决策

以下事项需要结合题目规则、上游兼容性和参赛实现成本，通过 ADR 确认：

1. 项目开源许可证。若希望与上游保持一致，可评估 Apache License 2.0。
2. 生产密钥管理基线，例如 Kubernetes Secret、Vault 或云 KMS。
3. Admin API 的身份提供方和租户管理员授权模型。

## 13. 参考资料

- [tRPC-Agent-Go 仓库](https://github.com/trpc-group/trpc-agent-go)
- [tRPC-Agent-Go v1.11.2](https://github.com/trpc-group/trpc-agent-go/releases/tag/v1.11.2)
- [tRPC-Agent-Go 文档](https://trpc-group.github.io/trpc-agent-go/)
- [本项目题目与验收要求](../README.md)

## 14. 2026-08-24 实现进度

- Phase 0 的背景、架构契约、数据模型、时序和验收矩阵已经建立。
- Phase 1 已有可运行的 HTTP → OpenAI-compatible Server → LLMAgent → Runner → InMemory Session 最小链路；持久化后端和取消端到端证据仍待补充。
- Phase 2 已实现 Tenant、Agent App、不可变 Revision 的内存参考仓库，以及发布、默认路由、固定版本和回滚语义。
- Runtime Resolver 已实现 `(tenant, app, revision)` 缓存、并发单次构建、租户身份二次校验和关闭租约；TTL/LRU 淘汰和配置变更通知仍待实现。
