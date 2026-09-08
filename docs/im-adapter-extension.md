# IM 文本适配器扩展

## 本次切片

2026-09-08，基于 `c9cb30d`，本切片已实现并通过验收。用户新增社区扩展目标：让飞书、Telegram 等通道复用已有文本执行链路。交付公共文本消费者、企业微信参考适配器、社区接入契约和本地验证证据；不包含第二个真实 IM 实现。

允许修改 `trpcservice/channels` 内的适配器契约、公共文本消费者、企微适配器及相关测试，`cmd/trpc-service/wecom.go` 启动接线，以及本文、架构与验收索引、企微包使用说明和必要的失效源码链接。冻结 PostgreSQL 表结构与 Store 公共契约、Session Run、Runtime、身份派生规则和现有恢复策略。

不实现飞书或 Telegram，不加入 Dispatcher、Worker、Redis Waker、动态插件、跨会话并发、媒体、卡片、流式增量或新的发送重试策略。现有未提交实验保留在原工作区，本切片只在干净交付工作区推进。

验收条件：

1. 公共文本执行包不依赖企微、飞书或其他平台包；企微生产包不依赖 Session Run、Runtime、数据库驱动或 Telemetry 实现。
2. 一个测试适配器能通过同一公共入口驱动受理与回复，不需要复制企微消费者。新增通道允许增加平台包、配置和组装代码，不需要修改 Store、Session Run 或公共调度分支。
3. 企微原有单聊文本、租户与绑定校验、消息去重、会话顺序、错误分类、观测以及正常取消行为继续成立。
4. 已启动但结果未知的 Agent 不重跑；发送失败不重跑 Agent；企微每条最终回复最多一次发送尝试，旧连接目标失败。
5. 通过全仓默认 race、build、vet，以及使用本地模拟 IM 和真实 PostgreSQL 的既有企微集成测试。真实 Bot 联调是已有历史证据，本切片不自动发送真实消息。

规模边界：以移动现有执行代码为主，净新增不足 3000 行，修改或新增不超过 20 个文件；不改变三个以上既有模块的公开契约。达到仓库暂停阈值时冻结新增范围并报告，不通过扩大抽象解决验收之外的问题。

## 职责与不变量

| 组件 | 负责 | 扩展方约束 |
| --- | --- | --- |
| 平台适配器 | 平台认证、事件解析、规范输入、回复目标、文本字节上限、发送与平台结果分类 | 不能直接调用 Runner 或写 Inbox/Outbox 表；原始消息与凭据不进入公开错误和观测 |
| 公共文本消费者 | 持久受理、去重、任务恢复、Session Run、最终回复收集、文本清洗与截断、Outbox 调度 | 不解析平台目标载荷；保留当前单绑定串行与保守恢复规则 |
| 启动接线 | 静态可信绑定、凭据授权、客户端和消费者生命周期、存储和执行依赖注入 | 绑定来源必须是服务端配置，不能把外部事件的租户声明当成授权 |

受理接口返回成功必须表示持久受理完成，不能只表示消息进入内存队列。HTTP 或飞书事件回调应依据该结果确认平台消息；企微长连接没有等价的入站确认能力，读取到落库之间的既有丢失窗口仍是风险登记。

回复清洗和截断必须在写入 Outbox 前完成，使用适配器提供的字节上限，并受 Store 上限约束；发送方原样发送已存文本或明确拒绝。平台目标作为带版本的不透明数据保存，由所属适配器校验绑定并解析。外部发送结果未知不能被解释成可安全重发，也不能被解释成重新运行 Agent 的理由。

## 接口裁决

Fable 5.1 `max` 会话 `43ae3ca6-dc20-4a88-b5d4-78728a9e9f4a`，实际响应模型 `claude-fable-5-1`，支持本次提取。Opus 5 `max` 会话 `f5b3b770-3d7a-4619-8050-54ee8b2d7e14`，实际响应模型 `claude-opus-5`，完成代码与测试迁移；Leader 完成源码复核、独立验证和文档收口。以下本切片发布阻断约束已落实：

- 构造时比较服务端配置的身份与适配器身份；每条输入落库前再次核对 Tenant、App、Binding、Channel。只调用 Envelope 的租户校验不足以替代其余三项检查。
- 平台发送返回不依赖 Telemetry 的有限结果分类，公共层分别映射为 Store 结果与观测结果；非法结果保守归为未知。
- 适配器运行错误不得原样进入公共消费者返回值。取消与平台内固定错误保留语义，其余错误使用固定描述。

