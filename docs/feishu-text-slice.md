# 飞书单聊文本接入

## 切片边界

2026-09-08，基于 `b26a1f2`。目标是将一个飞书企业自建应用机器人通过官方 Go SDK 长连接接入现有 `channels.TextAdapter` 与公共文本消费者，形成单聊文本到真实 Runner 再到最终回复的链路。用户已经在本地凭据文件填写 App ID 和 App Secret；验证过程不输出凭据或用户消息。

允许修改新增 `trpcservice/channels/feishu` 包及测试、`cmd/trpc-service` 的飞书配置和启动接线、必要依赖、演示启动入口和相关文档。公共 `channels.TextAdapter`、`channels/text`、Store、Session Run、Runtime 与数据库结构保持现有契约。

不实现群聊、卡片、媒体、流式增量、主动消息、Webhook、多绑定管理、发送重试、分布式唤醒或新的调度机制。不复制公共消费者。当前网页和企微进程不在本切片中重启。

验收条件：

1. 飞书适配器只导入平台契约、凭据授权与官方 SDK，不导入公共执行消费者或数据库实现。
2. 启动配置默认为关闭；凭据只通过固定保留 Secret 引用授权给静态 Tenant/App/Binding。校验飞书应用和企业身份，消息不能自行选择内部租户。
3. 官方事件回调成功只发生在公共持久受理完成之后；重复消息由现有 Store 去重。仅接受用户发给机器人的单聊纯文本，其他消息按明确范围处理。
4. 规范 Principal/Session/回复目标带租户、绑定与平台作用域；发送一次最终纯文本，确认平台成功后记录 sent，未知结果不重发也不重跑 Agent。
5. 本地协议、真实 Runner/PostgreSQL 集成、全仓默认 race、build、vet 通过。真实凭据验证、长连接成功和真实消息收发分别记录，不相互替代。

控制在 20 个文件与净新增 3000 行以内，优先实现和测试首条链路；发现公共契约必须改变、既有保证需要撤销或达到仓库强制暂停阈值时停止扩展并报告。

## 实施状态

凭据已保存且非空，文件权限为 `600`。2026-09-08 用户补充权限后再次运行只读预检：应用换取 Token、机器人身份查询、企业身份查询均返回 HTTP 200 / code 0，三项校验通过，脚本退出 0。此前企业查询的 code 99991672 / `tenant:tenant:readonly` 权限缺口已解决。该结果只证明凭据有效、机器人和企业身份可读取，不证明长连接在线或消息收发成功。

Fable 5.1 初期因上游 HTTP 503 不可用，用户授权本切片临时由 Astra 架构/最终审查、Opus 5 `max` 实现。服务恢复后已恢复原分工，完成 Fable 5.1 `max` 定向审查；实际回复标识分别核实为 `claude-opus-5` 与 `claude-fable-5-1`。Opus 实现、Leader 三项阻断修复复核和本地最终验证已完成，长期协作约定不变。

预检脚本已通过语法检查、缺失文件固定错误检查和真实身份查询。2026-09-08 15:59（Asia/Shanghai），从已验收提交 `f6bb709` 构建独立进程，启用真实模型配置、`feishu_live_demo` schema 与 `127.0.0.1:18082`。身份校验完成，SDK 已记录固定 `channel feishu connected`，HTTP 健康检查返回 200；原网页 `18080` 与企微 `18081` 未重启且健康检查均返回 200。只查询聚合计数确认飞书 Inbox/Run/Outbox 均为 0，未输出消息、外部身份或凭据。

因此凭据身份预检、本地代码验收和真实长连接建立已有证据；事件订阅保存/发布及用户真实单聊收发仍待验证。长连接在线不能替代后一项，也不证明持续在线或消息最终送达。

## 本地验收证据

2026-09-08，Leader 在干净交付检出对最终代码执行以下检查，全部退出 0：

```bash
TRPC_SERVICE_MODEL_INTEGRATION=0 TRPC_SERVICE_SESSION_INTEGRATION=0 \
  go test -race -count=1 -timeout 900s ./...
go vet ./...
go build ./...
TRPC_SERVICE_MODEL_INTEGRATION=0 TRPC_SERVICE_SESSION_INTEGRATION=1 \
  TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
  go test -race -count=1 -timeout 300s ./trpcservice/channels/postgres \
  ./trpcservice/channels/wecom ./trpcservice/channels/text ./trpcservice/channels/feishu
node --check scripts/start-local.mjs
git diff --check
```

