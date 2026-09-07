# 企业微信智能机器人长连接协议适配器

本包只做一件事：以长连接方式接入企业微信智能机器人，接收单聊文本消息，并对每条消息回复一条最终文本。它是一个协议检查点，不是完整 IM 链路。

## 范围

包含：

- WebSocket 帧编解码、`aibot_subscribe` 鉴权、心跳、指数退避重连、被接管即终止。
- 入站单聊文本的接受与拒绝规则，以及到平台标识（Principal、Session、Stream）的确定性派生。
- 一条 `aibot_respond_msg` 最终流式回复及其回执判定。

不包含（本包不实现，也不声称具备）：

- 去重、持久化、崩溃恢复、精确一次投递。外部 `msgid` 原样保留在 `DirectText.ExternalMessageID`，供后续持久化 Ingress 去重。
- 任何 Store、队列、Dispatcher、Registry 或 Runner 调用。消息只经由 `Messages()` 的有界 channel 交给调用方。
- 群聊、图片、混合消息、欢迎语、模板卡片、主动发送、素材上传。这些消息类型会被拒绝而不是部分解析。
- 与 `cmd` 或 `trpcservice/channels` 的接线。本检查点不启动真实机器人连接。

## 使用

```go
client, err := wecom.New(wecom.Config{
    Binding: wecom.Binding{
        TenantID:   "tenant-a",
        AgentAppID: "app-a",
        BindingID:  "binding-a",
        BotID:      "<bot id>",
        SecretRef:  "env:WECOM_BOT_SECRET",
    },
    Authorizer: entitlements, // security.SecretRefAuthorizer
})
if err != nil {
    return err
}
go func() {
    // Messages() 有界且永不关闭，退出条件是 ctx，不是 channel 关闭。
    for {
        select {
        case <-ctx.Done():
            return
        case msg := <-client.Messages():
            // 生成回复后在同一连接上发送；target 只在收到该消息的连接上有效。
            _ = client.SendFinalText(ctx, msg.Reply, answer)
        }
    }
}()
err = client.Run(ctx) // 阻塞直到 ctx 取消或到达终止状态
```

`Run` 总是返回非 nil error：正常关闭返回 `context.Canceled`；`ErrAuthRejected`、`ErrTakenOver`、`ErrInboundOverflow`、`ErrReconnectExhausted` 为终止原因，不再重连。拨号失败、读错误、心跳丢失、回复结果未知属于普通故障，按退避重连。

`SendFinalText` 的成功仅指收到 `errcode` 显式为 0 的回执。`ErrReplyRejected` 是平台明确拒绝，`ErrReplyOutcomeUnknown` 是已写出但无回执，两者不同，且本包都不重试。

## 凭据与日志

`Binding` 只携带 `SecretRef`，不携带 Secret。`New` 先对精确引用做租户授权，再解析环境变量；Secret 只出现在 `aibot_subscribe` 请求体中。

本包没有任何日志语句，错误文本不包含 Secret、Bot ID、外部用户或消息 ID、消息体，也不透传平台 `errmsg`（该字段根本不解码）。连接地址是常量，不提供 endpoint 配置项；测试通过包内不可导出的 dial 缝隙指向本地 mock 服务器。

## 身份派生

按架构文档 §5.4 的规则派生，租户、App 和 Binding 全部来自静态 `Binding`，帧内容不参与路由：

- `PrincipalID = "p-" + H(["im-principal-v1", tenant, binding, "user", 外部 userid])`
- `SessionID = "d-" + H(["im-session-v1", tenant, app, binding, "direct", principal, "", "0"])`
- `StreamID = "s-" + H(["im-stream-v1", tenant, binding, msgid])`

`H` 是 JSON 字符串数组的 SHA-256 十六进制摘要，字段边界不可移动。结果为 66 个 ASCII 字符，满足 `tenant.ValidateResourceID` 和 SessionID 67 字符上限。

## 测试

全部验证在本地 mock WebSocket 服务器上完成，不连接真实网络、机器人或模型：

```
go test -race -count=1 -timeout 120s ./trpcservice/channels/wecom
```
