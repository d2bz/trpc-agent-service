# 最小企微可观测性切片

## 目标与边界

基线 `b35215d`。在已验收的单进程企微文本链路增加可选择启用的 OpenTelemetry 阶段 Span、计数与耗时，按持久 `request_id` 关联受理、执行与发送。此切片不改变 Run/Outbox 状态机、尝试次数、Session 生命周期或回复协议。

允许修改新的 `trpcservice/telemetry` 包、企微 Consumer 的观测接线、`cmd/trpc-service` 的私有启动配置和关闭接线，以及针对性测试、本文件和必要 Go 依赖声明。目标控制在净增 1500 行、12 个文件以内；3000 行/20 文件或两个小时的项目强制暂停阈值继续有效。

不实现完整 Model/Tool/Session/Memory 自动埋点、持久 traceparent、数据库迁移、通用 Worker/Dispatcher/Waker、媒体、群聊、卡片、预算、审批、计费平台、仪表盘、多角色部署或在线管理接口。原工作区实验及 `.env.local` 不进入此工作树。当前真实 Bot 和网页服务不重启、不替换、不发送新消息。

## 实现决策

- 使用独立 SDK TracerProvider/MeterProvider，不调用 `otel.SetTracerProvider`、`otel.SetMeterProvider` 或上游 `telemetry/trace.Start`。固定 tRPC-Agent-Go v1.11.2 启用 tracing 后默认可捕获 LLM 输入、Tool 参数及错误，当前只显式采集平台白名单。
- 默认关闭；显式 `TRPC_SERVICE_TELEMETRY_ENABLED=true` 后使用 OTLP/HTTP 导出至 `TRPC_SERVICE_OTLP_ENDPOINT`，缺省 `http://127.0.0.1:4318`。配置错误返回固定安全错误，不能回显端点中的凭据或原始值；不接受含用户信息、查询或片段的 URL。优先复用已有 v1.29.0 OTel 依赖，不升级工具链。
- 受理：一次 Store.Accept 持久化重试循环只记录一次，成功后使用 AcceptResult 原始 RequestID，重复投递标记 duplicate，不能用本次生成但未入库的 ID 冒充原请求。
- 执行：记录现有 claim 的 RequestID、attempt、实际 Revision、阶段结果和耗时；不让统计逻辑影响执行、Yield、取消、Event 排空或 FinishRun。已启动未知任务恢复失败与真实调用 Runner 区分。
- 发送：只围绕真实投递尝试记录发送状态、耗时，区分平台成功、明确拒绝、未知和旧目标；CompleteOutbox 的持久化重试不增加发送次数，不能把平台 ACK 等同状态写入成功。
- 每阶段独立 Span，以内部 `request_id` 查询关联；暂不宣称同一连续父子 Trace。阶段内 Context 可传递给既有调用，不增加 Session 接口或持久化字段。
- Span 仅包含内部 Tenant/App/Binding、request/run/outbox 标识、固定 stage/channel/outcome/error_type、attempt、耗时和必要计数；不采集 Principal、Session、外部账号/用户/消息 ID、正文、目标 JSON、Tool 参数/结果、凭据或原始错误。不调用 RecordError 原样记录错误。
- 指标为阶段处理次数及耗时直方图，标签限定固定 stage/channel/outcome/error_type 和静态绑定的内部租户/App；请求、Run、Outbox、用户、Session 或文本不作指标标签。
- 遥测导出在有界后台队列运行，失败不影响业务状态、执行次数或发送次数。导出器公开错误也使用固定安全信息，关闭在业务排空后有界 flush/shutdown；不增加业务恢复逻辑。

## 验收条件

1. 默认关闭保持原有行为；开启后本地测试接收到三个阶段 Span 和指标，重复入站沿用原 RequestID。使用真实 Runner、PostgreSQL 和模拟企微的既有集成链路验证关联。
2. 白名单测试覆盖消息、外部用户/账号、凭据和原始错误的敏感标记不出现在 Span/Metric/导出错误中；观测包不安装全局 provider。
3. 一个代表性导出故障不改变 Run/Outbox 结果或发送次数；有界关闭且无无主循环。原企微顺序、去重和失败不重跑的验收继续通过。
4. 针对性 race 与真实 PostgreSQL 门控集成、干净工作树默认全仓 race/vet/build 通过。只对新增风险和已发生回归加测试，禁止扩大故障矩阵。
5. 记录实际范围、命令和证据后提交，结束本切片。没有真实 Bot 观测证据时只标记本地协议集成通过。

## 风险分级

- 发布阻断：关闭失效、敏感字段泄漏、遥测故障改变业务结果或次数、破坏现有生命周期和测试。
- 风险登记：进程崩溃可能丢失未导出的遥测；指标不是持久账本；阶段依赖 request_id 关联，暂无跨队列持久 Trace 上下文；当前没有 Model/Tool/存储细分 Span 与 token/费用采集。
- 可选优化：更细 Span、仪表盘、采样策略和跨进程连续 Trace 后续评估，均不进入本切片。

## 验证结果

2026-09-07 实施完成，本地验收与 Leader 最终 Review 通过。功能由 Opus 5 `max` 实现，实际会话模型标识为 `claude-opus-5`。

