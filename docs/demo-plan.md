# 演示与验收计划

## 1. 目标

按原题交付完整架构设计和对应的 GitHub 参考实现。对选入演示的场景给出固定输入、可重复步骤、明确预期结果和适当的测试证据；原题不要求下列所有扩展场景都能运行。七项设计验收见[验收矩阵](acceptance.md#原题设计验收)。

## 2. 证据规范

每次里程碑演示保存以下信息：

- 当前 Git commit、构建时间和依赖版本。
- 启动配置的脱敏副本，以及实际使用的进程或容器状态。
- 请求和响应样例、已实现的关联 ID、租户与 Revision 标识；未实现的 Trace 等能力明确标注。
- 对应的测试命令及完整结果；故障场景同时保存恢复前后的日志和指标。
- 数据库只截取必要字段，密钥、外部用户标识和消息正文按审计策略脱敏。

证据目录统一使用 `artifacts/demo/<date>/<scenario-id>/`。二进制、数据库和模型响应不作为唯一证据，所有关键结论必须能由测试或查询命令复现。

## 3. 已有参考实现的演示证据

D01-D03 是已有代码对应的参考链路，保留原演示步骤；当前交付应在选定提交上重跑并记录结果，不能把历史通过记录当作本轮验证。

2026-09-07 当前工作树的全仓默认 race 和本地 HTTP/SSE 验证见[验收记录](verification-2026-09-07.md)。已提供可重复的 HTTP 演示脚本（Node.js 20+，无 npm 依赖）：

```bash
./build.sh
TRPC_SERVICE_ADDR=127.0.0.1:18080 ./start.sh
node scripts/verify-reference.mjs http://127.0.0.1:18080
./stop.sh
```

请对一次性 InMemory demo 进程运行。脚本使用 demo/echo，创建一个测试 Revision，
验证发布、Pin 和回滚后恢复原默认版本；测试 Revision 留在该临时进程中，停服后消失。
它从环境或 `data/admin-api-key` 读取管理凭据，不打印凭据，不读取 `.env.local`。
自定义凭据须给服务和脚本设置相同环境变量。脚本覆盖 D01/D02 的 HTTP 可观察行为，
内部 Event 历史、完整租户隔离、Runtime 并发和 D03 Tool 行为继续由下列自动化测试取证。

### D01 最小 HTTP Agent 链路

1. 执行 `./build.sh && ./start.sh`，确认脚本只在 `/healthz` 就绪后返回。
2. 带 `Authorization: Bearer` 请求 `/v1/chat/completions`，验证确定性模型返回 `echo: <input>`；去掉该请求头验证返回 `401 unauthenticated`。
3. 不带 `X-Session-ID` 请求一次，从响应头取回平台生成的 Session ID，用它连续请求两轮，验证 Session Event 数量持续增加。
4. 在请求体中把 `user` 改成任意他人身份，验证会话仍写在 `u/{principal_id}` 名下。
5. 加入 `"stream": true`，验证 SSE 分片并以 `data: [DONE]` 结束，且响应头仍带回 Session 与 Revision。
6. 执行 `./stop.sh`，确认进程收到信号后退出且 PID 文件被清理。

自动化证据：

```bash
go test ./trpcservice/identity ./trpcservice/sessiondir ./trpcservice/agent ./trpcservice/web ./cmd/trpc-service
go test -race ./trpcservice/identity ./trpcservice/sessiondir ./trpcservice/agent ./trpcservice/web ./cmd/trpc-service
```

### D02 Tenant、Revision 与 Runtime 路由

1. 创建两个 Tenant，并在两个租户内创建相同 ID 的 Agent App 和 Revision。
2. 分别发布 Revision，验证默认路由只能返回本租户配置。
3. 发布第二版本后验证新 Session 使用新默认版本，首轮已经开始的 Session 仍留在旧版本；对已 Pin 的 Session 传入不同 `X-Agent-Revision-ID` 验证返回 `409 pin_conflict`。
4. 把旧版本重新切为默认版本，验证 `routing_version` 递增、历史 Revision 配置摘要不变，且两个已有 Session 的 Pin 都没有变化。
5. 并发解析同一三元组，验证只构建一个 Runtime；关闭 Resolver 时验证其等待活动租约释放。

自动化证据：

```bash
go test ./trpcservice/tenant ./trpcservice/agent
go test -race ./trpcservice/tenant ./trpcservice/agent
```

### D03 Revision Tool/Policy 闭环

1. 创建一个引用 `builtin_add`、`builtin_echo` 和 `builtin.safe-tools` 的 Revision，确认 Runtime 发给模型的函数名和顺序与 Revision 一致。
2. 使用离线脚本模型：首轮同时请求两个 Tool，确认框架执行后第二次模型请求包含按 call ID 关联的 `sum=5` 与 `text=pong`。
3. 确认最终文本从真实 OpenAI Adapter 的 `stream:true` SSE 返回，并以 `[DONE]` 结束。
4. 分别提交未知 Tool、未知 Policy、重复引用、缺少 Policy 和未授权 Tool，确认 Runtime 全部 fail closed。
5. 检查 before/after 审计只包含可信作用域、Tool、call ID、结果状态和耗时，不包含参数、结果或错误正文。
6. 让脚本模型持续返回 tool calls，确认只执行 4 轮，第 5 轮在执行 Tool 前终止。

自动化证据：

```bash
go test -race -count=1 ./trpcservice/tool ./trpcservice/agent
```

## 4. 已选演示方向与扩展场景

2026-09-07 确定网页聊天和企业微信智能机器人长连接（Bot ID + Secret）为演示方向，先完成文本链路。网页入口已完成，启动服务后可打开 `/` 发送、续聊和停止，见[网页切片验收](acceptance.md#网页切片验收2026-09-07)。企业微信真实收发尚未实现，下一切片须独立冻结范围、承诺和验收条件。飞书保留差异设计，后续视需要决定实现；两类外部 IM 的设计要求由企微与飞书覆盖。

E01-E07 是旧完整平台排期中的扩展设计/未来场景，保留用于说明演进方向，不再作为本次必须全部运行的清单。只有明确选入实现范围的场景才按对应承诺验证。

| 场景 | 核心操作 | 选用并实现后的验证目标 | 关联设计项 |
| --- | --- | --- | --- |
| E01 多租户控制面 | 通过 Admin API 创建两个 Tenant、App、Revision、Backend 和 Channel Binding | 跨租户读取返回 404/拒绝；发布、灰度、固定版本和回滚可追踪 | A01、A05、A26 |
| E02 双 Worker 会话 | 两轮消息分别命中不同 Worker，并同时向同一 Session 发送消息 | 无 sticky session 仍保留上下文；并发由 Run 租约在入口串行化，第二个 Worker 收到 `409 session_busy`，持有者失效后按 TTL 接管。不宣称过期 Worker 的写入被存储层拒绝（见 [Session Run Lease](session-lease.md)） | A03、A04、A08、A09、A10 |
| E03 多后端与迁移 | Tenant A 使用 Redis，Tenant B 使用 PostgreSQL；迁移一个 Session 和一套向量索引 | 路由隔离；校验和一致；切换失败可回滚，成功后读写指向新后端 | A06、A07、A11、A12 |
| E04 IM 幂等链路 | 对选入实现的 IM 发送文本，并重复投递同一外部事件 | 按声明的去重和重试边界检查 Run/Tool 次数；回复结果未知时说明平台限制，不能无依据宣称外部发送 exactly-once | A13-A18 |
| E05 治理与审计 | 触发允许 Tool、禁止 Tool、预算超限、敏感信息和危险操作审批 | Guardrail 决策正确；密钥不进入日志；审计字段、token 和成本完整 | A19、A20、A22、A23 |
| E06 Trace 与故障恢复 | 注入模型超时、Tool 失败、数据库短暂中断和 Worker 退出 | Trace 串起入站到回复；Context 取消和 Event 排空；重试不重复副作用 | A21、A24、A25 |
| E07 部署与容量 | Compose 最小部署、Kubernetes 多节点部署和分级压测 | 扩缩容可用；P95、错误率、后端 QPS、token 和成本数据能支撑容量结论 | A27、A28 |

## 5. 可选容量验证方法

原题要求说明容量评估方法，不强制完整压测平台。若选用容量演示，可参考三档流量：基线 1 RPS、目标 20 RPS、突发 40 RPS，记录实际具备的并发、延迟、后端 QPS、投递成功率和 token 指标。流量与时长应按参考实现规模和成本确定。

方案中的初始估算以目标 20 RPS、单 Worker 安全吞吐 4 RPS 得到 6 个 Worker（含一个故障冗余）。没有实测时必须标明这些是估算假设；有实测时注明配置、方法、结果和瓶颈，不把示例值宣称为系统能力。

## 6. 失败判定

以下判定适用于选入演示且明确承诺相应保证的场景；未实现的架构能力按设计和风险说明验收：

- 只能人工观察，无法用测试、查询或稳定 ID 复现。
- 声明消息去重或重试不重跑 Agent，却因重复事件产生额外 Run 或业务副作用；外部回复结果未知按已披露边界判定。
- 声明持久化或跨 Worker 会话，却在重启或换节点后丢失会话。
- 跨租户请求返回了资源是否存在、配置正文或运行对象。
- 声明实现全链路 Trace，却在队列、Tool、存储或 IM 回复阶段断链。
- 日志、Trace、错误响应或证据目录中出现明文密钥。
