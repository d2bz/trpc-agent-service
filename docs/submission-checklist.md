# 提交检查清单

## 1. 交付标识

| 项目 | 值 |
| --- | --- |
| 题目源仓库 | `liuzengh/trpc-agent-service` |
| 参赛仓库 | `d2bz/trpc-agent-service` |
| 历史工作分支 | `feature/d2bz`（当前提交前重新核对） |
| 方案版本 | `1.0` |
| 方案提交日期 | 2026-08-27 |
| 最终验收日期 | 2026-09-11 |

历史提交时 GitHub 账号对题目源仓库没有推送权限，开发提交先推送至个人 Fork。组织者要求最终分支采用 `feature/{your_name}`，但 `your_name` 的具体口径尚待确认；最终提交前应重新核对权限和命名。若最终分支必须直接位于题目源仓库且仍无权限，需要先将 `d2bz` 加为协作者。

## 2. 方案材料历史记录

以下勾选记录 8 月 27 日方案提交时的检查，不代表当前工作区已重新验收。原题以架构设计为主，须交对应的 GitHub 实现代码，不要求把所有设计功能完整编码。

- [x] 正式方案：`submission-2026-08-27.md` 中文正文 3288 字，2000–4000 字是原题建议。
- [x] 系统架构图：正式方案内 Mermaid 源码由 CLI 11.12.0 渲染与视觉检查通过。
- [x] 企业微信核心时序图：正式方案内 Mermaid 源码已渲染检查。
- [x] 数据模型与 ER 图：位于 `data-model.md`，ER 图已渲染检查。
- [x] 数据同步、幂等、多后端和迁移策略：位于 `storage-and-consistency.md`。
- [x] 至少 8 个生产风险：总稿列出 12 项。
- [x] tRPC-Agent-Go 复用能力与新增平台层职责：位于 `project-foundation.md`。
- [x] 演示步骤和验收证据规范：位于 `demo-plan.md`。

## 3. 提交前验证

