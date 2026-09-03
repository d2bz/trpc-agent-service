# 多租户节点化 Agent 平台方案

> 方案版本：1.0-rc1
> 启动日期：2026-08-21
> 方案提交：2026-08-27
> 项目验收：2026-09-11

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

开发阶段使用一个 Go 二进制按 `--role=all|gateway|worker|channel|job` 启动，避免过早微服务化。生产部署把 Gateway、Worker、Channel 和后台任务拆为独立 Kubernetes Deployment，并共享 PostgreSQL、Redis、PGVector、对象存储和 OpenTelemetry Collector。

详细架构图和职责见 [总体架构设计](architecture.md)。

## 4. 核心运行链路

企业微信消息到达后，Channel Adapter 先验签、解密并转换为统一消息；Gateway 根据外部账号确定 Channel Binding、Tenant 和 Agent App，并持久化 Inbox 幂等记录，再通过 Redis Streams 投递 `inbox_id`。Run Coordinator 获取 Session 租约，Runtime Manager 加载指定 Revision 的 Agent/Runner，Worker 调用 tRPC-Agent-Go Runner，读取 Session/Memory、检索 Knowledge、执行 Tool/MCP，并持续消费流式 Event。结果写入 Session、Memory、Run 和 Audit，最终通过 Outbox 重试发送到企业微信。

`trace_id` 贯穿 IM callback、Gateway、Runner、Model、Tool、Session/Memory 和 IM 回复；跨 Redis Streams 时显式携带 W3C `traceparent` 并在 Worker 恢复上下文。`request_id` 贯穿幂等、取消、状态查询、成本和审计。完整时序见 [核心消息时序](sequence.md)。

## 5. 重点技术

### 5.1 多租户隔离

所有请求必须解析明确的 `tenant_id`，失败时直接拒绝。tRPC-Agent-Go Session Key 只有 `AppName + UserID + SessionID`，平台将 `AppName` 编码为 `t/{tenant_id}/a/{agent_app_id}`，并在 Redis Key、SQL 行、向量元数据和对象路径中重复携带租户作用域。Tool 授权、密钥解析、预算、日志脱敏和审计同样按 Tenant 执行。

### 5.2 Agent 生命周期

Agent App 是稳定业务身份；Agent Revision 是模型、Prompt、Tool、Skill、Knowledge、Policy 和后端引用的不可变快照。Worker 按 `(tenant, app, revision)` 懒加载并缓存 `Agent + Runner`，每次请求只创建新的 Invocation。新版本生成新对象，旧版本等待活动 Run 结束后安全淘汰。灰度时同一 Session 默认固定 Revision，避免前后两轮 Prompt 或 Tool 集合变化；紧急安全回滚可主动使 Pin 失效。Session 不绑定进程内对象，因此 Worker 重启后可恢复。

### 5.3 无状态多节点

不使用节点级 sticky session。所有 Worker 都能读取共享配置和 Session。同一 Session 默认串行执行：Redis 租约在 Run 入口做合作型互斥，第二个 Worker 收到 `409 session_busy`，失去租约立即取消 Run；持有者崩溃时租约按 TTL 过期，另一个 Worker 接管。租约同时产出的单调 token 只用于观测，**不参与写入准入**——上游 `AppendEvent` 没有 fence/CAS 入口，因此过期 Worker 的写入不会被后端原子拒绝，取消是尽力而为且最终一致的（见 [Session Run Lease](session-lease.md)）。不同 Session 并行执行，并受租户并发、token 和费用配额控制。

### 5.4 多后端与一致性

参考实现使用 PostgreSQL 保存配置、Inbox/Outbox、Run 和 Audit；Redis 保存热 Session、租约、幂等和限流；PGVector 保存 Knowledge/Memory 向量；S3-compatible storage 保存 Artifact 和知识源文件。平台基于 tRPC-Agent-Go Service 接口增加租户路由和能力矩阵，不把不同后端伪装成相同语义。

Event 和 StateDelta 通过上游 `AppendEvent` 原子提交。Summary、Memory 和向量索引是带来源版本的派生数据，默认最终一致且可重建。Session 迁移采用按会话冻结、复制、校验、切换和观察；向量库迁移从源文档重建新索引版本后原子切换。详细设计见 [数据模型](data-model.md) 和 [存储与一致性](storage-and-consistency.md)。

### 5.5 IM 接入差异

| 能力 | 企业微信 | 飞书 |
| --- | --- | --- |
| 入站方式 | HTTPS Webhook 回调 | 事件订阅 Webhook 或 WebSocket 长连接 |
| 安全 | `msg_signature` 验签、AES 解密 | Verification Token、Encrypt Key、请求签名 |
| 回调时限 | 需快速确认，长任务主动回复 | 事件快速确认，长任务调用 Bot 消息 API |
| 会话 | 企业成员、外部联系人、群聊 | Chat、User、Thread |
| 富消息 | 文本、图片、文件、模板/应用消息 | 文本、富文本、图片、文件、交互卡片 |
| 限流处理 | 按企业/应用接口限制退避 | 按应用与群维度限频并退避重试 |
| 参考用途 | 满足微信体系验收并验证企业身份 | 低成本真实联调和第二通道差异 |

两个 Adapter 共享统一 InboundEnvelope 和 Outbox，平台差异只留在验签、身份提取、消息转换、限流与发送实现中。重复投递由 Redis 快路径和 PostgreSQL 唯一约束共同去重。

### 5.6 治理与安全