风险登记：当前仍是一次发送尝试、单绑定串行执行；适配器负责按约定的到达顺序提交消息并在退出前结束回调。未来平台的安全重试、并发入站与复杂格式能力需要独立设计，不能从这个文本接口推导出已有保证。取消与目标失效同时发生时，观测可能优先记录取消而非目标失效，不改变不发送的安全结果。

依赖边界说明：企微保留现有 `security.SecretRefAuthorizer` 授权，因此 `security -> tool` 仍会传递部分上游 `model/event` 类型依赖。本切片验证的是不依赖公共执行包、平台 Session Run、Runtime 管理、数据库驱动和 Telemetry 实现，不宣称企微是完全不依赖 Agent 框架类型的独立 SDK。该既有授权包拆分属于可选优化，不扩展本切片。

## 实现与验证状态

已完成。验证在干净交付工作区 `/tmp/trpc-wecom-observability-20260907` 执行，原工作区未提交实验没有纳入验证或交付提交。

本切片涉及 17 个文件；生产 Go 代码净增 431 行（含注释），测试净增 725 行，其余为交付说明。主要执行逻辑由原企微消费者迁移，未增加 Store、Session Run 或 Runtime 的公开契约。

| 实现入口 | 职责与证据 |
| --- | --- |
| [`channels/adapter.go`](../trpcservice/channels/adapter.go) | 四方法 `TextAdapter`、可信绑定、持久受理回调、平台发送报告 |
| [`text/consumer.go`](../trpcservice/channels/text/consumer.go) 与 [`text/reply.go`](../trpcservice/channels/text/reply.go) | 公共受理、Run/Session 执行、最终文本筛选、持久回复与发送调度 |
| [`wecom/adapter.go`](../trpcservice/channels/wecom/adapter.go) | 从已校验 Client 派生身份、转换规范消息、解析回复目标并发送；不导入公共执行包 |
| [`cmd/trpc-service/wecom.go`](../cmd/trpc-service/wecom.go) | `wecom.NewAdapter(client)` 与 `channeltext.New(...)` 的实际组装与既有退出监督 |
| [`text/e2e_integration_test.go`](../trpcservice/channels/text/e2e_integration_test.go) | `fake` 通道通过公共 `Consumer.Run`，使用真实 PostgreSQL Store、Session 与 Runner；验证回调返回时已持久化、正常回复、重复事件不新增执行，以及发送拒绝不重跑 Agent |
| [`text/telemetry_test.go`](../trpcservice/channels/text/telemetry_test.go) | 逐条身份拒绝、附件拒绝、持久 request_id、观测脱敏与导出失败不改变业务 |

Leader 验证结果：

| 检查 | 结果 |
| --- | --- |
| `TRPC_SERVICE_MODEL_INTEGRATION=0 TRPC_SERVICE_SESSION_INTEGRATION=0 go test -race -count=1 -timeout 900s ./...` | 全仓通过，退出码 0 |
| `go vet ./...`、`go build ./...` | 均通过，退出码 0 |
| 下方 PostgreSQL 集成命令 | 三包通过：PostgreSQL 4.072s、企微 10.920s、公共文本 3.834s；包含原有 5 组企微链路、2 组企微观测、2 组替代适配器集成用例 |
| `go list -deps` 依赖检查 | 企微没有公共文本执行、平台 Session Run、Session 后端、数据库驱动或 Telemetry 实现依赖；公共文本执行不依赖企微协议包 |

```sh
TRPC_SERVICE_MODEL_INTEGRATION=0 \
TRPC_SERVICE_SESSION_INTEGRATION=1 \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
go test -race -count=1 -timeout 300s \
  ./trpcservice/channels/postgres \
  ./trpcservice/channels/wecom \
  ./trpcservice/channels/text
```

Leader 裁决：发布阻断约束的代码与测试证据通过。身份错配保留在默认单元测试中覆盖全部四个字段，不重复扩展 E2E 矩阵；受理拒绝继续记录原有 `failed/permanent`，不改成新的观测分类。非阻断的平台能力、授权包间接依赖和取消时观测优先级按上述风险边界保留。最终仅校正接口注释与交付说明，不再扩展行为。