飞书[协议测试](../trpcservice/channels/feishu/adapter_test.go)使用真实 SDK 与本地 WebSocket，覆盖持久受理后 ACK、失败停止并排空、身份过滤、错误回复和禁止重定向。[PostgreSQL 集成](../trpcservice/channels/feishu/e2e_integration_test.go)连通公共消费者、真实 Runner/Session 和持久 Store，重复投递同一消息后得到一个成功 Run、一次执行和一个 sent 回复。模型使用确定性 echo；该证据不证明真实飞书平台收发或外部模型调用。

## 本次实现决策

- **发布阻断约束**：启动时用固定 App 凭据取得机器人及企业身份，静态绑定内部 Tenant/App/Binding；接收事件核对 header App/企业与 sender 企业。Principal、Session 和回复目标均带完整绑定作用域。同进程启用的企微和飞书不能在同一租户复用 Binding ID，避免两个消费者扫描相同任务作用域；仅在启动配置拒绝，不改变 Store 契约。
- **发布阻断约束**：官方 SDK 长连接回调同步调用持久受理，设置约 2 秒预算、有限并发等待和串行受理；只有持久受理成功才返回 nil。首次受理失败终止接收，保留原始受理错误给公共消费者脱敏。停止时等待 SDK 回调全部退出，不在回调中等待其自身任务组。
- **发布阻断约束**：最终回复使用私有标准 HTTP 请求，固定官方域名、禁用重定向、超时和无发送重试，避免 SDK Reply 内部重发。JSON 明确包含成功 code 且消息 ID 非空才确认 Delivered；不确定结果按 Unknown 终结，不重跑 Agent。
- **发布阻断约束**：SDK 注入静默 Logger；外部错误仅用固定错误类别对外呈现。正文按保守 4096 UTF-8 字节限制在公共消费者持久化前处理。
- **风险登记**：SDK 并发回调仅保证本实现串行受理后的持久顺序，不承诺平台原始时间顺序；终止时失败 ACK 可能来不及发出，平台有限重投不能当作无限恢复保证。每次回复单独获取 Token 增加一次认证请求，当前演示不增加 Token 缓存和并发刷新机制。

Opus 可基于源码或测试反驳这些决策；若需改变公共契约或扩大冻结范围，停止该部分修改并交回 Leader。

## 本地配置预检

应用凭据位于被 Git 忽略的 `data/feishu.env`，文件只需两个字段：

```dotenv
TRPC_SERVICE_FEISHU_APP_ID=
TRPC_SERVICE_FEISHU_APP_SECRET=
```

配置后运行以下命令（Node.js >= 22）：

```bash
node scripts/check-feishu.mjs data/feishu.env
```

脚本直接按 dotenv 数据解析指定文件，不加载 shell，不修改进程配置。请求限定到飞书官方地址，拒绝重定向，每次请求超时 15 秒；Token 仅用于内存中的只读查询。不建立长连接、不发送消息、不打印凭据、外部账号、企业标识或上游错误正文。输出只包含 HTTP 状态、数值业务码、布尔检查结果和固定权限名；三项检查有任何一项未通过即退出 1。

这不是消息权限检查，也不是机器人就绪探针。应用发布、事件订阅和实际文本收发需要分别验证。

## 启动配置

使用 [本地部署说明](local-deployment.md) 中的 PostgreSQL 单进程配置。`deploy/local.env.example` 中飞书默认关闭，开启需要：

```dotenv
TRPC_SERVICE_STORAGE_PROFILE=postgres
TRPC_SERVICE_SESSION_COORDINATION=inmemory
TRPC_SERVICE_FEISHU_ENABLED=true
TRPC_SERVICE_FEISHU_TENANT_ID=demo
TRPC_SERVICE_FEISHU_AGENT_APP_ID=echo
TRPC_SERVICE_FEISHU_BINDING_ID=feishu-local
```

`AGENT_APP_ID` 是服务内部的应用，`APP_ID` 是飞书开发者后台的应用，不能混用。自定义内部应用须先发布 Revision，并使用当前进程默认的持久 Session/Pin。首次演示可使用预置 `demo/echo`；真实模型配置与发布见 README。

凭据和运行配置可分别放在 `data/feishu.env` 与 `data/local.env`，从干净检出构建后启动：

```bash
./build.sh
node scripts/start-local.mjs data/feishu.env data/local.env
```

文件按参数顺序读取，已有进程环境优先，其次是较早文件的值，包括空值；凭据文件应放在带空占位字段的配置模板之前。配置文件不是 shell 脚本，不执行其中的命令。单个检出使用一套启动 PID 和日志，已有网页或企微运行时应使用独立检出与空闲 loopback 端口，避免覆盖运行记录。

