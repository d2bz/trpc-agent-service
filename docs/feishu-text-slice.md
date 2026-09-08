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

凭据已保存且非空，权限为 `600`。2026-09-08 只读预检结果：应用换取 Token 返回 HTTP 200 / code 0，机器人身份查询返回 HTTP 200 / code 0；企业查询返回 code 99991672，缺少 `tenant:tenant:readonly`。该结果只证明凭据有效和机器人身份可读取，不证明长连接在线或消息收发成功。

本切片尚未新增 Go Adapter 或启动接线。Fable 5.1 以 `max` 两次调用均因上游 HTTP 503 / No available accounts 失败，未产生有效模型回复；依赖该审查的回调生命周期与租户绑定决策暂停，没有以其他模型替代审查或宣称完成。Opus 5 `max` 的实际回复标识已核实为 `claude-opus-5`，仅做源码阅读；在审查不可用后由 Leader 中止，没有最终实现方案，也没有授权或产生代码修改。

已完成的独立准备工作为本文与 70 行只读预检脚本，共两个文件。`node --check`、缺失文件的固定错误/退出码检查及真实官方 API 预检已执行，后者如实返回企业权限缺失并退出 1；未重跑 Go 测试，因为 Go 代码和依赖均未改变。当前网页与企微服务未重启，未发送真实飞书消息。

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

## SDK 核对与待决边界

核对版本为官方 `github.com/larksuite/oapi-sdk-go/v3 v3.11.0`，来源提交 `3c046e36e885437d8dbae8870b8d56fce163590d`；尚未加入项目依赖。

- **发布阻断，待设计落实**：SDK 逐事件并发调用 Handler，并在 Handler 返回后发 ACK。必须以持久受理成功决定成功 ACK，串行受理和取消排空也需明确；不能从 Handler 内等待包含自身的 SDK 任务组。
- **发布阻断，待设计落实**：SDK 不替应用核对事件的 App ID 和 tenant_key，需以可信静态绑定及认证得到的企业身份校验，不能凭第一条事件建立租户信任。
- **发布阻断，待设计落实**：SDK 默认日志可包含消息正文、外部 ID 和原始错误，需覆盖 Logger 并约束公开错误。
- **发布阻断，待设计落实**：一次 SDK Reply 调用可发起两次 HTTP 请求；`Success()` 仅检查 code 0，不能单独证明完整成功响应。发送结果分类和重试行为须符合公共消费者既有保证。
- **风险登记**：SDK 并发回调不保证平台消息的原始顺序；最终需明确本实现保证的受理顺序。平台回复 UUID 的去重窗口有限，不能作为永久 exactly-once 保证。

上述条目是本次接入需作出的有限决策，不扩展为新的通用调度器、重放机制或生产恢复功能。
