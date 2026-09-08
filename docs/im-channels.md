# IM 接入与社区扩展

企业微信智能机器人和飞书企业自建应用均通过 `channels.TextAdapter` 接入公共文本消费者。当前支持单进程、每类通道一个静态绑定、单聊纯文本和一条最终回复。配置见[本地部署](local-deployment.md)，验证范围见[验收说明](acceptance.md)。群聊、Webhook、媒体、卡片、撤回及生产重试方案见[IM 差异设计](solution.md#55-im-接入差异)。

## 职责与依赖

| 组件 | 职责 | 依赖边界 |
| --- | --- | --- |
| 平台适配器 | 平台认证、事件解析、身份映射、回复目标、字节上限和发送结果分类 | 不直接调用 Runner 或访问 Inbox/Outbox，不依赖公共消费者 |
| 公共文本消费者 | 持久受理、去重、恢复、Session Run、最终文本收集、截断和 Outbox 发送 | 不导入企微/飞书，不解析平台目标载荷 |
| 启动入口 | 静态绑定、凭据授权、依赖注入、连接及消费者生命周期 | 从服务端配置确定 Tenant/App/Binding/Channel |

平台包与公共消费者共同依赖 [`channels`](../trpcservice/channels/adapter.go) 契约，由 [`cmd/trpc-service`](../cmd/trpc-service) 组装。新增通道只需平台包、配置和组装代码。现有凭据授权包间接引用上游 model/event 类型，因此该边界不等同于完全独立于 Agent 框架的 SDK，也不提供不可信插件沙箱。

## 消息转换与持久化

1. 适配器认证平台连接，筛选支持的消息，生成 `InboundEnvelope`。Tenant/App/Binding 来自服务端配置；外部事件只能在该绑定内映射用户和会话。
2. 公共消费者复核 Tenant/App/Binding/Channel，在事务内按 `(tenant_id, channel_binding_id, external_event_id)` 去重并创建 Inbox/Run。重复投递返回原 `request_id`。持久回调成功只表示已落库或已存在。
3. 串行任务循环在 SQL 的 LIMIT、认领和恢复前限定 Tenant/Binding；使用 claim token CAS 保护状态更新。同 Session 忙时在执行预算内等待，已标记开始执行的任务不再 Yield 或重新调用 Runner。
4. [`sessionrun`](../trpcservice/sessionrun/sessionrun.go) 把规范文本转成 `model.NewUserMessage`，调用真实 `runner.Runner.Run`。完整排空 Event，只选择完成、无错误且无 Tool 调用的 assistant 文本；工具结果、推理字段和 runner completion 不作为 IM 回复。
5. 最终文本在写 Outbox 前清洗并按 UTF-8 字节截断，溢出带 `[truncated]`。Run 终态与最多一个 Outbox 原子提交，适配器发送已存正文并分类平台结果。

平台目标是带版本的不透明数据，只由对应适配器解析。SQL/SDK 错误经固定错误边界脱敏；正文、凭据、外部账号和目标 JSON 不进入日志或遥测。

## 两类通道

| 能力 | 企业微信智能机器人 | 飞书企业自建应用 |
| --- | --- | --- |
| 连接 | 固定 `wss://openws.work.weixin.qq.com`，Bot ID/Secret 订阅、心跳与重连 | 官方 Go SDK `v3.11.0` 长连接，App ID/Secret 认证 |
| 身份验证 | 核对消息 Bot ID 与受信任连接一致 | 启动查询 Bot 和企业身份；核对事件 header App/企业与 sender 企业 |
| 持久受理确认 | 无等价入站 ACK，不假定断线后一定补投 | 官方 SDK 在处理器返回后确认；持久受理成功才返回 nil |
| 去重键 | 平台 `msgid` | 平台 `message_id`，不只依赖事件 `event_id` |
| 回复 | 同连接 `aibot_respond_msg`，固定 `stream.id`，一次 `finish=true` | 按原始 `message_id` 调用回复 API，一次最终文本 |
| 成功判据 | 回执显式 `errcode=0` | HTTP 2xx、显式 `code=0` 且平台消息 ID 有效 |
| 文本上限 | 20480 UTF-8 字节 | 保守限制 4096 UTF-8 字节 |
| 目标有效性 | 包含连接 generation，重连后旧目标失败 | 包含内部 Tenant/App/Binding 与外部 App/企业，不随 WebSocket 换代失效 |

企微协议只参考[官方 SDK](https://github.com/WecomTeam/aibot-node-sdk/blob/80615b987ef69c6028ad764924609247c0725955/README.md)，未引入 Node SDK；Go API 示例见[企微包说明](../trpcservice/channels/wecom/README.md)。飞书依赖固定为 `github.com/larksuite/oapi-sdk-go/v3 v3.11.0`，接收用官方 SDK，回复用固定官方地址的私有 HTTP Client，禁止重定向和发送重试。SDK bootstrap 的凭据请求使用同一禁止重定向策略。

## 账号、用户与会话

绑定由服务端静态配置授权；同进程启用的企微与飞书不能复用同一 `(TenantID, BindingID)`，启动时拒绝冲突。凭据分别通过固定保留引用 `env:TRPC_SERVICE_WECOM_BOT_SECRET` 与 `env:TRPC_SERVICE_FEISHU_APP_SECRET` 解析，不能授权给租户模型。跨进程的账号注册唯一性仍由部署约束保证。

令 `H` 为 JSON 字符串数组的 SHA-256 十六进制摘要。企微单聊使用：

```text
Principal = "p-" + H(["im-principal-v1", tenant, binding, "user", external_user])
Session   = "d-" + H(["im-session-v1", tenant, app, binding, "direct", principal, "", "0"])
```

飞书单聊使用：

```text
Principal = "p-" + H(["im-principal-v1", tenant, app, binding, feishu_app, tenant_key, "user", open_id])
Session   = "d-" + H(["im-session-v1", tenant, app, binding, feishu_app, tenant_key, "p2p", principal, chat_id, "0"])
```

框架 AppName 另以 `t/{tenant}/a/{app}` 限定作用域，不包含 Revision；Session Pin 在首轮确定版本。相同外部用户跨租户、应用或绑定不会进入同一框架会话。群聊的共享群/按成员隔离及跨群策略见[Session 命名](architecture.md#54-session-命名)，当前适配器不接收群聊。

## 恢复与平台限制

| 触发条件与影响 | 当前处理和监测 | 生产缓解与残余风险 |
| --- | --- | --- |
| 受理前断线或进程退出导致消息未落库 | 观察连接和持久化失败；未受理消息由用户重试 | 评估平台补投/回放能力；不能保证无限重投或最终送达 |
| 飞书受理超过 2 秒预算 | 最多 16 个回调等待、串行受理；实际受理错误结束服务，停止时排空 SDK 回调 | 按容量配置预算、监控数据库并由进程管理器恢复；平台有限补投可能耗尽 |
| 平台乱序或并发回调 | 仅承诺串行持久受理后的顺序 | 有可信序号时可做有限重排；无法恢复没有序号的严格原始顺序 |
| Agent 启动后中断、状态未知 | `first_execution_started_at` 非空的恢复任务明确失败，不重跑模型或 Tool | 生产需结果对账和业务幂等；不提供已生成但未存答案的重建 |
| 发送失败或结果未知 | 最多一次发送尝试；Unknown 终结并记录 `duplicate_risk`，不重发、不重跑 Agent | 平台查询与幂等能力决定对账方案；不承诺 exactly-once |
| 凭据获取失败、限流、Token 失效 | 飞书每次发送获取 Token；获取失败为 Unknown，明确业务拒绝为 Rejected | 可按平台规则做缓存与限频；当前串行执行不是限频器，Unknown 不区分未发送与可能已发送 |
| 同一飞书 App 多进程或额外订阅事件 | 部署仅运行一个实例，只订阅 `im.message.receive_v1` | 多连接可能分摊消息，未注册事件可能被 SDK 拒绝；生产需账号注册和订阅治理 |

飞书 Token 与回复共用公共消费者的 15 秒发送 Context。其回复 UUID 由完整绑定与原消息派生，平台去重窗口有限；当前只有一个回复分片，未来若拆分必须把分片身份纳入去重键。停止时先取消并等待连接和消费者，再关闭 Runtime、Session、数据库与遥测。

图片/文件、混合消息、卡片和撤回不进入当前执行链路。生产媒体由 Adapter 下载/解密、验证大小和类型、保存受租户保护的附件引用；撤回需关联原消息并保留 Audit，不自动撤销已执行 Tool。限频、回复有效期和跨连接恢复按平台能力核实，详细方案见[IM 差异](solution.md#55-im-接入差异)。

## 社区接入契约

```go
type TextAdapter interface {
    Identity() BindingIdentity
    ReplyTextLimit() int
    Serve(context.Context, AcceptFunc) error
    Send(context.Context, DeliveryTarget, OutboundMessage) DeliveryReport
}
```

- `Identity` 返回已校验的内部绑定，公共消费者独立比较预期身份；其中不携带平台凭据。
- `ReplyTextLimit` 返回 UTF-8 字节上限，公共层在持久化前处理长度；平台发送边界再次校验。
- `Serve` 先认证、筛选和规范化，再调用持久受理回调。回调错误应终止服务并返回原错误，由公共消费者脱敏；支持 ACK 的平台不能提前确认。`Serve` 必须响应取消，在所有已启动回调退出后返回。
- `Send` 校验目标版本和完整绑定，原样发送持久正文或拒绝，不能调用 Agent、改写正文或自行重试。

| 平台结果 | 公共发送结果 | Outbox 终态 |
| --- | --- | --- |
| `Delivered` | `succeeded` | `sent` |
| `DeliveryTargetStale` / `DeliveryRejected` | `permanent` | `failed` |
| `DeliveryUnknown` 或非法结果 | `outcome_unknown` | `failed`，保留重复风险 |

新增 Telegram 等通道时，在独立包使用官方协议库实现四个方法，使用合法小写 `ChannelType` 标识。通过启动代码注入现有 Store、Session Run 和 Revision 检查，无需复制公共消费者、修改数据库结构或增加平台分支。平台特有群聊/媒体能力应有显式契约，不应丢弃附件后把剩余文本送给 Runner。

验证至少覆盖正常收发、重复消息、绑定错配、代表性发送失败和取消；复用 [`text` 集成测试](../trpcservice/channels/text/e2e_integration_test.go) 的公共链路结构。模拟平台测试与真实平台证据分别记录。
