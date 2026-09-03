# 多后端、数据同步与幂等设计

## 1. 统一后端路由

平台不重新定义 tRPC-Agent-Go 的 Session 接口，而是在其上增加租户级 Backend Profile、路由和生命周期管理。当前实现见 `trpcservice/storagebundle`，它已经把“Revision 选择什么存储”和“Runtime 如何持有存储”从进程启动配置中分离出来。

### 1.1 当前实现

```go
type Profile struct {
    TenantID string
    ID       string // ID 就是不可变版本
    Session  SessionSpec
}

type Bundle struct {
    Session session.Service
}
```

`Profile` 只保存 `env:VAR_NAME` 形式的连接引用与命名空间参数，不保存 DSN、URL 或密钥明文。`ProfileRepository` 在 InMemory 与 PostgreSQL 两种控制面下提供相同的 Create/Get/List 契约：Profile ID 就是版本，不允许覆盖或删除；每租户最多 32 个；写入保存 SHA-256 fingerprint，读取时重新计算并核验，内容或租户身份被库外修改时 fail closed。生产进程让 Admin API 与 Router 共用同一个 Repository，因此控制面刚创建的 Profile 立即能被数据面解析。

`Router` 以 `(tenant_id, profile_id)` 为键，通过 singleflight 懒构建并缓存 `Bundle`；每次命中都重新解析 Profile 并比对 fingerprint，同一 ID 下内容变化时返回 `ErrProfileChanged`，不静默重建。空 `BackendProfileID` 才表示使用进程默认 Bundle；非空 ID 必须在当前租户中真实存在，并在创建 Revision 和发布时提前检查。

空 `BackendProfileID` 只在 Router 内解释为进程默认 Bundle。默认 Bundle 仍由 `storageStack` 所有，Router 不关闭它，但默认与动态 Bundle 的租约都会计入活跃引用。Runtime 从构建成功起一直持有租约，关闭顺序固定为 `OpenAI Adapter -> Runner -> Bundle lease`；进程关闭顺序固定为 `RuntimeResolver -> Storage Router -> storageStack`。Router 关闭后拒绝新解析，等待构建和全部租约结束，再逆序关闭自己构建的动态 Bundle。

Runtime 在接触 Profile Source 或 Factory 之前依次完成发布态、身份、`config_digest`、租户 entitlement、Tool/Policy 和模型配置检查。因此未授权、摘要被篡改或模型无效的 Revision 不会触发存储解析或连接。Factory 还继承进程级约束：非持久 Pin 不能搭配持久 Session，多 Worker 不能搭配 InMemory Session。

### 1.2 动态 Factory 与安全边界

Factory 支持 InMemory、PostgreSQL 和 Redis 三种 Session 后端，顺序固定为：Profile 形状与进程约束 → 逐个 SecretRef 的租户 entitlement → 环境解析 → 有界 probe → 上游 Session constructor。未授权引用不会读取环境；解析后的连接值只存在于构建路径，不写回 Profile 或 Bundle。错误返回前先整体替换 DSN/URL，再用 `sessionbackend.Scrub` 处理驱动改写的密码片段；发生替换就切断原错误链，避免调用方通过 unwrap 取回凭据。

一次动态构建默认最多等待 15 秒。上游 constructor 不接收 Context，因此超时只能让 Router 停止等待，不能强杀正在执行的 goroutine；迟到的 Service 会由该 goroutine 自行关闭。PostgreSQL probe 还会按“目标数据库 + schema + table prefix”获取 session-level advisory lock，直到 constructor 真正返回才释放，防止两个 Worker 首次并发执行上游 `CREATE TABLE IF NOT EXISTS`。Redis 不创建 schema，只做连通性 probe。

Factory 继承进程级不变量：非持久 Pin 不能搭配持久 Session，多 Worker 不能搭配 InMemory Session；任何不安全组合都明确拒绝，不回退默认后端。

### 1.3 当前边界与扩展方向

当前 `Bundle` 只有已经被 Runtime 消费的 `Session` 字段，不为 Memory、Knowledge、Artifact 预留无语义的 nil 字段；以后有真实消费者时按字段名扩展。Router 目前不做 TTL/LRU：成功构建的动态 Bundle 一直存活到进程关闭。每租户 32 个 Profile 的硬上限使资源占用有界，但后续若允许更多 Profile，必须同时引入连接预算和可证明不影响活跃 Runtime 的淘汰协议。

