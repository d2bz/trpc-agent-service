# 企业微信单聊文本执行切片

## 目标与范围

基于 `deeb137` 的已提交 PostgreSQL Store、企微长连接协议包和 `sessionrun.Start/Run`，形成单进程、单机器人绑定的可运行文本链路。网页聊天继续使用同一个 Session Run 服务。实现由 Opus 5 `max` 负责，Leader 审查和验收。

允许修改 `channels/wecom`、`channels` 的扫描请求及 PostgreSQL 查询、`cmd/trpc-service` 接线与针对性测试，以及使用和验收文档。净增目标不超过 3000 行、20 文件。原工作区的 Worker、Dispatcher、Waker、Scanner、Hold/Transcript 等实验保留，不自动纳入。

用户再次强调：不要围绕单个 bug 持续扩大规模。只修可复现且阻断本切片验收的问题；风险登记与可选优化不进入实现。默认只进行一次 Leader Review 与 Opus 修复；新增猜想先分级，不补穷举故障矩阵，不因理论边界增加通用抽象。达到验收立即停止。

本切片不实现飞书、群聊、媒体、卡片、主动推送、Redis 通知、多个机器人、跨节点执行调度、完整 Transcript 对账或 Tool 重放。上述能力继续保留架构设计。不能以省代码为由撤掉租户隔离、持久去重、同 Session 顺序、Run/Outbox CAS 或发送失败不重跑 Agent。

## 接线与恢复边界

1. 默认禁用，显式启用后要求 PostgreSQL 进程 profile，Tenant/App/Binding/Bot 由服务端静态配置决定。Bot Secret 只接受保留名称 `env:TRPC_SERVICE_WECOM_BOT_SECRET`，由启动代码私有授权器按精确租户和引用放行，不能进入租户模型 Entitlement。
2. 单独受理循环按接收顺序把 DirectText 写入 Store.Accept。重试使用同一组平台生成的 Inbox/Run/Request ID，重复 msgid 返回已存在记录。
3. 恢复和待处理扫描必须在 SQL LIMIT 与更新前按可信 Tenant/Binding 过滤。平台全局扫描的零值行为保留；不允许一半范围字段为空。无需新增数据库列或迁移。
4. 执行循环先 ClaimNextRun，再用 claim 的身份和 RequestID 调用共享 Session Run。执行期限从 claim 前的时钟加 RemainingExecutionBudget 计算。Session busy 先在预算内等待，避免立即 Yield 耗尽 attempt；MarkRunStarted 后不得 Yield 或再次调用 Runner。
5. Runner 事件完整排空后，正常路径先 Close Handle，再取消派生 context。最终文本与 Run 终态在 FinishRun 中原子持久化，最多一个 Outbox。文本上限 20480 UTF-8 字节，溢出带固定截断标记，持久化内容与发送内容一致。
6. 发送只使用版本化、带连接 generation 的持久目标，并再次核对租户、Binding 和 Channel。每条终态回复最多一次发送尝试；明确拒绝或目标过期为失败，回执未知保留 duplicate_risk，不自动重发，也不重新运行 Agent。
7. 未启动的持久任务在恢复后可执行。FirstExecutionStartedAt 非空的中断任务明确失败，不推断模型或 Tool 可以重放。该边界不提供“模型已完成、Outbox 未提交”窗口的答案重建。
8. 连接重建后旧 ReplyTarget 无效，待发送回复记录为失败；不能宣称跨连接重放或跨重启最终送达。平台尚无已验证入站 ACK/补发保证，读取后落库前仍有丢失窗口。真实机器人行为需后续凭据联调验证。
9. 启用时要求绑定 App 的 Session 与 Pin 持久；当前仅支持进程默认 PostgreSQL Session，启动及每次实际 Revision 执行前拒绝非默认 BackendProfile，防止配置发布后切换到内存存储。退出先取消并等待受理/执行/发送，再关闭 Runtime 和数据库。

## 验收条件

