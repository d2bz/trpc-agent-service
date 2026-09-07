# 2026-09-07 设计复核与参考实现验收

## 1. 本轮范围

本轮完成原题七项设计内容复核、八类交付物映射和现有参考链路验证。仅修订文档并新增一个本地验收脚本，没有新增或修改 Go 运行时功能；没有把 Channel、真实 IM、Memory、Telemetry 或生产部署扩为本轮实施任务。

原题依据为[上游固定版本 README](https://github.com/liuzengh/trpc-agent-service/blob/aa000c8407dcd6ea7788fcdccde9574b44bbe2d2/README.md)：以架构设计为主，不要求完整系统，同时要求基于设计的 GitHub 实现代码。设计复核通过表示内容满足内部核对，不代表赛事最终批准或全部功能可运行。

## 2. 八类交付物

| 交付物 | 当前入口与复核结论 |
| --- | --- |
| 架构设计文档 | [当前方案](solution.md)，完整覆盖七项要求；[冻结稿](submission-2026-08-27.md)保留历史内容，最新边界由当前方案与验收矩阵说明 |
| 系统架构图 | [架构](architecture.md#2-系统架构图)，显式包含 Storage Router/Adapter、治理、Telemetry、数据库及外部平台，已渲染检查 |
| 核心时序图 | [企业微信完整链路](sequence.md#1-企业微信完整链路)，已对齐 Bot 长连接，覆盖 Runner、Tool、Session/Memory、Outbox 和 trace/request 关联，已渲染检查 |
| 数据模型 | [数据模型与 ER 图](data-model.md)，核心实体、审计字段和账号绑定唯一性已核对，ER 图已渲染检查 |
| 同步与幂等 | [同步设计](storage-and-consistency.md)，包括 Event/State、派生数据、去重、恢复及迁移回退条件 |
| 多后端方案 | 同文第 2、3 节区分 SQL、Redis、向量库、对象存储的用途、同步与成本取舍；具体驱动实现状态另列 |
| 至少八项风险 | [十二项风险](solution.md#9-主要风险)，逐项列当前边界、残余风险、检测/降级和生产缓解措施 |
| GitHub 实现代码 | `feature/d2bz` 的已有实现及当前未提交实验分别记录；本轮仅提交文档/脚本检查点，最终代码选择和远端提交仍待收口 |

## 3. 修正与风险分类

- **发布阻断（设计材料，已修正）**：IM 路由用外部账号确定租户，但唯一索引原先允许跨租户重复账号。现明确完整平台账号命名空间全局唯一，长连接绑定来自服务端配置，Binding 尚未实现。
- **发布阻断（设计材料，已修正）**：迁移后直接回源可能遗漏目标新增写入。现限定静默、校验一致且目标无新写入的回退条件；其余先停写对账，当前未实现迁移。
- **发布阻断（事实表述，已修正）**：RLS、Audit/向量/对象后端和多角色最小部署原有表述易被理解为已实现。已标明当前实际边界。
- **风险登记（已补材料）**：企微长连接没有 Webhook 的 HTTP 200 步骤；`msgid`、`req_id`、平台 `request_id` 各司其职，入站落库前丢失、回复引用失效及回执未知均不假定由平台自动恢复。
- **可选优化（未实施）**：完整 TTL/LRU、生产迁移、RLS、媒体/卡片、全链路 OTel 和更多失败矩阵继续保留设计，未转为本轮功能任务。

## 4. 实际验证

测试时基线为 `ab64d81c153beb8e1dab304a0c8168203c540184`，工作树含原有未提交 Channel/sessionrun 增量。环境为 macOS arm64、Go `1.25.12`、Node `26.7.0`；框架依赖按仓库锁定版本使用。结果不能冒充基线 commit 本身或最终交付 commit 的新测试证据。

| 检查 | 结果 |
| --- | --- |
| `TRPC_SERVICE_MODEL_INTEGRATION=0 TRPC_SERVICE_SESSION_INTEGRATION=0 go test -race -count=1 -timeout 900s ./...` | 通过，含 Channel 当前实验的默认测试；真实数据库和外部模型门控未运行 |
| `go vet ./...` | 通过 |
| `./build.sh` | 通过，构建 `bin/trpc-service` |
| `./start.sh` + `node scripts/verify-reference.mjs http://127.0.0.1:18080` + `./stop.sh` | 8 项通过，服务仅在本机运行，停服后 PID 文件已清理 |
| `node --check scripts/verify-reference.mjs` | 通过 |
| Mermaid CLI `11.12.0` + 本机 Chrome | 架构、主时序和 ER 三张图均生成 PNG，并完成视觉检查 |
| `git diff --check` | 通过 |

HTTP 脚本验证：健康检查；未认证拒绝及 chat/admin 凭据分离；确定性回显与服务端 ID；SSE `[DONE]` 与续接；错误租户断言拒绝；发布后旧 Session 保持版本、新 Session 使用新版本；Pin 冲突；回滚后既有 Session 不漂移。内部 Event、Tool、Runtime 并发和隔离仍由全仓测试提供证据。脚本无需 npm 依赖，不读取 `.env.local`；它在临时 demo 进程创建测试 Revision，最后恢复原默认路由。

完整默认测试输出和 HTTP 检查输出见[本轮输出记录](../artifacts/demo/2026-09-07/reference/results.md)。本轮未运行真实 PostgreSQL/Redis、外部模型或企微账号联调；没有将这些能力标为本轮验证通过。

## 5. 下一条交付主线

1. 参考候选基线 `a7484d36d3353954edef591a12e28056a23bccd1` 已通过下述干净快照验证；最终交付时核对后续功能切片。原工作树保留，不批量删除或自动纳入整个 Channel 实验。
2. 网页与企微按独立演示切片冻结范围后实施，飞书保持差异设计；它们不阻断本轮设计内容复核。
3. 最终核对 GitHub 分支、远端 SHA、提交权限和赛事填写信息。当前文档检查点不等于最终提交，也不改变已选实现的安全与正确性保证。

## 6. 干净提交补充验证

为排除未提交实验文件的影响，从 `a7484d36d3353954edef591a12e28056a23bccd1` 执行 `git archive`，在独立临时目录构建和运行；没有修改原工作树或将本地配置复制进去。该提交的 Go 功能代码仍是原 `ab64d81` 基线，新增的是交付文档和验收脚本。

干净快照的全仓默认 race、`go vet ./...`、`./build.sh` 和相同 8 项 HTTP/SSE 检查全部通过。服务已停止。这证明现有参考实现可以脱离未提交 Channel 代码独立构建与演示；真实 IM、数据库门控和外部模型仍不在本轮验证范围。用户随后明确关注新增业务代码，下一切片进入网页聊天入口。
