# Channel、Inbox、Run 与 Outbox 实现契约

> 冻结日期：2026-09-03
> 适用范围：Channel 公共链路第一版。后续 Adapter 可以扩展消息类型，但不得绕过本文的持久化、租户、状态机和恢复边界。

2026-09-07 状态：本文保留 Channel 实验的工程契约，不作为原题必须全部实现的清单。相关代码尚未提交和接线，真实 IM 未完成；本次文档检查点不包含这些运行时代码。当前长连接协议、发送未知结果与合作型租约限制以[方案](solution.md#55-im-接入差异)、[Session 租约边界](session-lease.md)和[验收记录](verification-2026-09-07.md)为准。

## 1. 目标与所有权

`trpcservice/channels` 是平台自有的协议无关边界。当前固定的 tRPC-Agent-Go `v1.11.2` 没有可直接复用的 OpenClaw Go Channel 包；本项目只复用 `model.Message`、Runner 和 Session 等真实框架能力。

PostgreSQL 是 Inbox、Run 和 Outbox 的唯一事实源。Redis Stream 和进程内 Waker 都只传递唤醒通知，通知可以重复或丢失，不能决定任务是否存在、是否完成或是否可重试。

第一批实现包含：

- `InboundEnvelope`、`DeliveryTarget`、Inbox、Run、Outbox 和发送结果类型。
- 原子受理、Run claim/完成、Outbox claim/完成、恢复扫描所需的 Store 接口。
- 并发安全的 InMemory Store、PostgreSQL Store、迁移和共享 conformance suite。
- `sessionrun.Service` 的两阶段持有接口，保持现有 HTTP `Start` 行为兼容。

Waker、Worker、恢复扫描器、Sender Registry 和协议 Adapter 使用这些契约，但不在第一批 Store 实现中伪造。

## 2. 输入契约

Channel Adapter 完成验签、解密、请求体限长和协议解析后，Binding Resolver 把外部账号解析为可信的 Tenant、Agent App 和内部 Principal。只有完成这些步骤后才可构造 `InboundEnvelope`：

```go
type InboundEnvelope struct {
    TenantID         string
    Channel          ChannelType
    ChannelBindingID string
    AgentAppID       string
    PrincipalID      string
    SessionID        string
    ExternalEventID  string
    ReceivedAt       time.Time
    Message          InboundMessage
    DeliveryTarget   DeliveryTarget
}

type InboundMessage struct {
    Text        string
    Attachments []AttachmentRef
    ReplyTo     *MessageReference
}

type DeliveryTarget struct {
    Channel ChannelType
    Version uint16
    Payload json.RawMessage
}
```

约束如下：

- `TenantID`、`ChannelBindingID`、`AgentAppID`、`PrincipalID` 和 `SessionID` 必须通过 `tenant.ValidateResourceID`。
- IM 消息不携带 Revision hint：新 Session 使用 App 当前路由，已有 Session 使用服务端 Pin。HTTP 开发入口仍保留严格 hint 语义，hint 与既有 Pin 不同就返回冲突。
- `SessionID` 是平台生成的固定长度摘要 ID，只使用 `[A-Za-z0-9._-]`；原始外部用户、群、会话和话题 ID 不拼入该字段。
- `ExternalEventID` 必须非空且有明确长度上限。没有稳定事件 ID 的协议只能使用平台规范字段生成摘要，并记录降级风险；消息正文哈希不能冒充可靠事件 ID。
- `Message` 是可持久化的规范消息；第一版至少支持文本，并为媒体与引用保留有界、类型化表示。
- `DeliveryTarget.Payload` 是由对应 Adapter 定义和严格解析的版本化 JSON，必须包含进程重启后重新发送所需的真实 recipient/conversation/message reference。不能保存不可逆哈希或进程内 `any` 代替它。
- `ExternalEventID`、消息正文、附件来源和 `DeliveryTarget.Payload` 都是敏感数据：允许保存在 PostgreSQL，禁止进入普通日志、错误文本、Redis key 和 Stream payload。

受理调用由平台预先生成 `InboxID`、`RunID` 和 `RequestID`。它们必须是全局唯一、符合 Resource ID 规则的随机 ID，不能从外部请求读取。受理时还把有界的 `max_run_attempts`、`max_run_duration` 和 `recovery_grace` 固化到 Run；不同 Worker 不能在 claim 时临时选择不同策略。两个 duration 只接受整毫秒，避免 InMemory 保留纳秒而 PostgreSQL 整数毫秒列截断后产生行为漂移。

## 3. 原子受理与顺序

Store 暴露一个原子 `Accept(ctx, tenantScope, ids, envelope)` 操作，输出为已持久化 Inbox、Run 和 `Duplicate` 标记。调用前校验 `envelope.TenantID == tenantScope.TenantID`，不能只信 envelope 内的字符串。

首次事件在同一 PostgreSQL 事务内：

1. 按完整 Session key 获取事务级排序锁。
2. 插入 Inbox。
3. 创建同一 `request_id` 的 `accepted` Run。
4. 分配该 Session 内单调递增的 `accept_sequence`。
5. 提交后返回 `Duplicate=false`。

唯一约束必须至少包含：

```text
Inbox:  (tenant_id, channel_binding_id, external_event_id)
Run:    (tenant_id, run_id), plus globally unique run_id for wakeup lookup
Run:    (tenant_id, request_id)
Run:    (tenant_id, inbox_id)
```

重复事件返回第一次受理的原 `inbox_id`、`run_id` 和 `request_id`，`Duplicate=true`，不创建第二个 Run。受理结果同时返回该 Run 当前的 `attempt` 和**第一次**受理的时刻：重复受理发生时 Run 可能已经被 claim 过若干次，受理方要据此发布 wakeup 并记录投递（§6），用本次调用假设的 attempt 0 或本次调用的时刻都会写错代。Redis 不参与受理去重。

同一 Session 的 `accept_sequence` 以数据库事务锁后的受理顺序为准。Run 只能在同 Session 更早的非终态 Run 全部结束后被 claim；不同 Session 不互相阻塞。时间戳不能单独作为顺序依据。

## 4. Run 状态机与 CAS

Run 状态只有：

```text
accepted -> running -> succeeded | failed
                  \-> accepted   # 仅恢复扫描器可执行
```

`ClaimNextRun` 以完整 Session key 查找 `accept_sequence` 最小的非终态 Run，而不是强行 claim wakeup 指向的 Run。只有该最早 Run 本身是 `accepted` 且 `next_attempt_at` 已到期时才能 claim；它若为 `running` 或仍在退避，就返回当前无可 claim，绝不能跳过它执行后续消息。wakeup 的 `run_id` 只作提示；扫描恢复导致更早 Run 再次可执行时，返回的一定是更早 Run 的完整持久输入。

claim 调用方预先生成唯一随机 `claim_token`；Store 使用 Run 受理时已经固化的执行策略，并在同一原子操作中：

- `attempt = attempt + 1`。
- 保存调用方提供且从未用于其他 attempt 的 `claim_token`。
- 保存 `claimed_by`、`claimed_at`、`execute_deadline_at` 和更晚的 `recover_after`。
- 返回包含完整持久输入的 `RunClaim`。

`execute_deadline_at` 限制模型、Tool 和 Event 消费；`recover_after` 至少再覆盖 Runner 取消后的两秒尾写、claim 往返和配置的时钟偏差。Worker 用 Store 返回的剩余执行预算派生 Run Context，保证主动取消早于扫描器接管。

Worker 在调用 Runner 前，以 `(status=running, claim_token)` CAS 写 `execution_started_at`。只有该字段仍为空时，`YieldRun` 才能用同一 CAS 把瞬时 Open 失败或启动前取消的 Run 放回 `accepted`，并设置退避后的 `next_attempt_at`。Runner 一旦开始，未知结果不得 Yield，只能等待 `recover_after` 后扫描与对账。

同一 CAS 还写 `first_execution_started_at`：`execution_started_at` 是本次 attempt 的瞬时标记，claim、requeue、恢复和完成都会清掉它，因此它回答不了「这个 Run 有没有真的进过 Runner」——而这正是 §6 对账和「是否可能已经产生副作用」要问的问题。该字段一旦写下永不移动、永不清除；重复写入取行上已有的值而不是当前时刻，两个 Store 一致。它只记录第一次到达 Runner 的时刻，不替代 attempt 计数，也不能由 attempt 计数反推：claim 后未启动就死掉的 Worker 与启动后死掉的 Worker 在计数上无法区分。

任何可能把已用尽 `max_attempts` 的 Run 留在或放回 `accepted` 的路径，都必须改为原子终止 `failed`，`error_type=attempts_exhausted`，从而解除对同 Session 后续 Run 的阻塞；若 Binding 配置了安全的通用失败回复，应在同一事务写入对应 Outbox，不能先终止后补写。

Runner 完成后，`FinishRun` 在一个 PostgreSQL 事务内：

1. 以 `(tenant_id, run_id, status=running, claim_token)` 条件更新为 `succeeded` 或 `failed`。
2. 保存最终 Revision、稳定 `error_type`、统计和结束时间；错误正文不持久化。
3. 插入零到多个 Outbox part。

`RunStats.OutputParts` 必须与本次提交的 Outbox part 数量完全一致。条件未命中返回可匹配的 stale-claim 错误，事务不写入任何 Outbox。旧 attempt 的迟到结果无权覆盖新 attempt。即使调用方 Context 已取消，Finish 使用 `context.WithoutCancel` 派生的短超时 Context 尝试提交已知结果。

Runtime 解析成功后，Worker 通过 `(status=running, claim_token)` CAS 记录该 Session 实际 Pin 的 `revision_id`；0 行更新等同失去 claim，必须立即停止，不能继续花费模型预算。

claim 返回因连接或 Context 结束而不确定时，当前 Store 没有按 `claim_token` 查询的恢复 API，Worker 不能用 wakeup 的 Run ID 或普通列表猜测 claim 结果。应保持 wakeup 未确认、关闭 Hold 并让 Redis PEL 与 PostgreSQL deadline 扫描器恢复；后续若需要即时确认，必须新增显式的 token 查询/claim receipt 接口。`send_token` 的 claim 歧义使用相同的 fail-closed 规则。

## 5. `sessionrun` 两阶段持有

Worker 必须在 PostgreSQL claim 前取得 Session 租约，但不得在 claim 成功前加载 Runtime，也不能把 wakeup 对应 Run 的 request ID 提前绑定到 Context。为此 `sessionrun.Service` 提供：

```go
hold, err := service.Hold(ctx, sessionKey) // 只校验并持有完整 Session key
defer hold.Close()

anchor := time.Now() // 必须在 claim 之前读
claim, ok, err := store.ClaimNextRun(hold.Context(), tenantScope, sessionKey, ...)
// claim 成功后才确认 wakeup

request := requestFromClaim(claim)
handle, err := hold.Open(request, *claim.Run.ExecuteDeadlineAt)
defer handle.Close()
```

`Open` 收的是绝对执行 deadline，不是剩余时长。`RemainingExecutionBudget` 只用于观测或日志；Worker 必须把 Store 返回的 `Run.ExecuteDeadlineAt` 原样传给 `Open`，不能在第二次 `time.Now()` 上重基。这样不会把 claim 往返和 Runtime 解析耗掉的时间加回执行预算，恢复路径也会和当前 attempt 使用同一时间边界。deadline 为零值按 `tenant.ErrInvalidArgument` 拒绝，已过期按 `ErrExecutionExpired` 拒绝——后者不是参数错误，而是这次 attempt 已经该交给恢复路径。Worker 绝不因此改写持久化的 `execute_deadline_at`。

`Service.Start(ctx, request)` 保持现有行为，由 `Hold` 加 `Open` 组合实现。所有权规则：

- `Hold` 创建租约丢失 watcher，并提供随调用方取消或租约丢失而结束的 Context；从未 Open 的 Hold 没有 Runner 尾写，Close 即使在调用方 Context 已取消也用独立短超时 Context 做 owner-matched 释放。
- 租约的生命周期属于 Hold，不属于调用方 Context：决定「归还还是留给 TTL」的是 `Close`，而调用方取消先一步把租约作废就等于把这个决定变成了掷骰子——从未 Open 的 Hold 会被无谓地锁死一个 TTL。因此调用方取消只结束 `Hold.Context()`，续约继续，直到 `Close` 到达或 `abandonGrace` 用尽为止；后者兜住忘记 `Close` 的调用方，代价与进程消失相同。获取阶段仍然尊重调用方取消：取消发生在 `Acquire` 返回之前时，这个从未交给任何人的租约立即归还，而不是留给 TTL。
- `Open` 校验 `request.Key()` 与 Hold key 完全相同，然后才解析/采用 Pin、取得 Runtime、附加 request ID 和执行 deadline。Pin、Runtime 和可信 `RunContext` 只在 Open 中创建。
- 一个 Hold 可以顺序 Open 多个 Handle，但前一个 Handle 必须先 Close；Handle 只释放本次 Runtime 和执行 Context，Hold 继续拥有 Session 租约。若任一执行被取消或可能存在尾写，Hold 记为不洁，最终不主动释放租约而等待 TTL。
- 不洁是终态：不洁的 Hold 拒绝后续 `Open` 并返回 `ErrHoldUnclean`，调用方只能等 TTL 覆盖尾写后重新持有 Session。执行被取消或超时由 Hold 自己判定；claim 丢失只有 Worker 知道，因此由 Worker 在 `FinishRun` 或任何 attempt-token CAS 返回 stale 时显式调用 `Hold.MarkUnclean()`。该方法幂等、单向、nil 安全，失败方向一律向不洁靠拢。
- `Service.Start` 创建只服务一次执行的 Hold，返回的 Handle 在 Close 时同时关闭该 Hold，因此 HTTP 调用方接口和现有释放行为不变。
- Worker 不得直接调用 Lease Coordinator、Session Directory、Runtime Resolver 或绕过当前 Runtime 的 Session Service。

现有 `Handle` 的“一次执行”、Event Channel 排空、Runtime lease、取消和关闭顺序保持不变。为支持恢复，Handle 额外提供深拷贝的只读 `Transcript` 快照和复制后的 `ToolRefs`，但不暴露可变 Runtime 或原始 Session Service；`Run`、`Resume` 和 `OpenAIHandler` 共享同一个执行槽，三者只能选择一个。

`Transcript` 的契约是 request 级而非 Session 级：它只返回本 Handle 自己那个 request ID 的 Event，供 §6 对账和 Outbox 使用，并且**在无法证明窗口完整时失败关闭**，返回 `ErrTranscriptIncomplete` 而不是一个看起来正常的短快照——把截断误读成「没有最终输出」会让恢复路径重跑一次已经答过的消息。它投影的是已定型的内容：每个 choice 的角色与文本、非文本 part 的计数、tool call 的 id 与名字、tool 结果所属的 call id，以及 `Done`、`IsPartial` 和错误类型，全部深拷贝，调用方改不动 Session。

框架限制（v1.11.2，需在提交材料中如实写明）：`session.Service` 没有提供可移植的截断检测。三个后端的 `defaultSessionEventLimit` 都是 1000，`session.Session` 上没有总数、游标或截断标记，`WithGetSessionEventPage` 在 postgres/mysql 之外返回 `ErrEventPageUnsupported`。因此本实现只能检测**自己请求的窗口**是否可能被削平（取满窗口且窗口内不含本 request 的第一条 Event 即失败关闭），无法检测后端在更早的位置就丢弃过 Event。这一层的正确性依赖三件既有事实：窗口保留的是最新的 Event、Session 租约保证同一时刻只有一个 Worker 在写、同 Session 严格 head blocking 保证不会有第二个 request 在中间插入。

## 6. 中断恢复与 Tool 重放

Redis wakeup 只包含内部 `tenant_id`、`run_id` 和 `traceparent`。Worker 成功 claim PostgreSQL Run 后立即确认 wakeup；因此：

- PEL 只恢复尚未成功 claim 的通知窗口。
- PostgreSQL 扫描器只在 `recover_after` 已过后把 `running` attempt 原子重置为 `accepted`，清除旧 claim 字段、设置退避时间，但保留递增的 attempt 历史；达到上限则终止为 `failed`。
- PostgreSQL 扫描器还选择从未投递或 `last_dispatched_at` 早于阈值、且 `next_attempt_at` 已到期的 `accepted` Run，重复发布 wakeup；发布成功后再更新 `last_dispatched_at`。
- XADD 成功但更新时间失败、Stream 裁剪和重复扫描都只产生重复 wakeup，最终由 `ClaimNextRun` 消解。

wakeup 载荷始终只有引用，代（generation）留在 Store 里：`attempt` 就是代，列出待唤醒时随引用一起返回，记录投递时原样带回。记录是一次 CAS，条件为 Tenant + ID、状态、**期望的 attempt**，以及 `next_attempt_at <= mark_at`；返回值是 CAS 命中的行数，`GREATEST` 是否真的推进了时间戳不影响这个数。四条规则由此确定：

- 任何真正把 Run/part 放回 `accepted` 的路径（Yield、可重试与未知发送结果、恢复扫描）都在同一条 SQL 里清空 `last_dispatched_at`。否则新 attempt 在退避到期后仍被 `StaleAfter` 压着，要多等一整个窗口才会被重新发布。
- 为 attempt N 计算的记录落不到 attempt N+1 上：命中 0 行说明这次唤醒已被更新的代取代，属于正常结果而非错误，调用方按「已被取代」计数，不重试也不补偿——wakeup 已经发出去了，收不回来。
- 退避中的重复受理只发布提示，不刷新时间戳：`next_attempt_at` 未到期时 CAS 不命中，避免把一个还没到期的代标记成刚投递过。
- 记录用的时刻在**发布之后**重新采样，而不是复用扫描开始的时刻。一批发布得比 `StaleAfter` 还慢时，用开始时刻盖章等于当场作废，下一轮会重新选中同一页（排序稳定），页后面的行永远排不上；同时 `GREATEST` 保证一个更旧的并发扫描不会把更新的时间戳改回去。

恢复后的 Worker 在调用 Runner 前必须先对账 Session Event：

- 该 `request_id` 下没有任何 Event：Runner 尚未持久化用户输入，任何 Revision 都按首次执行调用 `Handle.Run(message)`；此路径不构成 Tool 结果未知，但仍受 `max_run_attempts` 约束。
- Session 已有该 `request_id` 的最终输出：不重跑 Agent，以已持久化结果完成 Run 并生成 Outbox。
- 没有最终输出但已有该 request ID 的用户 Event，且 Revision 无 Tool：允许通过 `Handle.Resume` 自动续跑。Resume 使用无 payload 消息且关闭上游 Tool-call resume 选项，不追加第二条用户 Event；自动化测试必须证明恢复后恰好一条用户输入和一条最终回复。若固定上游版本不满足该行为，就退化为 `interrupted_before_output` 失败。
- Revision 的全部 Tool 都显式声明可重放，具有副作用的 Tool 还提供跨 attempt 稳定的业务幂等键：允许自动续跑。
- 其他情况：以 `tool_outcome_unknown` 失败并进入人工/明确错误处理，不自动重跑。

活着的 Worker 因执行 deadline 取消时仍负责排空 Event Channel，并在当前 claim 尚有效时提交明确的 `run_timeout` 或 `tool_outcome_unknown` 终态；只有进程消失或无法完成 CAS 的未知 attempt 才留给 `recover_after` 扫描和下一 attempt 对账。

上游 `tool_call_id` 可能在新的模型 attempt 中改变，只能用于 attempt 内关联，不能单独作为跨 attempt 业务幂等键。

消费 wakeup 时的正常路径规则：

- Session lease busy 时立即确认当前 wakeup，避免 PEL 忙等；事实任务仍在 PostgreSQL。
- 成功持有 Session 的 Worker 在同一 Hold 内按 `accept_sequence` 依次 claim 和执行所有已到期 Run。
- 首次发现无可 claim Run 后关闭 Hold，再立即按同一 Session key 复查一次；若有新 Run 则重新 Hold 并继续。并发受理仍可能落入极窄窗口，因此 stale-accepted 扫描重投保留为最终兜底。

## 7. Outbox 状态机与发送结果

Outbox part 在 Run 终态事务内创建，状态只有：

```text
pending -> sending -> sent
                  \-> pending
                  \-> failed
```

每个 part 使用唯一 `(tenant_id, channel_binding_id, idempotency_key)`，其中 `idempotency_key` 基于稳定 `request_id + part_no`。同时持久化独立的稳定 `client_message_id`、`max_attempts` 和 `next_attempt_at`，每次重试复用同一个 client ID。

`ClaimOutbox` 只 claim 未达到 `max_attempts` 且已到 `next_attempt_at` 的 `pending` part。发送器预先生成唯一 `send_token`；Store 原子递增 attempt、保存该 token、sender 和 deadline。发送结果按以下规则通过 `(status=sending, send_token)` CAS：

| Sender 结果 | 新状态 | 规则 |
| --- | --- | --- |
| success | `sent` | 保存必要的平台消息引用 |
| retryable / rate_limited / auth_expired | `pending` | 保存稳定错误类型和 `next_attempt_at` |
| permanent | `failed` | 不再自动发送 |
| outcome_unknown | `pending` | `duplicate_risk=true`，查询平台状态后或按策略重试 |

发送 deadline 过期也按 `outcome_unknown` 恢复到 `pending`。任何重新入队都采用有上限的指数退避；达到 `max_attempts` 后转 `failed(attempts_exhausted)`。旧 `send_token` 的迟到结果必须失败；调用方只用内部 Outbox ID 记录 stale-result 观测，平台消息引用若要保留只能进入受保护的审计/附加存储，不能写普通日志。Outbox 失败不改变已成功的 Run，更不能重新运行 Agent。

## 8. Store 与扫描接口边界

Store 的公开能力按职责拆分，但 PostgreSQL 实现共享同一个连接池和事务实现：

- Inbox：`Accept`、按 Tenant + ID 读取。
- Run：读取、按 Session 顺序 claim、标记开始、记录 Revision、启动前 Yield、CAS 完成、恢复过期/耗尽 attempt、列出/标记待唤醒 Run。
- Outbox：读取、按到期时间 claim、CAS 完成、恢复超时发送、列出/标记待唤醒 part。

普通读写方法必须带显式 `tenant.TenantContext`。扫描方法属于内部平台 Job，可跨租户返回最小内部引用，但不能返回消息正文、外部事件 ID 或 DeliveryTarget。所有列表都有稳定排序和硬 `limit` 上限；取消 Context 时不继续工作。

「列出待唤醒」返回的是候选：内部引用加它当时的 `attempt`，没有别的字段。「标记已投递」收下这批候选和一个时刻，返回 CAS 命中的行数，调用方用「发布数 − 命中数」得到被取代的数量。到期判断属于 Store：调用方无法在自己那一侧安全地重放 `next_attempt_at` 与时钟的比较，也不该去猜。

InMemory 和 PostgreSQL 必须通过同一 conformance suite，至少验证：

- 三次重复受理只产生一条 Inbox、一个 Run，并返回同一组 ID。
- 两租户相同 Binding/Event 不冲突，按错误 Tenant 读取不可见。
- 同 Session 的 Hold 只检查 `accept_sequence` 最小的非终态 Run：更早 Run 为 running 或退避中时，后续 Run 不可 claim；返回的 request ID 必须来自实际 claim。不同 Session 可同时 claim。
- Run attempt 和调用方提供的 claim token 每次恢复后变化，旧 token 不能记录 Revision、标记开始或完成。
- Run/Outbox attempt 耗尽后终止，后续 Run/part 不被永久阻塞。
- 仅未开始的 Run 可 Yield 并立即由其他 Worker 接手；开始后的未知结果等待宽限和对账。
- Run 与 Outbox 原子完成；stale claim 不产生 Outbox。
- Outbox 四类结果、稳定 client message ID、duplicate risk 和旧 send token 拒绝。
- 扫描器覆盖从未投递、长期 accepted、超过恢复宽限的 running 和 expired sending。
- 每一条 requeue 路径都清掉投递标记，新 attempt 在自己的退避到期时即可被列出，而不是一个 `StaleAfter` 之后。
- 为旧代计算的标记命中 0 行且不改时间戳；未到 `next_attempt_at` 的标记同样命中 0 行；一批候选中只有代正确的那些被计数；更旧的时刻不会覆盖更新的时间戳，但仍算命中。
- `first_execution_started_at` 跨 claim、requeue、恢复、attempt 耗尽和完成始终保留，只有第一次到达 Runner 会写它。
- busy wakeup 被确认且 Session 持有者排空已到期 Run；恢复 Resume 不重复用户 Event。
- Open 后、Runner 持久化前中断的 Run 在下一 attempt 走首次执行，最终恰好产生一条用户 Event。
- 返回值深拷贝；调用方修改 JSON/切片不能污染 Store。
- 非法状态、ID、时间、大小和敏感错误均 fail closed。

## 9. PostgreSQL 迁移约束

Channel Store 使用独立 advisory migration lock，DDL 在一个事务内执行并可并发重复调用。表使用 `channel_inbox_messages`、`channel_agent_runs` 和 `channel_outbox_messages`，避免与上游 Session 表混淆。

每张表都保存 `tenant_id` 并建立以 Tenant 开头的业务索引。Run 的主键为 `(tenant_id, run_id)`，同时保证供 wakeup 定位的 `run_id` 全局唯一；`(tenant_id, inbox_id)` 保证 Inbox 与 Run 一比一。状态、attempt、part、版本和时间字段有 CHECK 约束；所有中途写入和终态 CAS 都在单条 SQL 或同一事务内检查 `status + attempt token`。迁移不存储 Secret 明文，不启用删除级联，不假设控制面和 Channel 表一定在同一 schema。

迁移要面对已经有数据的库：新列以「可空、无默认值」加入（PostgreSQL 11 起是纯目录变更，不重写表），在 CREATE 之外再写一条 `ADD COLUMN IF NOT EXISTS`，因为 `CREATE TABLE IF NOT EXISTS` 对既有表是空操作。能回填的只回填确定的部分——`first_execution_started_at` 只从仍然带着 `execution_started_at` 的在跑行推出——其余保持 NULL 表示「不知道」，不拿 attempt 计数去猜。这类变更不改 `migrationLockKey`：改锁等于让新旧进程各自持锁并发建表。

PostgreSQL 错误包装只保留稳定操作、SQLSTATE 与 ConstraintName，不保留可能包含完整失败行的 `Detail`、SQL 或参数；唯一冲突错误不得泄漏 ExternalEventID、正文或 DeliveryTarget。

PostgreSQL 集成测试继续使用 `TRPC_SERVICE_SESSION_INTEGRATION=1` 门控和已有本地 PostgreSQL，不新增默认触网测试。

## 10. 关闭顺序

进程关闭按所有权从外到内执行：

1. 停止 HTTP/IM Receiver 接收新事件。
2. 停止 Run/Outbox 恢复扫描和新的 wakeup 发布。
3. 停止 Worker 与 Outbox Dispatcher 领取新任务并有界等待活动任务。尚未启动 Runner 的 claim 用 Yield 归还；已经启动的 Runner 被取消后仍必须排空 Event，不能安全裁决的 Run 留给恢复扫描；发送未知结果回到 `pending` 并标记风险。
4. 关闭 Channel Sender/连接。
5. 有界等待结束后关闭 Session Run Coordinator，作为对残留 HTTP/Worker Run 的兜底取消；再关闭 Runtime Resolver。
6. 关闭 StorageBundle Router，最后关闭共享 PostgreSQL/Redis/Session 资源。

任何活动 Worker 都不得晚于 Runtime Resolver 关闭；Runtime Resolver 仍必须早于 Router 和 storage stack 关闭。
