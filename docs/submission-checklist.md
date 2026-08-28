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
go test -race ./trpcservice/config ./trpcservice/tenant ./trpcservice/identity ./trpcservice/sessiondir ./trpcservice/agent ./trpcservice/web ./cmd/trpc-service
go vet ./...
./build.sh
./start.sh
```

启动后至少验证：

1. `GET /healthz` 返回 `200`。
2. 预置 `demo/echo` 可以带 `Authorization: Bearer` 通过 `/v1/chat/completions` 对话；缺少该请求头返回 `401`。
3. 响应头带回 `X-Session-ID` 和 `X-Agent-Revision-ID`，用回传的 Session ID 可续接同一段对话。
4. Admin API 可以创建 Tenant、App、Revision 并发布。
5. 发布新 Revision 后，已开始的 Session 仍返回旧版本，新建 Session 才用新版本。
6. `./stop.sh` 正常停止服务并清理 PID 文件。

## 4. Git 检查

- [x] 本地分支名为 `feature/d2bz`。
- [x] 个人 Fork 存在 `origin/feature/d2bz`。
- [x] 8 月 27 日提交 commit 已推送，且本地与远端 SHA 一致。
- [x] 工作区干净，无运行日志、PID、二进制、覆盖率文件或密钥进入 Git。
- [ ] 提交平台所需的仓库、分支、文档入口和演示说明已经填写。

## 5. 当前实现边界

截至方案 `1.0`，已实现最小 Runner 链路、Tenant/App/Revision 内存控制面、Admin API、动态 Runtime 路由、版本发布/回滚和多租户隔离测试。此后追加了对话面的静态 API Key 认证、服务端决定的会话归属和进程内 Session Revision Pin。PostgreSQL、Redis 共享 Session、跨进程 Pin、Admin 认证、双 Worker、IM Adapter、治理、Telemetry 和生产部署仍在后续计划中，已知限制见[验收矩阵](acceptance.md#已知限制)。方案提交不改变这些功能的 `planned/partial` 状态。