范围：新增 `trpcservice/telemetry/telemetry.go`(350)、`channel.go`(307)、`telemetry_test.go`(347) 和 `trpcservice/channels/wecom/telemetry_test.go`(416)；改动 `trpcservice/channels/wecom/consumer.go`(+159/−20)、`cmd/trpc-service/wecom.go`(+24/−2)、`main.go`(+13/−1)、`wecom_test.go`(+51/−2)、`e2e_integration_test.go`(+4/−0)、`go.mod`(+9/−9，`go mod tidy` 把 7 个 OTel 模块由 indirect 提为 direct)。Opus 净增约 1646 行 / 10 个代码文件，含 Leader 回归夹具 `partial_success_test.go`(96) 约 1742 行：超出 1500 软目标，低于 3000 行 / 20 文件强制阈值，按 19:24 指示冻结范围。

行为：`ConsumerConfig.Telemetry *telemetry.Telemetry` 是唯一新增可选依赖，nil 即现状；Session/Store 契约、状态机、尝试次数与数据库 schema 未改。受理在整个持久化重试循环外只记一次，重复投递记 duplicate 并沿用首次入库的 `RequestID`；执行记 claim 的 request/run/attempt 与解析后的 RevisionID，区分 interrupted/yielded/skipped/failed/succeeded。发送阶段记录实际发送结果或发送前跳过、旧目标；CompleteOutbox 的持久化重试不产生新发送记录。`channels.SendResult`、Run 状态与发送次数不变。`trpc.channel.stage.count` 统计阶段次数，`trpc.channel.stage.duration` 记录毫秒耗时；标识符只上 Span，指标标签限于 stage/channel/outcome/error_type 加静态租户/App，不带请求 exemplar。执行结果不等于 FinishRun 持久化成功，阶段次数也不直接等于实际发送次数。

命令与结果（本地开发 DSN，独立临时 schema）：

- `gofmt -l .` 无输出；`go build ./...`、`go vet ./...` 通过。
- 门控针对性 race：`TRPC_SERVICE_SESSION_INTEGRATION=1 TRPC_SERVICE_POSTGRES_DSN=… go test ./trpcservice/channels/wecom/ ./trpcservice/telemetry/ ./cmd/... -count=1 -race -timeout 600s` → ok 11.226s / 6.554s / 2.993s。
- 干净工作树全仓默认 race：`go test ./... -count=1 -race -timeout 900s` → 全部 ok（exit 0），telemetry 7.522s、wecom 4.751s、cmd 1.604s，原有默认测试全部保留并通过。
- 验收 1：`TestIntegrationRecordsThreeStagesOfOneRequest`（真实 Runner + PostgreSQL + 模拟企微：accept 两条、execute 一条、deliver 一条，全部指向同一 `request_id`，重复投递沿用原 RequestID，execute 记录实际应答 Revision）。
- 验收 2：`TestAStageRecordsTheWhitelistOnly`、`TestRecordedValuesStayInTheirVocabulary`、`TestARefusedEndpointIsNeverEchoed`、`TestAcceptRecordsAFailureWithoutItsCause`、`TestOpenInstallsNoGlobalProvider`，以及 Leader 夹具 `TestPartialSuccessDoesNotLogCollectorText`（trace/metrics 两条路径都不再输出 Collector 原文）。集成用例对每个 Span 和指标读数复查敏感标记与标识符。
- 验收 3：`TestIntegrationARefusedCollectorChangesNothing`（Collector 全程 400，Run 仍为 succeeded/attempt=1，Outbox 为 sent/attempt=1，无重复风险，用户轮次仍为 1）、`TestAFailedExportChangesNothing`、`TestShutdownIsBoundedAndSaysNothing`、`TestWeComChannelFlushesWhatItsLoopsRecorded`（循环退出后 `stop()` 完成有界 flush）。企微顺序、去重与失败不重跑的既有用例继续通过。

PartialSuccess 修复：`Open` 安装固定文本的 `otel.SetErrorHandler`（只输出 `telemetry: an export attempt failed`），并给两个 OTLP/HTTP 导出器设置 3s 超时与 0.5s 重试间隔，使 BatchSpanProcessor 后台队列的排空有界。v1.29.0 的 HTTP 导出器没有 `WithHTTPClient` 配置入口，因此本版采用 SDK 错误处理器拦截该路径。它只在启用遥测的 `Open` 里安装，作用持续到进程结束或其他代码替换处理器；默认关闭时不安装，也始终不注册全局 provider。

未解决发布阻断：无。风险登记：错误处理器为进程级全局；只有本地协议集成证据，未接触真实 Bot，按验收 5 仅标记本地通过；进程崩溃仍可能丢失未导出遥测。

## Leader 最终裁决

- 发布阻断已关闭：SDK 的 PartialSuccess 原文绕过返回错误包装器。Leader 先用 trace/metrics 双路径夹具复现泄漏，再独立运行 `go test -race ./trpcservice/telemetry -run '^TestPartialSuccessDoesNotLogCollectorText$' -count=1 -timeout 30s` 验证修复通过。
- Leader 独立 `go vet ./...` 和 `TRPC_SERVICE_SESSION_INTEGRATION=0 go test -race -count=1 -timeout 900s ./...` 均通过；已复核 Opus 的 PostgreSQL 门控测试证据。
- 风险登记：接受错误处理器的进程级影响，其他 OTel 诊断也会失去错误详情；生产可按固定错误计数告警，并通过受控 Collector 自身诊断定位。阶段关联、遥测丢失和设计未实现项保持上述边界。
- 可选优化：不继续扩展细分 Span、Collector 仪表盘或 Channel 公共层。本切片结束，下一步为可复现本地部署。
