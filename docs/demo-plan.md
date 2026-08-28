# 演示与验收计划

## 1. 目标

演示不以页面展示代替功能验证。每个场景必须包含固定输入、可重复步骤、明确预期结果、自动化测试和可追踪证据。最终演示按“控制面配置 → 数据面执行 → 故障与恢复 → 证据查询”的顺序进行，避免在多个零散命令之间跳转。

## 2. 证据规范

每次里程碑演示保存以下信息：

- 当前 Git commit、构建时间和依赖版本。
- 启动配置的脱敏副本，以及 Docker Compose 或 Kubernetes 资源状态。
- 请求和响应样例、`request_id`、`trace_id`、租户与 Revision 标识。
- 对应的测试命令及完整结果；故障场景同时保存恢复前后的日志和指标。
- 数据库只截取必要字段，密钥、外部用户标识和消息正文按审计策略脱敏。

证据目录统一使用 `artifacts/demo/<date>/<scenario-id>/`。二进制、数据库和模型响应不作为唯一证据，所有关键结论必须能由测试或查询命令复现。

## 3. 8 月 27 日方案提交演示

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

## 4. 9 月 11 日最终验收场景

| 场景 | 核心操作 | 必须观察到的结果 | 主要验收项 |
| --- | --- | --- | --- |
| E01 多租户控制面 | 通过 Admin API 创建两个 Tenant、App、Revision、Backend 和 Channel Binding | 跨租户读取返回 404/拒绝；发布、灰度、固定版本和回滚可追踪 | A01、A05、A26 |
| E02 双 Worker 会话 | 两轮消息分别命中不同 Worker，并同时向同一 Session 发送消息 | 无 sticky session 仍保留上下文；并发按租约和 fencing token 串行提交 | A03、A04、A08、A09、A10 |
| E03 多后端与迁移 | Tenant A 使用 Redis，Tenant B 使用 PostgreSQL；迁移一个 Session 和一套向量索引 | 路由隔离；校验和一致；切换失败可回滚，成功后读写指向新后端 | A06、A07、A11、A12 |
| E04 IM 幂等链路 | 企业微信和飞书各发送消息，并重复投递同一外部事件三次 | 每个外部事件只产生一个 Run；回复经 Outbox 限流、重试并只产生一次业务副作用 | A13-A18 |
| E05 治理与审计 | 触发允许 Tool、禁止 Tool、预算超限、敏感信息和危险操作审批 | Guardrail 决策正确；密钥不进入日志；审计字段、token 和成本完整 | A19、A20、A22、A23 |
| E06 Trace 与故障恢复 | 注入模型超时、Tool 失败、数据库短暂中断和 Worker 退出 | Trace 串起入站到回复；Context 取消和 Event 排空；重试不重复副作用 | A21、A24、A25 |
| E07 部署与容量 | Compose 最小部署、Kubernetes 多节点部署和分级压测 | 扩缩容可用；P95、错误率、后端 QPS、token 和成本数据能支撑容量结论 | A27、A28 |

## 5. 容量演示方法

容量演示使用固定的三档流量：基线 1 RPS、目标 20 RPS、突发 40 RPS。分别记录 Worker 并发 Run、端到端 P50/P95/P99、模型和 Tool 延迟、Session 读写 QPS、Redis 等待时间、SQL 连接池、IM 投递成功率、token/s 和租户成本。每档至少稳定运行 10 分钟，突发档运行 2 分钟。

方案中的初始估算以目标 20 RPS、单 Worker 安全吞吐 4 RPS 得到 6 个 Worker（含一个故障冗余）。最终报告必须用实测值替换这一示例，并说明瓶颈位于模型、Worker、Session Backend 还是 IM 限流。

## 6. 失败判定

出现以下任一情况，场景不能标记完成：

- 只能人工观察，无法用测试、查询或稳定 ID 复现。
- 同一输入重复执行会产生额外 Run、Tool 副作用或回复。
- Worker 重启后只能依赖原节点内存恢复会话。
- 跨租户请求返回了资源是否存在、配置正文或运行对象。
- Trace 在队列、Tool、存储或 IM 回复阶段断链。
- 日志、Trace、错误响应或证据目录中出现明文密钥。
