# 多租户节点化 Agent 平台方案

本方案覆盖多租户、节点部署、数据同步、多后端、IM、治理监控和故障恢复。交付采用完整设计与代表性实现：网页、企业微信和飞书文本通过共享 Session Run 执行真实 Agent，未实现的生产能力以设计和风险边界标明。材料与验证见[交付说明](delivery.md)和[验收说明](acceptance.md)。

## 1. 背景与目标

企业通常会为客服、研发、运营等不同业务建设多个 Agent。如果每个 Agent 独立实现 IM 接入、Session、Memory、知识库、权限、密钥、监控和部署，不仅重复建设，跨节点会话、数据隔离和合规审计也很难保持一致。

本项目基于 tRPC-Agent-Go `v1.11.2` 建设统一运行平台。业务 Agent 专注 Prompt、工作流、Tool、Skill 和 Knowledge；平台负责租户识别、发布路由、共享状态、多后端适配、IM 接入、安全治理、可观测性和故障恢复。它不是重新实现 Agent 框架，也不是只转发请求的普通网关，而是完整的 Agent 控制面和运行数据面。

最终效果是：多个租户可以在同一平台创建和发布相互隔离的 Agent，绑定企业微信或飞书，选择各自的存储后端，并由多个无状态 Worker 水平扩展运行。

## 2. 场景示例

- 租户 A 发布订单客服 Agent，绑定企业微信，使用 Redis Session、PGVector 商品知识库和订单查询 Tool。
- 租户 B 发布运维 Agent，绑定飞书，使用 PostgreSQL Session、运维知识库和服务状态 Tool。
- 两个租户即使外部用户名、群 ID 或 Session ID 相同，配置、数据、工具、密钥、日志和成本仍完全隔离。
- Worker 重启后重新加载不可变 Agent Revision，并从共享后端恢复 Session，不丢失已提交对话。

## 3. 总体设计

系统逻辑上分为控制面和数据面：

- 控制面包括 Admin API、配置与发布、灰度回滚，管理 Tenant、Agent App、不可变 Agent Revision、Channel Binding、Backend Profile 和 Policy。
- 数据面包括 Channel Adapter、Gateway、Inbox、Worker、Runtime Manager、Policy、Storage Router 和 Reply Outbox，负责一条消息的完整运行。

目标部署方案使用一个 Go 二进制按 `--role=all|gateway|worker|channel|job` 启动，生产部署可把 Gateway、Worker、Channel 和后台任务拆为独立 Kubernetes Deployment，并共享 PostgreSQL、Redis、PGVector、对象存储和 OpenTelemetry Collector。多角色参数与生产部署当前未实现，属于架构说明。

详细架构图和职责见 [总体架构设计](architecture.md)。

## 4. 核心运行链路

目标架构中，企业微信智能机器人 Adapter 通过 Bot ID/Secret 订阅长连接，只处理已认证连接上、机器人标识匹配的消息。Gateway 根据受信任的账号绑定确定 Tenant/App，在一个 PostgreSQL 事务内插入或命中 Inbox 并为首次事件创建 `accepted` Run，提交后再通过 Redis Streams 投递 `run_id`。Run Coordinator 获取 Session 租约并成功 claim PostgreSQL Run 后，Runtime Manager 才加载指定 Revision 的 Agent/Runner。Worker 调用 tRPC-Agent-Go Runner，读取 Session/Memory、检索 Knowledge、执行 Tool/MCP，并持续消费流式 Event。结果写入 Session、Memory、Run 和 Audit，最终通过 Outbox 发送到企业微信。长连接没有 HTTP 200 确认步骤；消息推送、Inbox 提交、回复回执是三个不同事实，边界见下文 IM 设计。