Runner 的 Plugin、Guardrail 和 Callbacks 承载请求级策略。执行前检查 IM 用户权限、Tool 白名单、预算和敏感输入；危险 Tool 进入人工审批；执行后进行输出脱敏和审计。密钥配置只保存 `secret_ref`，日志、Trace、错误和审计不记录明文密钥。具有副作用的 Tool 使用 `request_id + tool_call_id` 作为业务幂等键。

### 5.7 可观测性

OpenTelemetry Span 覆盖 Channel、Gateway、Run、Model、Tool、Session、Memory 和 Outbox。核心指标包括每租户请求量、并发 Run、模型/Tool/后端延迟、错误率、IM 投递成功率、token、费用和队列等待时间。审计记录至少包含题目指定的 `tenant_id`、`channel`、`user_id`、`session_id`、`agent_name`、`tool_name`、`decision`、`latency`、`error_type`、`cost` 和 `trace_id`。

### 5.8 故障恢复

- Worker 故障：Redis Streams Consumer Group 认领未确认消息，PostgreSQL Inbox 扫描补偿；Tool 依靠业务幂等键安全重试。
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
- IM 回调按日均峰值系数 10 估算，Inbox 和 Gateway 保留至少两倍突发余量；实际结果由压测报告替换。

## 7. 预期效果

1. 两个以上租户可以创建、发布和隔离运行各自 Agent。
2. 企业微信与飞书完成从入站到回复的全链路演示。
3. 两个 Worker 无需 sticky session 即可继续同一持久化会话。
4. Redis、PostgreSQL、PGVector 和对象存储职责清楚，并演示 Session/索引迁移。
5. 重复 IM 消息只产生一个 Run 和一次 Tool 副作用。
6. 单次请求可以通过 `trace_id` 查询模型、Tool、存储和回复耗时，通过 `request_id` 查询状态、成本和审计。
7. Worker 重启、模型超时和后端短暂故障有明确、可测试的恢复行为。

## 8. 时间规划

| 日期 | 工作与交付物 |
| --- | --- |
| 8/21 | 冻结项目背景、总体架构、核心时序、数据模型、存储与一致性方案 |
| 8/22-8/23 | 完成治理、可观测性、运维、风险、验收矩阵；验证最小 Runner 链路 |
| 8/24-8/25 | 完成方案总稿、架构图、容量与演示计划；完成 Tenant/Revision、Admin API、动态路由和 Runtime 缓存的内存参考实现 |
| 8/26 | 方案评审、图表渲染、交叉检查、提交演练并冻结 `1.0-rc1` |
| 8/27 | 提交方案文档；演示多租户 Admin → Runtime → Runner → Session 链路 |
| 8/28-8/31 | PostgreSQL 控制面、Gateway、共享 Redis/PostgreSQL Session、Runtime 淘汰和双 Worker 验证 |
| 9/1-9/3 | Session Run Lease、双 Worker、StorageBundle、租户 BackendProfile 与动态 PostgreSQL/Redis |
| 9/4 | Inbox/Outbox、Channel 公共链路、幂等与重试骨架 |
| 9/5-9/6 | 周末，不安排开发任务 |
| 9/7 | 企业微信、飞书、身份映射、群聊规则、媒体/限流与端到端测试 |
| 9/8 | Memory/Summary、迁移验证、Guardrail/预算/审计、OpenTelemetry、故障恢复与部署收口 |
| 9/9-9/10 | 冻结功能开发；全量联调、容量测试、演示脚本和缺陷修复 |
| 9/11 | 正式验收 |
| 9/12-9/14 | 验收后修复、材料整理和最终提交 |

## 9. 主要风险

| 风险 | 缓解措施 |
| --- | --- |
| 同一 Session 被多个 Worker 同时执行 | Redis owner-token 租约（已实现，合作型入口互斥 + TTL 接管）、有界队列（未实现）、后端 CAS/版本兜底（上游 `AppendEvent` 无 fence/CAS 入口，做不到；残余风险按 [§5.3](#53-无状态多节点) 接受） |
| IM 重复或乱序 | Inbox 唯一约束、平台事件 ID、会话队列、过期消息策略 |
| Tool 重试造成重复业务操作 | `request_id + tool_call_id` 幂等；不支持幂等的危险 Tool 转人工 |
| 租户数据串读 | 强制 TenantContext、复合查询、键前缀、向量过滤、对象路径分区和隔离测试 |
| Worker 崩溃丢失进行中请求 | Inbox 状态机、租约过期重领、Event/Outbox 持久化 |
| Summary/Memory 旧任务覆盖新数据 | 来源 Event 边界、版本检查、幂等写入和可重建机制 |
| 后端迁移数据遗漏 | 分批游标、事件 ID、数量与校验和验证、观察期和指针回滚 |
| 模型或 Tool 延迟拖垮节点 | 分级超时、Context 取消、并发舱壁、断路器和租户配额 |
| 密钥或敏感内容泄漏 | Secret Reference、结构化脱敏、日志字段白名单和访问审计 |
| IM 平台限流或发送结果未知 | Outbox、指数退避、`retry_after`、稳定消息 ID和重复风险记录 |
| Agent 对象长期空占资源 | 热门预热、低频懒加载、空闲 TTL、LRU、租户配额和引用计数淘汰 |
| 三周工期导致过度设计 | 模块化单体、参考后端收敛、逐条验收矩阵、优先完成纵向链路 |

## 10. 交付与验收

交付包括方案总稿、架构图、时序图、数据模型、同步和幂等方案、多后端方案、风险清单、运行代码与可复现演示。详细要求到证据的映射见[验收矩阵](acceptance.md)，演示场景和证据采集方式见[演示与验收计划](demo-plan.md)。
