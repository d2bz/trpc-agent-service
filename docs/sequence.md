# 核心消息时序

## 1. 企业微信完整链路

```mermaid
sequenceDiagram
    autonumber
    actor User as 企业微信用户
    participant WC as 企业微信
    participant CA as WeCom Channel Adapter
    participant GW as Agent Gateway
    participant DB as PostgreSQL Inbox/Config
    participant RC as Run Coordinator
    participant RM as Runtime Manager
    participant R as tRPC-Agent-Go Runner
    participant T as Tool/MCP
    participant S as Session Service
    participant M as Memory Service
    participant OB as Reply Outbox
    participant OT as OpenTelemetry

    User->>WC: 发送消息
    WC->>CA: Webhook 回调
    CA->>CA: 验签、解密、解析消息
    CA->>GW: InboundEnvelope
    GW->>OT: 建立 trace，生成 request_id/traceparent
    GW->>DB: 查询 Channel Binding 与发布路由
    GW->>DB: INSERT Inbox(external_event_id)

    alt 重复投递
        DB-->>GW: 唯一键冲突，返回已有 request_id
        GW-->>CA: 已受理
        CA-->>WC: 200 OK
    else 首次投递
        DB-->>GW: tenant_id + app_id + revision_id
        GW-->>CA: 已受理
        CA-->>WC: 200 OK
        GW->>RC: Redis Stream(inbox_id, traceparent)
        RC->>OT: Extract traceparent，恢复/关联 trace
        RC->>RC: 获取 Session 租约
        RC->>RM: 获取指定 Revision Runtime
        alt Runtime 缓存未命中
            RM->>DB: 读取不可变 Revision 和 secret_ref
            RM->>RM: 组装 Agent + Runner 并缓存
        end
        RM-->>RC: Runner
        RC->>R: Run(ctx, userID, sessionID, message)
        R->>S: Get/Create Session
        S-->>R: 历史 Event、State、Summary
        R->>M: 检索长期 Memory
        M-->>R: 相关 Memory
        R->>OT: Model span
        R->>T: 调用 Tool/MCP
        T->>OT: Tool span + 审计决策
        T-->>R: Tool 结果
        R->>S: 顺序追加 Event 和 StateDelta
        R-->>RC: 流式 Event Channel
        RC->>RC: 持续消费直到关闭
        RC->>M: 异步写入 Memory
        RC->>DB: 更新 Run 状态与成本
        RC->>OB: 写入回复，幂等键=request_id+part
        RC->>RC: 释放 Session 租约
        OB->>CA: 发送任务
        CA->>WC: 按长度拆分/媒体上传/限流发送
        WC->>User: Agent 回复
        CA->>DB: 记录投递结果
    end
```

`trace_id` 从 Gateway 创建后写入 Context，并传递给 Runner、Model、Tool、Session、Memory 和出站发送 Span。`request_id` 是平台 Run 的稳定业务标识，也作为重试、取消、结果查询和成本聚合的关联键。

## 2. 关键顺序规则

一次 Run 内按以下顺序处理：

1. 验签成功后才解析租户和身份。
2. Inbox 首次写入成功后才向 IM 平台确认已受理。
3. 获取 Session 租约后才加载 Session 和调用 Runner。
4. Runner 顺序持久化用户输入、模型输出、Tool 调用和 Tool 结果 Event。
5. StateDelta 与对应 Event 由具体 Session Backend 在同一原子操作中处理；平台不拆开写入。
6. Run 完成后，以已提交 Event 的边界触发 Summary 和 Memory 更新。
7. 回复先进入 Outbox，再调用 IM API；网络错误只重试 Outbox，不重新运行 Agent。

Summary 是派生数据，必须记录输入 Event 边界。旧 Summary 生成任务晚到时，如果其边界小于当前版本，只保存历史版本或丢弃，不能覆盖更新的 Summary。

## 3. 同一 Session 同时收到两条消息

```mermaid
sequenceDiagram
    participant A as Message A
    participant B as Message B
    participant Q as Run Coordinator
    participant R as Runner
    participant S as Shared Session

    A->>Q: session-key-X
    B->>Q: session-key-X
    Q->>Q: A 获得租约，B 排队
    Q->>R: Run A
    R->>S: 追加 A 的 Event
    R-->>Q: A 完成
    Q->>Q: 释放并把租约交给 B
    Q->>R: Run B
    R->>S: B 读取包含 A 的最新历史
    R->>S: 追加 B 的 Event
```

HTTP 调用方可以选择等待或收到 `409 session_busy`；IM 场景默认按到达时间排队。队列必须有最大长度和等待超时，超过限制时返回明确的繁忙提示。

> **当前实现只到"获得租约/被拒绝"这一步。** 上图里的队列尚未实现：拿不到租约的请求直接收到 `409 session_busy` + `Retry-After`，不排队、不继承租约。租约本身也只在 Run 入口互斥，不阻止已经在写的旧 Worker——见 [Session Run Lease](session-lease.md)。

## 4. Worker 故障与重试

Worker 可能在三个阶段故障：

| 故障点 | 恢复方式 |
| --- | --- |
| Runner 调用前 | 任务仍在 Inbox，其他 Worker 重新领取 |
| Runner 运行中、尚无外部副作用 | 租约过期后重跑，同一 Inbox 记录保持相同 `request_id` |
| Tool 已产生副作用 | Tool 使用 `request_id + tool_call_id` 幂等；无法幂等的 Tool 标记为 `manual_review`，不自动重放 |
| Session 已写入、回复未发送 | 不重跑 Agent，只从已持久化结果重新生成或重试 Outbox |
| 回复请求超时、结果未知 | 先按平台能力查询投递状态；无法查询时以稳定客户端消息 ID 重试并记录可能重复风险 |

## 5. HTTP 流式链路差异

HTTP/SSE 不需要 Channel Outbox 才能逐片返回：Worker Event 可以由 Gateway 实时转发给客户端，同时把稳定 Event 写入 Session。客户端断开时由策略决定取消 Run或转为后台运行；无论哪种方式，都必须取消无主 goroutine，并允许客户端使用 `request_id` 查询最终状态。