本次没有重新验证真实 Bot、发送真实消息或重启现有服务。2026-09-07 的真实企微正常收发仅是历史证据；当前重构代码的证据是本地模拟平台与真实 PostgreSQL/Runner 集成。

## 社区接入契约

公共入口见 [`channels/adapter.go`](../trpcservice/channels/adapter.go)，文本执行见 [`channels/text`](../trpcservice/channels/text)。适配器只依赖前者，不需要导入文本执行包或上游 Runner。

```go
type TextAdapter interface {
    Identity() BindingIdentity
    ReplyTextLimit() int
    Serve(context.Context, AcceptFunc) error
    Send(context.Context, DeliveryTarget, OutboundMessage) DeliveryReport
}
```

`BindingIdentity` 只有内部 Tenant/App/Binding/Channel，不携带 Bot ID、Token 或 Secret。适配器的身份必须来自已经校验的平台客户端配置；公共消费者另持有启动配置中的预期身份并比较两者。外部消息即使填写格式正确的其他租户或应用，也不能改变这个身份。

`ReplyTextLimit` 的单位是 UTF-8 字节。公共层在构造时验证限制，并将上限约束在平台消息契约允许的范围内。企微使用 20480 字节；以字符或编码后大小限额的平台应提供保守字节上限，并在自己的协议发送边界再次验证。当前接口只生成一条最终纯文本；富文本转义、卡片和分片需要后续独立能力设计。

`Serve` 是适配器到平台的持久受理入口。适配器先认证、解析和筛选支持的消息类型，生成规范输入和版本化回复目标，再调用 `AcceptFunc`。对有入站确认的平台，只能在回调成功后确认；成功包含首次落库和已持久化的重复消息，不保证 Agent 推理或外部回复一定成功。回调错误应结束当前服务；适配器可以依其平台协议返回非成功确认以等待平台重投，但不能把失败当成内存受理成功。

当前要求适配器按其声明的消息顺序提交。`Serve` 必须响应取消，并在已启动的受理回调全部结束后返回。客户端连接由启动入口拥有；公共消费者等待受理与任务循环都退出后，启动入口才能关闭 Runtime、数据库和观测资源。该约定不新增运行池或多副本所有权协议。

`Send` 只处理一个已持久化的回复，必须按记录的文本发送或明确拒绝；不自行调用 Agent、重写回复正文或增加第二次发送。它负责校验目标版本、平台与绑定，并将 SDK 结果映射为以下类型。未知或非法结果不能当成成功。

| 适配器结果 | 持久化结果 | 观测结果 |
| --- | --- | --- |
| `Delivered` | `succeeded`，可带平台消息 ID | `succeeded` |
| `DeliveryTargetStale` | `permanent` | `stale_target` |
| `DeliveryRejected` | `permanent` | `rejected` |
| `DeliveryUnknown` 或非法结果 | `outcome_unknown`，保留重复风险 | `unknown` |

接口只表达当前保守发送策略；有明确幂等能力的平台仍不能在此切片自行启用重试。平台账号凭据由各自的启动配置与授权器解析，不通过公共身份或日志传递。编译期接入的社区代码仍是受信任的服务代码，这些契约不是恶意插件的进程沙箱。

## 新增通道步骤

1. 新建平台适配器包，复用官方协议 SDK，定义服务端绑定和凭据校验。使用稳定的 `ChannelType` 小写标识；它是开放的合法标识，不需要修改公共枚举或数据库。
2. 实现四个方法。将外部账号、用户、群或话题映射为可信绑定下的内部 Principal/Session；回复目标由适配器编码并保留版本。平台不支持的消息在协议边界明确拒绝，不能丢弃附件后把剩余文本交给 Runner。
3. 在启动入口构造平台客户端、适配器和公共消费者，并注入现有 Store、Session Run 和 Revision 检查。连接与消费者共用取消关系，退出时等待两者完成。无需修改公共任务调度、SQL 表结构或 Session Run。
4. 提交协议测试、平台限制说明和公共链路验证：正常收发、重复消息、绑定错配、代表性发送失败与取消。测试适配器的运行证据与真实平台联调证据分别记录。

原 `wecom.NewConsumer` 的内部调用方已迁移到 `channeltext.New(channeltext.Config{...})`。`Consumer.Accept` 是校验并持久受理单条规范输入的公共操作；常规适配器通过 `Serve` 收到的回调使用它。旧企微切片与观测切片中的文件位置描述的是当时的提交；本指南和当前架构/验收索引是提取后的入口。