Revision 只记录 `BackendProfileID`，不复制 Profile fingerprint。不可变 Repository、存储行 fingerprint 与 Router 进程内比对共同覆盖正常操作和意外漂移；fingerprint 不是认证器，一个能同时越过 Repository 修改 `spec` 并重算 fingerprint 的数据库写入方仍能在全进程重启后绕过它。防御这种威胁应使用数据库写权限隔离或签名配置，而不是在发布时修改 Revision 的不可变 Config/digest。

完整目标仍包括能力矩阵：

```go
type BackendCapabilities struct {
    Durable           bool
    SharedAcrossNodes bool
    AtomicStateEvent  bool
    OptimisticCAS     bool
    TTL               bool
    Pagination        bool
    VectorFilter      bool
}
```

能力矩阵用于在配置发布前检查 Agent 所需能力与后端能力是否匹配。例如生产多节点 Agent 不能选择 InMemory Session；要求严格同会话并发的租户不能使用缺少必要协调能力的组合。该矩阵尚未实现，不能把 Factory 当前的两条进程约束等同于完整能力协商。

## 2. 数据放置

| 数据 | 推荐后端 | 一致性要求 | 原因 |
| --- | --- | --- | --- |
| Tenant、Agent、Revision、Binding、Policy | PostgreSQL | 强一致 | 管理配置和发布指针需要事务与审计 |
| Inbox、Run、Outbox、Audit | PostgreSQL | 强一致/追加写 | 需要唯一约束、状态机、可追溯重试 |
| Session Event/State | Redis 或 PostgreSQL | 同 Session 强顺序 | 高频上下文读写；按租户延迟和成本选择 |
| Summary | 与 Session 同后端或 SQL | 最终一致 | 可从 Event 重建，不能反向覆盖新 Event |
| Memory | PostgreSQL/PGVector、Redis 或外部 Memory | 写后最终可见 | 检索型派生数据，允许短暂索引延迟 |
| Knowledge 元数据和源文档 | PostgreSQL + Object Storage | 元数据强一致 | 源文档是重建索引的事实来源 |
| Knowledge Chunk/Embedding | PGVector/Qdrant/Milvus 等 | 最终一致 | 可按索引版本重建和切换 |
| Artifact | S3-compatible Storage | 写入后可读 | 适合大对象、生命周期和独立扩容 |
| 限流、幂等快路径、Session 租约 | Redis | 短期强原子 | Lua/SET NX、低延迟、天然 TTL |

本地模式允许 InMemory/SQLite/本地文件，但能力矩阵必须标记 `SharedAcrossNodes=false`，启动生产角色时拒绝不安全组合。

## 3. 后端取舍

| 后端 | 优点 | 限制 | 推荐用途 |
| --- | --- | --- | --- |
| InMemory | 零依赖、延迟低 | 重启丢失、不可跨节点 | 单元测试和本地演示 |
| Redis | 低延迟、TTL、原子命令 | 内存成本高，复杂查询弱 | 热 Session、锁、幂等、限流 |
| PostgreSQL/MySQL | 事务、唯一约束、查询和审计能力强 | 写延迟高于 Redis，需维护索引 | 配置、持久 Session、Run、Audit |
| SQLite | 部署简单 | 多节点和高并发写受限 | 本地单节点 |
| PGVector | 与 SQL 共用运维体系、事务边界清楚 | 超大规模向量性能有限 | 参赛生产参考实现 |
| Qdrant/Milvus | 专用向量检索和水平扩展 | 新增运维组件，元数据事务分离 | 大规模独立向量场景 |
| S3/COS | 低成本、高耐久、大对象友好 | 不适合频繁小状态更新 | Artifact 和知识源文件 |

所谓“统一接口”不意味着“统一语义”。平台必须把一致性、TTL、分页、过滤和事务能力暴露给配置校验与运维页面。

## 4. 同一 Session 并发

### 4.1 默认规则

同一 Session 同时只运行一个 Run，不同 Session 并行。租约的作用域是完整的 `{tenant, app, principal, session, epoch}`，与 Session Directory 的 Pin 同一把键；落到 Redis 上是它的定长 SHA-256 摘要，按字段长度前缀计算，因此租户、主体和 Session ID 都不进入 keyspace，也无法靠移动字段边界撞成同一把锁：

```text
<prefix>:{sha256(len-prefixed tenant|app|principal|session|epoch)}:lock    owner token，带 PX 过期
<prefix>:{sha256(...)}:fence                                              只增不减的计数器
```