- 本地 mock WebSocket 的真实协议帧，经实际 Runner、PostgreSQL Store，产生最终回复并得到 ACK。
- 同 msgid 重投只受理一次、执行一次、发送一次；同 Session 多条消息按受理顺序执行和回复。
- 不同租户以及同租户不同 Binding 的记录不被当前消费者扫描、认领或恢复；包括外部记录排序靠前、LIMIT=1 的情形。
- 明确拒绝、回执未知与连接目标过期均不重跑 Agent；未知发送不产生第二个 final 帧。
- 重建消费者后持久去重仍生效，未启动任务可继续，已启动未知任务失败且不重新执行，旧连接 Outbox 明确失败。
- Web 正持有同 Session 租约时，IM 在释放后继续，不快速耗尽 attempt。
- 覆盖 UTF-8 截断、取消与事件排空、Bot Secret 不可用于模型、错误正文和错误链不泄漏敏感标记。
- 干净工作区全仓默认 race、vet、build 通过；真实 PostgreSQL 门控集成通过。没有凭据时不能标记真实企微账号联调完成。

## 审查裁决

Fable 5.1 `max` 会话 `15e8f3fa-c1b6-4e57-ae88-14eaa57b36bc`，实际模型 `claude-fable-5-1`，支持此路线。Leader 采纳保守恢复与最多一次终态发送，修正三点：期限使用 RemainingExecutionBudget；扫描必须在数据库内限定范围；传输错误可能包含敏感文本，已用拨号错误回归复现并修复公开错误出口。串行执行的跨会话等待登记为吞吐限制，本版不承诺跨会话并行。

上述范围已完成本地验收，证据如下。真实账号联调不在本次完成结论中；不继续扩展本切片功能。

## 验证结果

2026-09-07，在基线 `deeb137` 的干净工作树 `codex/wecom-text-local` 验证；没有纳入原工作区未提交的 Worker、Dispatcher、Waker 或 Session Run 实验。以下默认检查全部通过：

```
go build ./...
go vet ./...
TRPC_SERVICE_MODEL_INTEGRATION=0 TRPC_SERVICE_SESSION_INTEGRATION=0 \
go test -race -count=1 -timeout 900s ./...
```

门控集成使用 `deploy/docker-compose.session.yml` 的本地开发库，独立 schema 并在结束时清理。以下检查通过，PostgreSQL Store 3.906 秒、企微包 9.993 秒：

```
TRPC_SERVICE_SESSION_INTEGRATION=1 \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
go test -race -count=1 -timeout 180s ./trpcservice/channels/postgres ./trpcservice/channels/wecom
```

对应关系：受理/执行/发送各一次与同 Session 顺序、明确拒绝不重跑、未知回执不重发、Web 租约占用后继续，见 `trpcservice/channels/wecom/e2e_integration_test.go`。该夹具使用真实 PostgreSQL Channel Store、Session、Pin 和配置仓库；重建 Runtime/Session 对象后检查历史和 Pin 保留、重复消息不增加用户轮次，以及未启动任务继续、已启动过期任务不再进入 Runner、旧连接目标失败。这是同数据库上的对象重建实验，不是外部进程崩溃或真实 Bot 实验。

跨 Tenant/Binding 的扫描和恢复隔离（外部行排序靠前、LIMIT=1）见 `trpcservice/channels/postgres/scope_integration_test.go`；绑定错配、查询失败后的回复顺序、目标编解码、UTF-8 截断和 Event 筛选见 `trpcservice/channels/wecom/consumer_test.go`；默认关闭、PostgreSQL 限制、Bot Secret 精确放行，以及终止错误触发 HTTP 排空退出见 `cmd/trpc-service/wecom_test.go`。原有 `waitForStop` 四项断言保留，只补禁用通道的 nil 参数。

中间并行运行两组 race 时，原有 `sessionlease.TestLeaseStopsRenewingOnceReleased` 出现一次计时敏感失败；隔离复测及最终全仓检查通过，没有为此改动 Session Lease 模块。

未完成：没有真实企业微信机器人凭据，真实账号联调仍未做，不能据本次结果宣称线上可用。已启动即中断的 Run 明确失败而非重放，连接重建后旧回复记录为失败，两项为本切片接受的残留边界。
