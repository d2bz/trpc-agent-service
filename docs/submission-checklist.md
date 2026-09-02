# 提交检查清单

## 1. 交付标识

| 项目 | 值 |
| --- | --- |
| 题目源仓库 | `liuzengh/trpc-agent-service` |
| 参赛仓库 | `d2bz/trpc-agent-service` |
| 当前工作分支 | `feature/d2bz`（验收名称待组织者确认） |
| 方案版本 | `1.0` |
| 方案提交日期 | 2026-08-27 |
| 最终验收日期 | 2026-09-11 |

当前 GitHub 账号对题目源仓库没有推送权限，开发提交先推送至个人 Fork。组织者要求最终分支采用 `feature/{your_name}`，但 `your_name` 的具体口径尚待确认；确认后可无损重命名当前分支。若最终分支必须直接位于题目源仓库，需要先将 `d2bz` 加为协作者。

## 2. 方案材料

- [x] 2000–4000 字正式方案：`submission-2026-08-27.md` 中文正文 3288 字。
- [x] 系统架构图：正式方案内 Mermaid 源码由 CLI 11.12.0 渲染与视觉检查通过。
- [x] 企业微信核心时序图：正式方案内 Mermaid 源码已渲染检查。
- [x] 数据模型与 ER 图：位于 `data-model.md`，ER 图已渲染检查。
- [x] 数据同步、幂等、多后端和迁移策略：位于 `storage-and-consistency.md`。
- [x] 至少 8 个生产风险：总稿列出 12 项。
- [x] tRPC-Agent-Go 复用能力与新增平台层职责：位于 `project-foundation.md`。
- [x] 演示步骤和验收证据规范：位于 `demo-plan.md`。

## 3. 提交前验证

```bash
git status --short --branch
git diff --check
go test ./...
go test -race -count=1 ./trpcservice/config ./trpcservice/tenant ./trpcservice/identity ./trpcservice/security ./trpcservice/secretref ./trpcservice/sessiondir ./trpcservice/sessionbackend ./trpcservice/tool ./trpcservice/agent ./trpcservice/web ./cmd/trpc-service
go vet ./...
./build.sh
./start.sh
```

上述命令在没有 PostgreSQL、Redis 和网络的机器上必须全部通过。构建工具链下限为 **Go 1.24.1**，由依赖 `storage/redis@v0.0.3` 的 go directive 传递强制，理由见 [Session 后端 Spike](session-backend.md#21-go-directive-被抬到-1241)。

### 持久化 Session 后端 Spike（可选，需 Docker）

该 Spike 不改变服务默认行为（仍为 InMemory Session），只验证上游 PostgreSQL/Redis Session 子模块。跳过它不影响上面的验收。

```bash
docker compose -f deploy/docker-compose.session.yml config
docker compose -f deploy/docker-compose.session.yml up -d --wait

TRPC_SERVICE_SESSION_INTEGRATION=1 \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
TRPC_SERVICE_REDIS_URL='redis://:trpc-local-dev@127.0.0.1:56379/0' \
go test -race -timeout 120s ./trpcservice/sessionbackend/...

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

- [x] 本地分支名为 `feature/d2bz`。
- [x] 个人 Fork 存在 `origin/feature/d2bz`。
- [x] 8 月 27 日提交 commit 已推送，且本地与远端 SHA 一致。
- [x] 工作区干净，无运行日志、PID、二进制、覆盖率文件或密钥进入 Git。
- [ ] 提交平台所需的仓库、分支、文档入口和演示说明已经填写。

## 5. 当前实现边界

截至方案 `1.0`，已实现最小 Runner 链路、Tenant/App/Revision 内存控制面、Admin API、动态 Runtime 路由、版本发布/回滚和多租户隔离测试。此后追加了对话面的静态 API Key 认证、服务端决定的会话归属和进程内 Session Revision Pin。另有一次持久化 Session 后端 Spike：`trpcservice/sessionbackend` 能构造 PostgreSQL/Redis 的 `session.Service` 并通过集成测试，但**默认后端仍是 InMemory，进程中没有任何代码使用持久化后端**。此后又追加了控制面身份与租户 entitlement：Admin 面有独立于对话面的静态凭据和 `platform_admin`/`tenant_admin` 角色模型，SecretRef/PolicyRef 按租户授权，发布态 Revision 的摘要在构建时重算校验，规则见[身份、权限与密钥治理](security-and-governance.md)。共享 Session、IM Adapter、Telemetry 和生产部署仍在后续计划中；安全侧仍未实现 JWT/OIDC、动态 RBAC、清单热加载、凭据轮转/撤销、持久化管理审计、生产 Secret Manager 和预算/审批/Guardrail。已知限制见[验收矩阵](acceptance.md#已知限制)。方案提交不改变这些功能的 `planned/partial` 状态。