当前公共文本消费者在可信 Tenant/Binding 范围扫描 PostgreSQL 任务，claim 后调用 `sessionrun.Start/Run`；Session 忙时在执行预算内等待。它排空 Event，原子完成 Run 和最终文本 Outbox，再由企微或飞书适配器发送。消息与恢复契约见 [IM 指南](im-channels.md)，验证结果见[验收说明](acceptance.md#验证结果)。

`trace_id` 贯穿 IM callback、Gateway、Runner、Model、Tool、Session/Memory 和 IM 回复；跨 Redis Streams 时显式携带 W3C `traceparent` 并在 Worker 恢复上下文。`request_id` 贯穿幂等、取消、状态查询、成本和审计。完整时序见 [核心消息时序](sequence.md)。

## 5. 重点技术

### 5.1 多租户隔离

所有请求必须解析明确的 `tenant_id`，失败时直接拒绝。tRPC-Agent-Go Session Key 只有 `AppName + UserID + SessionID`，平台将 `AppName` 编码为 `t/{tenant_id}/a/{agent_app_id}`，并在 Redis Key、SQL 行、向量元数据和对象路径中重复携带租户作用域。Tool 授权、密钥解析、预算、日志脱敏和审计同样按 Tenant 执行。

### 5.2 Agent 生命周期

Agent App 是稳定业务身份；Agent Revision 是模型、Prompt、Tool、Skill、Knowledge、Policy 和后端引用的不可变快照。Worker 按 `(tenant, app, revision)` 懒加载并缓存 `Agent + Runner`，每次请求只创建新的 Invocation。新版本生成新对象，旧版本等待活动 Run 结束后安全淘汰。灰度时同一 Session 默认固定 Revision，避免前后两轮 Prompt 或 Tool 集合变化；紧急安全回滚可主动使 Pin 失效。Session 不绑定进程内对象，因此 Worker 重启后可恢复。

### 5.3 无状态多节点

不使用节点级 sticky session。所有 Worker 都能读取共享配置和 Session。同一 Session 默认串行执行：Redis 租约在 Run 入口做合作型互斥，第二个 Worker 收到 `409 session_busy`，失去租约立即取消 Run；持有者崩溃时租约按 TTL 过期，另一个 Worker 接管。租约同时产出的单调 token 只用于观测，**不参与写入准入**——上游 `AppendEvent` 没有 fence/CAS 入口，因此过期 Worker 的写入不会被后端原子拒绝，取消是尽力而为且最终一致的（见 [Session Run Lease](session-lease.md)）。不同 Session 并行执行，并受租户并发、token 和费用配额控制。

### 5.4 多后端与一致性

目标架构使用 PostgreSQL 保存配置、Inbox/Outbox、Run 和 Audit，并作为入站去重与任务状态的唯一事实源；Redis 保存热 Session、租约、限流和可丢失的 Worker 唤醒；PGVector 保存 Knowledge/Memory 向量；S3-compatible storage 保存 Artifact 和知识源文件。当前入口已接入配置与 Session 路由，公共文本消费者已接入 PostgreSQL Inbox/Run/Outbox；分布式 IM 调度、持久 Audit、Memory、向量库和对象存储尚未接入运行链路。平台基于 tRPC-Agent-Go Service 接口增加租户路由和能力矩阵，不把不同后端伪装成相同语义。

Event 和 StateDelta 通过上游 `AppendEvent` 原子提交。Summary、Memory 和向量索引是带来源版本的派生数据，默认最终一致且可重建。Session 迁移采用按会话冻结、复制、校验、切换和观察；向量库迁移从源文档重建新索引版本后原子切换。详细设计见 [数据模型](data-model.md) 和 [存储与一致性](storage-and-consistency.md)。

### 5.5 IM 接入差异

| 能力 | 企业微信智能机器人长连接 | 飞书事件订阅 |
| --- | --- | --- |
| 入站方式 | 主动连接 `wss://openws.work.weixin.qq.com`，发送 `aibot_subscribe`；接收 `aibot_msg_callback` | 当前采用自建应用官方 Go SDK 长连接；HTTPS Webhook 保留为扩展设计 |
| 安全 | Bot ID/Secret 认证；校验事件 `aibotid` 与受信任连接绑定一致，不用自建应用 access token | App ID/Secret 认证查询企业身份；核对事件 App/企业和 sender 企业，静态绑定内部 Tenant/App；Webhook 签名/Token 方案见下文 |
| 入站确认 | 推送帧没有 HTTP 响应；只有 Inbox 事务提交后才在平台内部标记受理，不假定断连后必然补投 | SDK 在处理器返回后 ACK；持久受理成功才返回 nil，回调串行受理且有限等待，停止时排空；验证范围见验收说明 |
| 幂等与回复关联 | `body.msgid` 作入站事件键；`headers.req_id` 用于回复关联，不等同于平台 `request_id` | `im.message.receive_v1` 按 `message_id` 在 Binding 内去重，不能只依赖 `event_id`；message/chat/thread 标识用于回复定位 |
| 身份与会话 | `from.userid`、`chattype`、群聊 `chatid`，机器人账号先绑定 Tenant/App | App、Chat、User、Thread 共同决定绑定和会话作用域 |
| 文本回复 | `aibot_respond_msg` 透传 `req_id`，以固定 `stream.id` 发送最终文本；收到成功回执再确认 Outbox | Bot 消息 API 按 message/chat ID 回复；平台流式/卡片更新能力与普通文本分开 |
| 限制与失败 | 官方 SDK 的流式文本上限为 20,480 字节；按具体消息类型限制输出，同一 `req_id` 串行发送，超时记录结果未知 | 当前采用保守 4096 UTF-8 字节最终文本；单次 HTTP 回复，未知不重发；生产按消息类型配置限频/退避 |
| 扩展与撤回 | 图片/文件需下载解密和媒体上传，卡片、欢迎语、主动发送有独立协议；均不进入首条文本链路 | 图片/文件、富文本、交互卡片和撤回分别映射事件与出站动作，未支持时明确拒绝或记录 |
| 当前状态 | 单静态 Binding、单聊纯文本已实现，真实正常收发已验证；增量流、群聊、媒体、卡片和撤回仍为设计 | [飞书接入](im-channels.md)已实现，身份预检、本地协议与 PostgreSQL 集成通过；真实连接及两轮正常单聊收发已验证 |

协议依据为[企微官方 SDK README](https://github.com/WecomTeam/aibot-node-sdk/blob/80615b987ef69c6028ad764924609247c0725955/README.md) 和 [WebSocket 实现](https://github.com/WecomTeam/aibot-node-sdk/blob/80615b987ef69c6028ad764924609247c0725955/src/ws.ts)，2026-09-07 核对；这里只参考协议，不将 Node SDK 引入 Go 服务。自建应用 Webhook 的 `msg_signature`/AES、HTTP 200 和 access token 发送流程是另一种接入模式，不与智能机器人长连接混用。未核实的平台回复有效期、跨连接重试能力和限频数值保持待联调，不能据 SDK 的本地请求超时推定平台保证。

当前企微超长文本按 UTF-8 字节截断并带 `[truncated]` 标记，只发送一次 `finish=true`，不是逐字流式。生产限频设计在外部账号作用域分配配额、保持同会话顺序，按消息/API 类型配置额度，对已确认可重试的限流采用有上限的指数退避并遵守平台有效等待值；当前没有限频调度器，也不因发送失败自动重发。媒体下载/解密、受租户保护的附件引用和出站上传由 Adapter 负责；当前不支持的消息不进入 Runner。各项降级和待核实边界见[平台限制](im-channels.md#恢复与平台限制)。

**飞书 HTTPS 回调扩展设计，未实现。** 当前实际接入采用[官方长连接](im-channels.md)，以下 Webhook 方案继续用于通道差异和扩展设计。入口拟为 `POST /channels/feishu/{account_id}/events`，每个飞书应用只登记一个回调 URL。路径中的账号 ID 仅定位服务端已登记的候选 Binding 和凭据引用（Verification Token、可选 Encrypt Key、预期 App ID 及允许的飞书 tenant_key），不是租户认证结果；不依赖请求体声明的 App/Tenant 去寻找任意密钥。

处理顺序如下，所有鉴权前解析和解密都不触发 Inbox、Runner 或业务路由：

1. 按入口定位候选凭据，限制请求体大小并保留原始 body 字节。根据服务端配置解析明文或解密 `encrypt` 包装，识别事件类型；配置了 Encrypt Key 却收到普通明文事件时拒绝降级。解析出的身份此时仍不可信。
2. `url_verification` 是独立分支：必要解密后校验顶层 `token` 与该账号的 Verification Token 一致，再在官方要求的 1 秒内返回 `{"challenge":"原值"}`；不创建 Run。官方 challenge 示例没有 App ID/tenant_key，不要求这些字段，也不把普通事件的签名要求套到 challenge 上。
3. 普通事件配置了 Encrypt Key 时，必须用 `X-Lark-Request-Timestamp`、`X-Lark-Request-Nonce`、Key 和**原始 body** 按顺序计算 SHA-256，并核对 `X-Lark-Signature`；缺失或不匹配即拒绝。不能对重新序列化或解密后的 JSON 验签。签名计算本身不依赖解密，可在签名头齐备时提前执行；仅解密成功不能跳过验签。
4. 普通事件未配置 Encrypt Key 时，仍须校验 Verification Token，不允许因 SDK 跳过签名就放行。对本设计采用的 v2.0 消息事件，无论是否加密都显式核对 `header.token`、`header.app_id` 和已登记的 `header.tenant_key`；App/租户不符或无法唯一定位有效 Binding 即拒绝。通过后才按受信任的 Binding 及 Chat/User/Thread 映射平台 Tenant、App、Session，并校验允许的事件类型。
5. 消息内容通过验证后，以 Binding 内的 `message_id` 持久去重，Inbox 提交成功或命中已提交记录后再确认受理；不等待 Agent 执行完成。签名校验不替代持久去重，Token 和原始消息体也不写入日志。

以上依据为 2026-09-07 核对的飞书官方[Webhook 配置与 challenge](https://open.feishu.cn/document/ukTMukTMukTM/uYDNxYjL2QTM24iN0EjN/event-subscription-configure-/choose-a-subscription-mode/send-notifications-to-developers-server)、[事件安全校验与解密](https://open.feishu.cn/document/ukTMukTMukTM/uYDNxYjL2QTM24iN0EjN/event-subscription-configure-/encrypt-key-encryption-configuration-case)和[接收消息事件](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/events/receive)。官方 Go SDK 固定版本 [`b059ee1` 的 Dispatcher](https://github.com/larksuite/oapi-sdk-go/blob/b059ee1824d45444306559b5c33c3f268c0de10d/event/dispatcher/dispatcher.go)会先解析/解密、对 challenge 跳过签名、无 Encrypt Key 时直接跳过签名，并仅在 challenge 分支校验 Token；普通事件的 Token/App/租户绑定校验仍由平台负责，不能只构造 SDK Dispatcher 就宣称完成认证。

**撤回策略仍属设计。** 收到已验证的撤回事件时，记录对原消息和 Run 的关联，不删除已经提交的 Audit 或 Session Event；撤回也不自动抵消已执行的 Tool。通道支持撤回机器人回复时，经 Outbox 提交撤回动作，否则按租户策略忽略或发送更正说明。该策略承接[通道设计](im-channels.md)，不把撤回、媒体或卡片加入当前文本演示实现范围。

设计上两个 Adapter 共享统一 InboundEnvelope 和 Outbox，重复投递由 PostgreSQL Inbox 唯一约束裁决，Redis 不参与权威去重。企微出站目标保存版本、Binding、收到的 `req_id`、会话引用和稳定 `stream.id`；飞书按原始 `message_id` 回复，目标携带完整绑定作用域，不随 WebSocket 换代失效。敏感引用不写日志。ACK 超时或连接断开不算成功，也不重跑 Agent；生产扩展只有确认平台允许且目标仍有效时才重试，否则记录结果未知或投递失败。当前企微每条最终回复最多一次发送尝试，未知结果保留 `duplicate_risk` 且不重发，连接换代后旧目标失败。平台不提供幂等保证时，稳定 ID 仅用于关联，不能宣称发送 exactly-once。

单聊按 Binding 与可信用户映射 Session，群聊按 Binding、群和显式线程划分，规则见[Session 命名](architecture.md#54-session-命名)。当前部署限制同一 Bot 一个活动实例。企微连接可能互相替换，断线重连停止旧连接的待回执等待；飞书多个连接可能分摊事件，SDK 回调并发不保证原始时间顺序。Inbox 提交前的进程故障可能丢失尚未持久化的帧；飞书虽有超时重推，也不能作为无限恢复保证。监测连接与持久化失败、明确提示用户重试，生产可评估平台回放能力或本地持久接收层。该残余风险不因采用长连接而自动消失。

### 5.6 治理与安全

Runner 的 Plugin、Guardrail 和 Callbacks 承载请求级策略。执行前检查 IM 用户权限、Tool 白名单、预算和敏感输入；危险 Tool 进入人工审批；执行后进行输出脱敏和审计。密钥配置只保存 `secret_ref`，日志、Trace、错误和审计不记录明文密钥。Tool 必须显式声明是否可重放；具有副作用且允许自动重放的 Tool 使用跨模型 attempt 稳定的业务操作键，不能把可能重生的上游 `tool_call_id` 当成跨 attempt 保证。

| 治理阶段 | 生产决策与失败行为 |
| --- | --- |
| 入站 | 根据已验证 Tenant/Principal/Binding 检查用户是否可访问 App；缺失身份或权限拒绝，不允许模型决定租户或授权 |
| Model 前后 | Plugin 在调用前按租户预算原子预占本次有界额度，超额拒绝；完成后按供应商 usage 与版本化价格表结算。调用结果未知时保留预占并进入对账，不把未知费用当作零 |
| Tool 前 | Callback 以租户已授权 Policy 的交集裁决 Tool；危险操作的批准绑定 Tenant、主体、操作、参数摘要和有效期。参数变化、审批拒绝或过期均不执行，不以聊天中的一句确认直接放行 |
| 输出前 | Guardrail 在最终回复或每个可发布流式片段离开服务前检查。需要全文才能判断的策略缓冲完整输出并降级为最终回复；已发送内容无法靠事后脱敏撤销 |
| 审计 | 记录 allow/deny/approve/redact/limit 等决策与稳定错误分类，使用[审计字段](data-model.md#37-inboxrunoutbox-与-audit)。`latency_ms` 对应题目 latency 的毫秒值，费用或 Trace 尚不可得时明确为空/未知 |

上表是治理设计。当前实现为静态身份与 SecretRef entitlement、Tool/Policy 交集、Tool callback 结构化审计和循环上限；用户级 IM 动态授权、预算预占结算、危险操作审批、输出 Guardrail 和持久 Audit Store 尚未实现。现有 Secret 边界及局限见[安全说明](security-and-governance.md)，不因当前实现观测接入扩大治理实现。

### 5.7 可观测性

目标设计中 OpenTelemetry Span 覆盖 Channel、Gateway、Run、Model、Tool、Session、Memory 和 Outbox。核心指标包括每租户请求量、并发 Run、模型/Tool/后端延迟、错误率、IM 投递成功率、token、费用和队列等待时间。审计记录至少包含题目指定的 `tenant_id`、`channel`、`user_id`、`session_id`、`agent_name`、`tool_name`、`decision`、`latency`、`error_type`、`cost` 和 `trace_id`。

| 指标 | 采集点和统计口径 |
| --- | --- |
| 新请求与重复入站 | Inbox 首次提交计一个新请求，重复命中单独计数；持久化内部重试不能重复增加受理次数 |
| 阶段、模型、Tool、后端与排队耗时 | 各阶段起止点记录带明确单位的直方图，区分等待与实际调用；失败样本保留 outcome，不能只统计成功耗时。当前阶段指标使用毫秒 `ms` |
| IM 投递结果 | 平台明确成功 ACK / 实际发送尝试数为成功率；拒绝、未知、目标过期分开。Outbox 状态写入重试不是新发送，ACK 不表示用户已读 |
| 错误率与并发 | 错误阶段数 / 同阶段处理总数；活跃 Run 为已开始且未结束数量，不能将排队数计作运行并发 |
| token 与租户成本 | 模型返回的 usage 按输入/输出及供应商语义记录，以模型与价格版本计算费用；缺失 usage 或价格时标记未知，不填零。跨租户查询由受控成本明细按租户聚合 |

完整 Trace 为目标设计：受理端创建上下文，将 W3C `traceparent` 随 Inbox/Run/Outbox 持久化，Worker/发送器恢复上下文，Runner、Model、Tool 与存储适配器传递同一 Context；异步派生 Memory 使用父上下文或 Span Link 关联。当前表结构没有持久 Trace 上下文字段。

当前[可选观测](local-deployment.md#观测边界)使用独立 provider，在受理、执行、发送三个阶段生成 Span、次数与耗时，以持久 `request_id` 关联；阶段可能属于不同 Trace，尚未实现跨队列连续父子 Trace、Model/Tool/Session/Memory 细分采集或成本统计。指标标签只用有限阶段、通道、结果类别及静态绑定的内部租户/App；request/user/session 等高基数 ID 不进入指标。观测只采集白名单，上游自动 tracing 保持关闭，具体结果见[验收说明](acceptance.md#验证结果)。

当前 `trpc.channel.stage.count` 统计阶段处理次数，`trpc.channel.stage.duration` 记录对应毫秒耗时。`execute` 的结果是本次执行决策，不能替代持久 Run 终态；`deliver` 还含发送前跳过和旧目标记录，计算实际投递率时须区分 outcome，不直接用全部阶段次数作分母。启用时 OTel 进程级错误处理器只输出固定诊断，避免 SDK 将 Collector 原文写入日志；它不安装全局 provider，但会影响进程中其他 OTel 错误的诊断详细度，关闭遥测时不安装。

### 5.8 故障恢复

- Worker 故障：Redis PEL 只认领尚未成功 claim PostgreSQL Run 的唤醒；claim 后由 PostgreSQL deadline 扫描器重置过期 `running` attempt，并重投长期 `accepted` 的 Run。只有显式可重放且具备业务幂等的 Tool 才能自动重试。
- IM 重试：相同外部事件返回原 `request_id`，不创建第二个 Run。
- 模型超时：取消 Context，排空 Runner Event Channel，保存终止状态并给出可重试回复。
- 数据库故障：有界重试；不能把生产 Session 静默切到 InMemory。
- Memory/Knowledge 故障：按 Agent 策略降级并在 Trace 和回复元数据中标识。
- 灰度与回滚：Run 开始时固定 Revision，回滚只改变新请求路由。

## 6. 容量估算方法

容量以实测 P95 模型延迟、平均 Tool 次数和消息大小校准。初始估算示例：

- 单 Worker 允许 100 个并发 Run，平均一轮 15 秒，则理论吞吐约 `100 / 15 = 6.7 RPS`；按 60% 安全水位规划约 4 RPS。
- 峰值 20 RPS 时，Worker 数量至少为 `ceil(20 / 4) = 5`，再增加 1 个故障冗余，共 6 个。
- 每轮平均写入 6 个 Event，则 Session Backend 峰值写 QPS 约 `20 × 6 = 120`，按两倍突发准备 240 QPS。
- 每轮输入输出合计 4,000 token，20 RPS 时模型消耗约 `80,000 token/s`，必须按租户和模型供应商设置预算与限速。
- IM 回调按日均峰值系数 10 估算，Inbox 和 Gateway 保留至少两倍突发余量；若提供压测，报告应区分实测结果与这些假设，原题未要求达到示例容量。

## 7. 预期效果

以下是目标架构的生产效果，未实现部分以设计和限制说明交付。网页、企微和飞书文本链路的实现与真实运行范围见[验收说明](acceptance.md#验证结果)。

1. 两个以上租户可以创建、发布和隔离运行各自 Agent。
2. 企业微信与飞书完成从入站到回复的全链路演示。
3. 两个 Worker 无需 sticky session 即可继续同一持久化会话。
4. Redis、PostgreSQL、PGVector 和对象存储职责清楚，并演示 Session/索引迁移。
5. 重复 IM 消息只产生一个 Run；纯函数 Tool 或具备跨 attempt 业务幂等的 Tool 可保证一次业务副作用，其他 Tool 的未知结果转显式失败处理而不自动重放。
6. 单次请求可以通过 `trace_id` 查询模型、Tool、存储和回复耗时，通过 `request_id` 查询状态、成本和审计。
7. Worker 重启、模型超时和后端短暂故障有明确、可测试的恢复行为。

## 8. 部署与验证

[本地部署](local-deployment.md)提供默认网页、可选持久化、IM 和 Collector 的运行步骤。[验收说明](acceptance.md)给出七项设计映射、代码范围和可复现检查。生产独立 Gateway/Worker、共享后端与租户级配置传播见[节点部署](architecture.md#6-节点部署)。

## 9. 主要风险

此表列目标架构的风险与缓解方向，不是功能实施清单。当前参考版本采用的措施、未实施措施及残余限制需分别标明；未来后端、极端故障和平台不提供的强保证不因进入本表而自动增加实现范围。

| 风险与触发条件 | 当前边界与残余风险 | 检测/降级与生产缓解方案 |
| --- | --- | --- |
| 同 Session 并发或暂停的旧 Worker 恢复写入 | 已有合作型租约与取消；上游 AppendEvent 无 fence/CAS，不能原子拒绝旧 writer；Redis failover 不在互斥保证内 | 监测续约失败、重叠 Run 与 Event 异常；协调失败拒绝新 Run。严格生产要求需支持写入准入的后端或上游接口 |
| IM 重复、乱序或落库前进程退出 | 企微 Inbox 持久去重并按同 Session 受理顺序执行/回复，本地集成已验证；长连接未持久化帧仍不保证平台补投 | 监测重复数、连接中断、持久化错误和队列年龄；提示未受理消息重试。生产评估平台回放或持久接收层 |
| Tool 执行成功但结果未知后重放 | 现有内置 Tool 无业务副作用；企微已启动但结果未知的 Run 明确失败，不自动重跑，不替外部系统提供幂等 | 未知结果转显式失败/人工对账；生产副作用 Tool 需业务操作键和结果查询，不能只依赖模型 tool_call_id |
| 作用域缺失导致跨租户读写 | 当前配置、Session、Tool 和 Secret 边界有隔离测试；未来向量库/对象存储尚未接入，RLS 未启用 | 拒绝缺失租户与越权请求，监测拒绝事件；生产为向量过滤、对象路径、审计和缓存逐层验证作用域，可加 RLS |
| Worker 崩溃或 Outbox 未提交 | 企微要求持久 Session/Pin；未启动任务可恢复，已启动未知任务失败，尚无最终答案重建；HTTP 默认 InMemory 重启丢失 | 监测 Run 超期和未投递结果；生产按持久 Event 对账、attempt CAS 与 Outbox 恢复，禁止盲目重跑未知副作用 |
| Summary/Memory 旧任务晚到 | 目前为设计，未提供派生存储；不可宣称已有版本检查 | 记录输入 Event 边界与索引延迟；旧版本丢弃/保留历史，可从源 Event 重建，用户可选择等待写后可见 |
| 迁移切换后回退到旧源库 | 当前无迁移 Job；目标已有新写入时旧源不再完整 | 冻结 Session 并校验 Event ID、数量与摘要；写入未知时停止切换，对账补齐后回切，不允许静默回退 |
| 慢模型/Tool 占满执行资源 | 有 Context 取消、Event 排空与 Tool 循环上限；企微有持久执行预算，当前单消费者串行，无租户预算或断路器 | 监测活跃 Run、耗时和取消后残留；生产增加分级超时、配额、舱壁和告警，后端故障不降级为易失存储 |
| Secret 被日志、错误或上游地址带出 | 已有 SecretRef entitlement 与明确错误边界脱敏；base_url 无出站白名单，环境变量不是进程隔离，配置摘要不是签名 | 限制管理与出站网络权限，审计目标域名与授权变更；生产增加 Secret Manager、轮转及敏感日志检测 |
| IM 限流、发送超时或旧 req_id 失效 | 企微正常发送获真实 ACK；拒绝/未知/旧目标由本地测试验证，每条最终回复最多一次尝试，无完整限流器和最终送达保证 | 监测投递年龄、回执超时和 duplicate_risk；生产按平台能力决定配额、条件退避及对账，失效目标停止发送，发送失败不重跑 Agent |
| Runtime/连接缓存持续增长 | 已有引用计数与关闭顺序；Runtime/Bundle 无完整 TTL/LRU，每租户 Profile 有上限，总租户数仍影响资源量 | 监测缓存数、活跃租约、连接和内存；生产按资源预算淘汰空闲对象，先拒绝新引用再等待活动引用退出 |
| 遥测导出失败或标签基数失控 | 当前只采集白名单和有限标签，阶段依赖 request_id 关联；进程崩溃可丢未导出记录，指标不是持久账本 | 监测 Collector 队列、导出失败和内存；生产配置采样、保留期和容量，故障不影响业务执行与发送 |

## 10. 交付与验收

交付包括方案总稿、架构图、时序图、数据模型、同步和幂等方案、多后端方案、风险清单、运行代码与可复现演示。详细要求到证据的映射见[验收矩阵](acceptance.md)，演示场景和证据采集方式见[本地部署](local-deployment.md)。
