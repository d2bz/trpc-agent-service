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
| 当前工作分支 | `feature/d2bz`（验收名称待组织者确认） |
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
2. 生产密钥管理基线，例如 Kubernetes Secret、Vault 或云 KMS。当前实现只做到 `env:VAR_NAME` + 租户 entitlement，不是这一项的答案。
3. Admin API 的**外部**身份提供方（JWT/OIDC）和动态 RBAC。角色模型本身已经确定并实现为 `platform_admin`/`tenant_admin` 两个封闭取值（见 §18 与[身份、权限与密钥治理](security-and-governance.md)），待确认的是凭据从哪里来、怎么轮转和撤销。

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

## 15. 2026-08-25 实现进度

- Admin REST API 已支持创建和读取 Tenant、Agent App、Agent Revision，以及发布新 Revision 和把旧 Revision 重新切为默认版本。
- `/v1/chat/completions` 已要求显式 Tenant/App 路由，可按默认或指定的已发布 Revision 获取 Runtime，并继续复用上游 OpenAI-compatible 非流式和 SSE 协议。
- 主进程已改为 `Repository → Runtime Resolver → LLMAgent/Runner` 的真实平台装配，所有 Revision 共用 App 级 Session Service；启动时只种入一个无外部依赖的 demo 配置。
- HTTP 集成测试已覆盖多租户同名资源隔离、发布、固定版本、回滚、跨版本 Session、动态 SSE、错误响应和 CORS。
- 当前控制面仍使用 InMemory Repository，Admin API 仍缺少身份认证；在 PostgreSQL 和授权模型完成前不作为生产接口。（这两条都已改变：PostgreSQL Repository 见 §17 之后的存储 profile，Admin 认证与授权见 §18。）

## 16. 2026-08-26 提交冻结

- 组织者要求最终验收分支采用 `feature/{your_name}`，但 `your_name` 的具体口径尚待确认；当前个人 Fork 暂时使用并跟踪 `feature/d2bz`，确认后可以无损重命名。
- 当前账号对题目源仓库只有读取权限，因此开发提交先推送至 `d2bz/trpc-agent-service`；若最终分支必须位于题目源仓库，需要组织者先授予写权限。
- 正式提交方案版本为 `1.0`，中文正文 3288 字，满足 2000–4000 字要求。
- 本次冻结只表示 8 月 27 日方案材料和当前可运行基线通过检查，不表示 A01-A28 已全部实现；未完成项继续以[验收矩阵](acceptance.md)为准。

## 17. 2026-08-28 可信身份与 Session Pin