Run Coordinator 使用 Redis Lua 脚本完成：

1. `SET key owner_token NX PX lease_ttl` 获取租约。
2. 获取租约时同时 `INCR` Session fence，得到单调 token。
3. Worker 定期比较 owner token 后续约；续约的"未知"结果一律按失败处理，重试到安全边界后判定失去租约。
4. 失去租约立刻取消 Run Context，并停止后续 Tool 与存储操作。
5. 释放时只允许 owner token 匹配者删除锁，并且从不删除 fence 计数器。
6. Run 结束或进程关闭时**不**删除锁，留给 TTL 过期，覆盖被取消 Runner 的收尾写入。

**已实现的是第 1-6 步，并且它是一把合作型租约。**它把并发写者挡在 Run 入口：第二个 Worker 收到 `409 session_busy`，持有者失效后按 TTL 接管。实现见 [Session Run Lease](session-lease.md)。

**没有实现、并且在当前上游接口下做不出来的是用 fencing token 做写入准入。** 上游 `session.Service.AppendEvent` 没有 fence 或 CAS 参数，PostgreSQL/Redis Session 模块的 `WithAppendEventHook` 在写入之前执行、两步之间没有屏障，不是原子的。因此"Session 装饰器把 token 传给 Backend、Backend 原子拒绝落后写入"无法在这一层补出来，**不能宣称过期 Worker 的写入被原子拒绝**，token 目前只是观测句柄。被暂停或分区后在 TTL 内恢复的持有者、以及上游 Runner 取消后仍通过 `context.WithoutCancel` 写约一秒终态 Event 的行为，都不被阻止；取消是尽力而为且最终一致的。

真正的单写者语义需要存储层的条件写（Redis Lua 比对 token 后再写，或 PostgreSQL 带版本号的条件 UPDATE），这超出上游 Session 接口的能力，必须由平台自建，本切片不做。等待队列同样未实现：被拒绝的 Worker 不排队。

单实例 Redis 是已验证的部署形态。failover 下锁 key 可能随未同步的副本丢失、fence 可能回退，因此**不宣称** failover 下仍然互斥或 fence 仍然单调。无法接入 fencing/CAS 的上游后端不能宣称网络分区下的线性一致。这一限制必须在 Backend Capabilities 中显式展示。

### 4.2 Tool 副作用

Session 串行不能防止 Worker 在 Tool 成功后崩溃。每个具有副作用的 Tool 必须接收：

```text
idempotency_key = request_id + tool_call_id
```

Tool Adapter 在业务系统侧保存该键与结果。相同键再次调用时返回原结果。无法支持幂等的危险 Tool 不自动重放，Run 进入 `manual_review`。

## 5. Event、State、Summary 和 Memory 顺序

### 5.1 Event 与 State

Runner 是一次 Invocation 内的事件顺序来源。Worker 只使用一个事件消费者，按收到顺序处理并持久化。`StateDelta` 必须通过 Session Backend 的 `AppendEvent` 语义与对应 Event 一起提交；不能先更新 State 再单独追加 Event。