同一飞书 App 只运行一个本实现实例。多个连接可能分摊事件，当前绑定没有跨进程注册唯一性。HTTP 健康只表示服务就绪，日志中的固定 `channel feishu connected` 表示 SDK 长连接曾建立；事件订阅与实际回复仍须单独验证。

## 飞书后台配置

| 设置 | 本切片要求 |
| --- | --- |
| 应用能力 | 已开启机器人 |
| 单聊接收权限 | `im:message.p2p_msg:readonly` |
| 机器人发送权限 | `im:message:send_as_bot` |
| 企业身份校验 | `tenant:tenant:readonly`，用于本方案额外的企业查询，并非消息收发 API 的必需权限 |
| 应用身份事件 | 接收消息 v2.0：`im.message.receive_v1` |
| 订阅方式 | 使用长连接接收事件；保存时必须已有 SDK 客户端在线 |
| 应用可用范围 | 包含实际测试用户 |

先开启能力和权限。待本地客户端上线后保存长连接方式、添加上述事件，再创建版本并发布；普通企业自建应用需企业管理员审核生效。权限获批不代表新增事件配置已发布。官方测试版应用有自动生效的例外。本切片不需要群消息、通讯录、用户 OAuth 或卡片回调权限，也不需要公网 Webhook URL。

2026-09-08 核对官方资料：[接收消息](https://open.feishu.cn/document/server-docs/im-v1/message/events/receive)、[回复消息](https://open.feishu.cn/document/server-docs/im-v1/message/reply)、[长连接配置](https://open.feishu.cn/document/server-docs/event-subscription-guide/event-subscription-configure-/request-url-configuration-case)、[添加事件与发布](https://open.feishu.cn/document/ukTMukTMukTM/uYDNxYjL2QTM24iN0EjN/event-subscription-configure-/subscription-event-case)、[应用可用范围](https://open.feishu.cn/document/home/introduction-to-scope-and-authorization/availability)。

## 审查与风险

依赖固定为官方 `github.com/larksuite/oapi-sdk-go/v3 v3.11.0`，来源提交 `3c046e36e885437d8dbae8870b8d56fce163590d`。Leader 三项发布阻断为 SDK bootstrap 同样禁止重定向、回复目标包含内部 Agent App、启动时拒绝 Tenant/Binding 重用；均已修复并通过回归测试。Fable 定向审查未增加新的发布阻断，Leader 已完成源码与测试复核。

Fable 建议在受理超时后继续接收，分类为风险登记/可选小修。Leader 未采用：这会改变 `AcceptFunc`/`TextAdapter.Serve` 的“任何实际受理错误均终止，原错误返回”契约；每次写入都超过预算的故障也可能被反复忽略。当前保留失败即停并登记可用性代价。Fable 提及发送最长 20 秒适用于直接调用两次 10 秒 HTTP；本公共消费者还以一个 15 秒 Context 约束 Token 和回复的总耗时。

| 风险触发与影响 | 当前边界与监测/降级 | 生产缓解及残余风险 |
| --- | --- | --- |
| 数据库延迟或故障使实际受理超过 2 秒，通道与进程可能终止 | 返回受理错误并停止；观察固定失败日志及可选 accept 阶段指标，修复存储后重启 | 由进程管理器重启并按容量实测预算；平台有限重投仍可能耗尽，未落库消息可能丢失 |
| SDK 并发调度或平台重推改变消息顺序 | 只保证串行受理后的持久顺序，不保证原始发送时间顺序 | 平台有可信序号时再评估有限重排；缺少序号时无法恢复严格原始顺序 |
| 停止/断网时 ACK 未发出，或回复 HTTP 结果未知 | 重推按 message_id 持久去重；Unknown 终结且不重发、不重跑 Agent | 按平台查询/幂等能力设计对账；UUID 去重只有一小时，不承诺永久 exactly-once 或最终送达 |
| Token 获取失败、限流或 Token 失效导致未回复 | Token 失败也记 Unknown，显式业务拒绝记 Rejected；观察 deliver 阶段和 Outbox，当前不自动重试 | 按 API 能力增加 Token 缓存及限频策略；当前 Unknown 不能区分“尚未发送”与“可能已发送” |
| 同一 App 多进程运行，或订阅未注册事件 | 仅支持一个实例；应用只订阅 `im.message.receive_v1`，其他事件可能被 SDK 返回失败并反复投递 | 生产增加账号注册唯一性及已订阅事件治理；本地启动校验不能约束其他进程配置 |

本次发布阻断以正常链路、安全边界和已承诺保证为准；风险登记不扩展为新的调度器、重放机制或生产恢复功能。