- `/v1/chat/completions` 的租户、主体和可用 App 改为由 Bearer 凭据推导。上游 OpenAI Server 从请求体 `user` 和 `X-Session-ID` 取身份，因此平台在注入 Runner 的位置加了一层包装器：协议层传入的 `userID`/`sessionID` 被丢弃，实际执行使用 `u/{principal_id}` 和平台确定的 Session；请求上下文中没有可信作用域时直接失败，不猜测身份。
- 新增 `trpcservice/identity` 与 `trpcservice/sessiondir` 两个包。静态 API Key 只保存 token 的 SHA-256 摘要，长期 map 中不留明文；Session Directory 以 `{tenant, app, principal, session, epoch}` 为键，`EnsurePin` 的首次写入是线性化点。
- Session 在首轮原子钉住当时的 Revision。这反转了上一版行为：发布新版本后，已经开始的会话不再切到新版本，回滚也不改变已有 Pin。`X-Agent-Revision-ID` 退化为新会话首轮的开发用提示，对已 Pin 会话给出不同值返回 `409 pin_conflict`。
- `Key.Epoch` 已在结构中预留但恒为 0；显式 Retire/Unpin、跨进程 Pin、共享 Session 和按 Principal 的配额限流都未实现，已在[验收矩阵](acceptance.md#已知限制)中记录为已知限制。
- Admin API 依然完全未认证。对话面的认证不改变"进程只能绑定本机地址"这一边界。（**已被 §18 取代**：Admin 面现在有独立的静态凭据和角色模型；绑定本机地址这条边界保留，但理由换了。）

## 18. 2026-09-02 控制面身份与租户 Entitlement

- **两条凭据链路互不相交。** `identity.AdminIdentity`/`AdminAuthenticator` 与对话面的 `Identity`/`Authenticator` 是两组独立类型，方法名分别是 `AuthenticateAdmin` 和 `Authenticate`，因此没有任何值能同时满足两者——Chat Key 不可能被当成 Admin Key 用，反之亦然。Admin 凭据的长度下限是 32 字符（对话面是 16）：一把 chat key 只能和一个租户的一个 App 对话，一把 admin key 决定平台执行什么，两者不是同一种凭据换个标签。
- **角色是封闭集合。** `platform_admin` 不属于任何租户，是唯一能创建租户的角色；`tenant_admin` 恰好绑定一个租户，比较是精确字符串。清单里写不出第三个角色，因为一个无法识别的角色一定得被赋予某种默认含义。
- **认证排在路由之前。** Admin 请求的处理顺序是：认证 → Content-Type（POST）→ 路径前缀 → 租户作用域 → 方法 → 角色 → Repository。`/admin` 整个子树由一层包装器在 `http.ServeMux` 之前接管，否则 ServeMux 的路径清洗会把 `/admin/../admin/v1/tenants` 这类请求用一个 301 回答未认证的调用方，泄漏"这个路径存在"。`tenant_admin` 触碰别的租户得到的是和"资源不存在"逐字节相同的 404，且**不产生任何 Repository 调用**——用一个任何方法被调用就让测试失败的 Repository 断言。
- **`created_by` 来自认证后的 Principal。** 创建 Revision 的请求体里不再有这个字段，未知字段会被拒绝，所以伪造作者不再是一次请求的事。
- **Admin 面不发布任何 CORS 头**，也没有预检分支；所有 POST 必须携带 `application/json`，这同时把 Admin 写操作挡在 CORS simple request 之外。
- **Security Manifest 是严格版本化的。** `version` 必须恰好是 1，文件上限 256 KiB 且必须是普通文件，未知字段拒绝，任意层级的重复成员按大小写折叠拒绝，整个文件只允许一个 JSON 值。多把静态 key 长期只以 SHA-256 保存；无法作为 Bearer 可靠传输的 key（空、首尾有空白、含 header 非法字节）在构造期就被拒绝，而不是等到某次认证莫名失败。
- **能力按租户授权。** `allowed_secret_refs` / `allowed_policy_refs` 按租户组织：能不能引用一个能力，是"跑这个 Revision 的租户"的属性，不是"碰巧创建它的人"的属性。加载期就拒绝把持有平台自身凭据的变量或 `TRPC_SERVICE_` 命名空间授权给任何租户。Admin 创建、Admin 发布和 Runtime 构建三处共用同一个 authorizer 实例。
- **Runtime 的构建顺序本身是一条安全属性。** 发布态 → 身份/形状 → 重算并逐字节比对 `config_digest` → entitlement → Tool Registry → 模型/Secret。entitlement 先于 Secret 解析，意味着未授权的引用被拒时那个环境变量根本没有被读取。
- **`start.sh` 首次启动生成 `data/admin-api-key`（0600），重启复用，只打印路径、从不打印 key**；显式设置 `TRPC_SERVICE_ADMIN_API_KEY` 或 `TRPC_SERVICE_SECURITY_CONFIG_FILE` 时不生成。
- **明确未实现：** JWT/OIDC、动态 RBAC、清单热加载、凭据轮转/过期/撤销、持久化管理操作审计、生产 Secret Manager、预算/审批/Guardrail。`base_url` 仍不是受 entitlement 约束的能力，`config_digest` 仍是不带密钥的 SHA-256。完整边界见[身份、权限与密钥治理](security-and-governance.md)。

## 19. 2026-09-03 StorageBundle 与 Runtime 生命周期

- 新增 `trpcservice/storagebundle`：Profile 以 `(tenant_id, profile_id)` 标识，ID 即不可变版本；配置只保存 PostgreSQL DSN/Redis URL 的 `env:` 引用，不保存连接明文。内存 Source 拒绝覆盖并在输入、输出两侧深拷贝。
- Router 每次解析 Profile 并核对 fingerprint，按租户键 singleflight 懒构建 Bundle。默认 Bundle 由进程存储栈拥有、Router 只借用；默认和动态 Bundle 的租约都参与关闭等待，动态 Bundle 由 Router 逆序关闭。
- Runtime 不再用 `ownsSessionService` 布尔推测所有权，而是持有类型化 Bundle lease。安全检查和模型构造通过后才解析存储；关闭顺序为 Adapter → Runner → Bundle lease，进程顺序为 Runtime Resolver → Router → storage stack。
- BackendProfile 控制面已接入生产装配：InMemory/PostgreSQL 两种进程 profile 都提供不可变 Create/Get/List Repository，Admin 与 Router 共用同一实例；创建与发布 Revision 都检查 Profile 的租户归属、存在性和 SecretRef entitlement。Factory 可按 Profile 动态构建 InMemory/PostgreSQL/Redis Session，解析后的连接值整体按 Secret 脱敏；PostgreSQL 首次建表用 advisory lock 串行，constructor 超时后锁仍持有到实际返回。每租户 32 个 Profile 的硬上限约束常驻连接池，但 Router 仍无 TTL/LRU，Bundle 也尚未扩展到 Memory/Knowledge/Artifact。