- [x] 2026-09-07 已按[七项原题设计验收](acceptance.md#原题设计验收)完成内容复核及八类交付物映射，见[本轮记录](verification-2026-09-07.md)。
- [x] 当前参考链路、未实现能力和十二项风险边界已区分；后续已完成[网页切片验收](acceptance.md#网页切片验收2026-09-07)及[企微文本链路验收](wecom-text-slice.md#验证结果)。
- [x] 企业微信真实正常单聊已有[独立证据](wecom-text-slice.md#真实单聊验证)；重复投递、发送失败与恢复仍以本地协议集成为证据。
- [x] `f5ed53c` 的可选企微三阶段 OTel 已完成[本地验收](observability-slice.md#验证结果)，以 `request_id` 关联，未验证真实 Bot 遥测，不宣称完整连续 Trace。
- [x] 本轮[可复现本地部署](local-deployment.md#本轮验证)的构建、网页 8 项 HTTP/SSE、环境文件、临时 PostgreSQL schema 启动与 Collector 配置校验通过；没有新增真实 Bot 遥测证据。
- [ ] 在选定交付提交上执行以下适用检查并保存结果；本清单列出命令不代表已经执行通过。

```bash
git status --short --branch
git diff --check
go test ./...
go test -race -count=1 ./trpcservice/config ./trpcservice/tenant ./trpcservice/identity ./trpcservice/security ./trpcservice/secretref ./trpcservice/sessiondir ./trpcservice/sessionbackend ./trpcservice/sessionlease ./trpcservice/sessionrun ./trpcservice/storagebundle ./trpcservice/tool ./trpcservice/agent ./trpcservice/web ./cmd/trpc-service
go vet ./...
./build.sh
./start.sh
```

上述命令是本项目参考实现的验证步骤，不是原题逐项编码要求。依赖已准备好时，默认路径应无需 PostgreSQL、Redis 或外部模型服务即可运行。构建工具链下限为 **Go 1.24.1**，由依赖 `storage/redis@v0.0.3` 的 go directive 传递强制，理由见 [Session 后端 Spike](session-backend.md#21-go-directive-被抬到-1241)。

### 持久化存储与双 Worker 集成（可选，需 Docker）

默认运行仍不依赖外部服务。该门控测试验证 PostgreSQL 控制面和 Session Directory、动态 PostgreSQL/Redis Session、租户 BackendProfile、Redis Session Run Lease 及双 Worker 共享链路；跳过它不影响上面的离线验收。

```bash
docker compose -f deploy/docker-compose.session.yml config
docker compose -f deploy/docker-compose.session.yml up -d --wait

TRPC_SERVICE_SESSION_INTEGRATION=1 \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
TRPC_SERVICE_REDIS_URL='redis://:trpc-local-dev@127.0.0.1:56379/0' \
go test -race -count=1 -timeout 900s ./...

docker compose -f deploy/docker-compose.session.yml down -v
```

集成测试可重复执行，两次运行互不干扰。Compose 里的口令是本地开发占位值，服务只绑定 `127.0.0.1`，不是生产 secret。语义差异与未实现边界见 [Session 后端 Spike](session-backend.md)。

启动后至少验证：

1. `GET /healthz` 返回 `200`。
2. 预置 `demo/echo` 可以带 `Authorization: Bearer` 通过 `/v1/chat/completions` 对话；缺少该请求头返回 `401`。
3. 响应头带回 `X-Session-ID` 和 `X-Agent-Revision-ID`，用回传的 Session ID 可续接同一段对话。
4. Admin API 带 `Authorization: Bearer $(cat data/admin-api-key)` 和 `Content-Type: application/json` 可以创建 Tenant、App、Revision 并发布；不带该请求头返回 `401`，且 `start.sh` 的输出里只有 key 文件的路径、没有 key 本身。
5. 发布新 Revision 后，已开始的 Session 仍返回旧版本，新建 Session 才用新版本。
6. `./stop.sh` 正常停止服务并清理 PID 文件。

## 4. Git 检查

以下前三项为历史提交记录，当前分支、远端 SHA 和权限均需在最终提交前重新确认。

- [x] 本地分支名为 `feature/d2bz`。
- [x] 个人 Fork 存在 `origin/feature/d2bz`。
- [x] 8 月 27 日提交 commit 已推送，且本地与远端 SHA 一致。
- [ ] 当前交付的分支、commit 和 GitHub 入口已核对，所需代码已推送。参赛入口为 `d2bz/trpc-agent-service` 的 `feature/d2bz`；本地已验收 `f5ed53c` 及后续部署切片尚待确认推送，不能将本地提交视为远端可用。
- [ ] 当前工作区和待提交差异已检查，无运行日志、PID、二进制、覆盖率文件或密钥进入 Git。原工作区的未提交 Channel 实验继续保留；当前部署切片在独立工作树中进行，不将原实验自动纳入交付。
- [ ] 提交平台所需的仓库、分支、文档入口和演示说明已经填写。

## 5. 当前实现边界

截至 2026-09-03 的历史记录：已实现真实 LLMAgent/Runner、Tenant/App/不可变 Revision、发布与回滚、对话面和 Admin 面独立凭据、租户 SecretRef/PolicyRef entitlement、服务端 Session Revision Pin、Runtime 缓存与生命周期、PostgreSQL 控制面、InMemory/PostgreSQL/Redis Session、Redis Session Run Lease、双 Worker 共享链路、StorageBundle Router，以及租户 BackendProfile 的 InMemory/PostgreSQL 控制面和动态 Session Factory。默认本地配置仍为 InMemory，`postgres` profile 已接入共享 Repository/Directory，Revision 可按租户 BackendProfile 动态构建 PostgreSQL/Redis Session；相关门控集成测试有历史通过记录。

2026-09-07 早期工作树历史记录：Inbox/Run/Outbox 等 Channel 实验代码当时尚未提交，跨租户碰撞测试的 Run ID 夹具已修复。该轮全仓默认 race、vet、构建和 8 项本地 HTTP/SSE 演示通过；真实 PostgreSQL/Redis 集成、外部模型和 IM 当时均未联调，真实 IM Adapter 与启动接线尚未完成。此记录对应当时含未提交代码的工作树，不代替后续提交的验收。

后续已提交网页聊天、选定 PostgreSQL Store 和企微持久消费者；`0378175` 已验证真实 Bot、真实模型与 PostgreSQL 的正常单聊，`b35215d` 收口 IM 设计，`f5ed53c` 增加默认关闭的三阶段 Span、次数和耗时。企微为单进程、单静态 Binding、单聊文本；未知发送结果不自动重发，已启动未知 Run 不重跑，旧连接目标失败。网页、企微与观测证据分别见本页第 3 节链接。

当前按原题收口设计与参考实现，旧全平台开发冻结排期不再作为必须实现的清单。飞书保留差异设计；Memory/Summary、持久 Audit、完整 Model/Tool/存储 Trace、生产部署及完整治理等未完成能力如实列入设计和风险边界，不自动进入实施。本轮只补可复现本地运行配置与说明。已知限制见[验收矩阵](acceptance.md#已知限制)，所有实现状态以可运行代码和对应版本的证据为准。