PostgreSQL 后端使用事务同时更新 Session State 和插入 Event。Redis 后端使用原子操作维护 Session 数据。平台装饰器负责租户键校验、Telemetry 和审计，不绕开上游 Service 直接拼接后端命令。**装饰器不做租约检查**：上游 `AppendEvent` 没有 fence/CAS 入口，`WithAppendEventHook` 与写入之间不是原子的，在这里"检查租约"只会产生一层看着像准入控制、实际拦不住任何东西的代码（见 [§4.1](#41-默认规则)）。

### 5.2 Summary

Summary 在 Event 提交后异步生成，并携带：

```text
session_id + filter_key + source_end_sequence + summary_version
```

写入时只允许覆盖相同或更旧的 `source_end_sequence`。Summary 失败不回滚事实 Event；下一次任务可重试。读取时若 Summary 落后，Runner 仍可使用原始 Event，只是 token 成本增加。

### 5.3 Memory

Memory 从已完成 Session/Run 的稳定 Event 中异步提取。Memory 写入 Outbox 使用 `(tenant_id, source_event_id, extractor_version)` 作为幂等键。成功提交后发布失效通知；其他 Worker 的 Memory 查询缓存使用短 TTL，因此在通知丢失时仍会最终看到新记忆。

强制“下一请求必须看见上一请求的新 Memory”的 Agent，可以在 Run 完成前同步等待 Memory Upsert；默认模式采用最终一致以降低响应延迟。

## 6. IM 消息幂等

### 6.1 入站

幂等键优先使用 IM 平台事件 ID：

```text
inbound:{channel_binding_id}:{external_event_id}
```

处理分两层：

1. Redis `SET NX` 是低延迟快路径，短 TTL 状态为 `processing`，完成后延长保留期。
2. PostgreSQL `UNIQUE (tenant_id, channel_binding_id, external_event_id)` 是持久正确性保证。

重复请求返回原 `request_id` 和已受理状态，不创建新 Run。没有稳定事件 ID 的平台，使用平台建议字段组成规范字符串后计算摘要，并记录碰撞与误判风险。

首次 Inbox 提交后，将 `inbox_id + traceparent` 投递到 Redis Streams。Consumer Group 提供 Worker 分发和故障认领；定时扫描 PostgreSQL 非终态 Inbox 负责修复“数据库已提交但 Stream 未投递”的窗口。

### 6.2 出站

回复先写 Outbox，再调用 IM API：

```text
idempotency_key = request_id + reply_part_no
```

发送器处理平台长度限制、频率限制、媒体上传和指数退避。若外部 API 不提供幂等能力，“发送成功但状态未落库”时可能重复发送；平台记录客户端消息 ID并尽量查询投递状态，不能虚假承诺 exactly-once。

## 7. 后端迁移

迁移使用统一状态机：

```text
planned → snapshotting → syncing → verifying → cutover → draining → completed
                               ↘ failed / rolled_back
```

### 7.1 Session：Redis 到 SQL

1. 新建目标 Backend Profile，验证连通性和能力。
2. 新 Session 先切到目标后端；存量 Session 按批次迁移。
3. 对单个 Session 获取运行租约并标记 `migrating`。
4. 从源后端读取 State、Event 和 Summary 快照，按稳定 Event ID 幂等写入目标。
5. 比较 Event 数量、摘要和抽样内容校验和。
6. 原子更新 Session 目录中的 `backend_profile_id`。
7. 进入观察期，读取失败可回退源后端；确认后再按保留策略清理源数据。

迁移以 Session 为单位，不全局停机。实现在线双写前，短暂冻结一个 Session 比跨后端分布式事务更可靠，也更符合参赛工期。

### 7.2 向量库迁移

向量不直接搬运，因为 Embedding 模型、维度、距离算法和元数据过滤能力可能不同：

1. 从 Object Storage/数据库读取原始知识文档。
2. 使用固定解析器和目标 Embedding 配置生成新索引版本。
3. 新旧索引并存，执行文档数、chunk 数和固定查询集对比。
4. 原子切换 Knowledge Base 的 `active_index_version`。
5. 观察期内保留旧索引，失败直接回滚指针。

### 7.3 Migration Adapter

各后端实现 `Export/Import/Verify` 能力。迁移记录保存游标、批次和校验结果，任务重启后从已提交游标继续，不能依赖进程内进度。

## 8. 故障与降级

| 故障 | 行为 |
| --- | --- |
| Redis 协调不可用 | 停止新有状态 Run；不能绕过锁并发写 Session |
| Session Backend 短暂不可用 | 有界重试后失败，Inbox 保留；不能切换 InMemory |
| Memory/Vector 暂时不可用 | 根据 Agent 策略无 Memory/Knowledge 降级，并记录明显的 Trace 与指标 |
| Object Storage 不可用 | 文本请求可继续；需要 Artifact 的操作 fail closed，避免丢文件 |
| PostgreSQL 配置不可用 | 已缓存且未过安全期限的不可变 Revision 可继续短时运行；发布和新绑定解析停止 |
| 后端写入结果未知 | 使用稳定 Event/Outbox ID 重试，禁止生成新的业务 ID |

## 9. 最小验证场景

1. 两个租户使用不同 Session Backend，使用相同外部用户名和 Session ID，数据仍完全隔离。
2. 两个 Worker 同时收到同一 Session 消息，最终按顺序执行且后者能读取前者 Event。
3. 同一个企业微信事件投递三次，只产生一个 Run 和一个业务 Tool 副作用。
4. Memory 写入后，另一 Worker 在缓存 TTL 内或收到通知后可以检索。
5. 一个 Session 从 Redis 迁移到 PostgreSQL，迁移前后事件数和回答上下文一致。
6. 知识库从本地向量索引重建到 PGVector，固定查询集结果达到约定阈值并可回滚。
