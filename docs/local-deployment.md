# 可复现本地部署

## 本轮范围

基线 `f5ed53c`。目标是让评审者从干净检出构建并验证现有网页链路，再按需启用 PostgreSQL、企业微信单聊和三阶段 OTel。允许修改 README、文档索引、验收/提交记录、data 目录说明，以及本页、配置模板、最小 Collector 配置和环境文件启动脚本；目标净增不超过 500 行、10 个文件。

验收：构建成功；独立临时端口的默认网页 HTTP/SSE 检查通过且可停止；PostgreSQL 与环境配置步骤可复现；Collector 配置通过自身校验。真实 Bot 配置和服务保持原状，不发真实消息。原 Go 实验不进入部署验证。

不实现多角色参数、Kubernetes 部署、反向代理、TLS、媒体/卡片、飞书、Memory、完整 Trace、仪表盘或生产恢复。本轮只维护脚本、配置和文档；不能把运行示例写成生产可用性承诺。

## 环境与版本

需要 Git、Bash、curl 和 Go >= 1.24.1。配置文件启动和自动演示检查另需 Node.js >= 22；默认 `./start.sh` 不需要 Node。可选 PostgreSQL/Collector 需要 Docker Compose v2 和可用 Docker daemon。首次克隆、Go 模块下载及容器镜像下载需要网络；默认 echo 演示在构建完成后不访问外部模型、数据库或 CDN。

以 [README 快速开始](../README.md#快速开始)的个人 Fork 和 `feature/d2bz` 为交付入口。本轮新增提交尚未推送，最终交付必须补充远端提交 SHA，不能把本地验证视为远端已更新。已有本地仓库可用 `git worktree add --detach /tmp/trpc-review HEAD` 建干净检出；原工作区未提交实验不进入其中。

## 默认网页

在干净检出的项目根目录执行：

```bash
./build.sh
TRPC_SERVICE_STORAGE_PROFILE=inmemory \
TRPC_SERVICE_SESSION_COORDINATION=inmemory \
TRPC_SERVICE_WECOM_ENABLED=false \
TRPC_SERVICE_TELEMETRY_ENABLED=false \
./start.sh
curl --fail http://127.0.0.1:8080/healthz
node scripts/verify-reference.mjs http://127.0.0.1:8080
```

打开 [网页聊天](http://127.0.0.1:8080/)。预置 `demo/echo` 使用实际 LLMAgent、Runner 和 InMemory Session，模型为确定性回显，默认无需 API Key。真实模型的 SecretRef 授权和 Revision 发布步骤见 [README](../README.md#运行真实模型)。切换代码版本后重新执行 `./build.sh`，现有启动脚本只在二进制不存在时自动构建。

自动检查只用于可丢弃的 `demo/echo` 环境，会创建测试 Revision 并临时发布，退出时恢复原发布；不要对正在使用的 Bot 或业务库运行。端口占用时在启动命令前设置 `TRPC_SERVICE_ADDR=127.0.0.1:18082`，对应替换所有检查 URL；先确认该端口可用。一个检出只有一套 PID/日志文件，不在同一检出内并行启动两份。

```bash
./stop.sh
```

`stop.sh` 发送 SIGTERM 后返回，可能尚在完成 HTTP/Runner 排空；重新启动前确认旧进程和监听端口已退出。默认 InMemory 会话随退出消失，日志、Admin Key 与 PID 的位置见 [data 目录说明](../data/README.md)。

## 私密环境文件

```bash
install -m 600 deploy/local.env.example data/local.env
node scripts/start-local.mjs data/local.env
```

首次准备时才运行 `install`，再次运行会覆盖已填配置。私下编辑 `data/local.env` 后再启动；示例字段为空，不含真实 Bot 或模型密钥。`start-local.mjs` 使用 Node 的 `loadEnvFile` 按数据解析，不执行 shell 替换，不输出环境值；已导出的同名环境变量优先。文件变更需要停止旧进程后再启动，运行中的服务不会热加载。仅示例模板可提交，私密文件保持 0600 并留在已忽略的 `data/` 目录。

## 可选 PostgreSQL 与企业微信

先启动已有本地数据库配置。下列用户名、口令和端口是公开开发占位值，不能作为生产配置：

```bash
docker compose -f deploy/docker-compose.session.yml up -d --wait postgres
```

在 `data/local.env` 中把 `TRPC_SERVICE_STORAGE_PROFILE` 改为 `postgres`，沿用模板 DSN、`POSTGRES_SCHEMA=public` 和 `SESSION_COORDINATION=inmemory`。这是一进程部署，不需要 Redis。Docker daemon 若在远端，必须先提供本机可达的 PostgreSQL 端口转发；本页 localhost 指运行 Go 进程的机器。

使用独立 schema 时先创建它，再配置同名 schema。建议使用短 ASCII 名称；当前上游索引名限制使 schema 与表前缀合计最多 27 个字符，过长会在启动时拒绝。例如仅在本地开发库执行：

```bash
docker compose -f deploy/docker-compose.session.yml exec -T postgres \
  psql -U trpc -d trpc_session -v ON_ERROR_STOP=1 \
  -c 'CREATE SCHEMA IF NOT EXISTS agent_demo;'
```

服务会在所选 schema 自动建表并预置 `demo/echo`；控制面、Pin、Session 和 Inbox/Run/Outbox 共享该库。先保持 Bot 关闭验证 PostgreSQL 网页启动，再停止进程，在私密文件填写 Bot ID/Secret 并设置 `TRPC_SERVICE_WECOM_ENABLED=true`，重新运行 `node scripts/start-local.mjs`。默认 Tenant/App/Binding 已在模板列出。自定义 App 须先发布 Revision，且不能指定独立 BackendProfile。

同一 Bot 只启动一个实例；第二个连接可能接管旧连接。健康 HTTP 通过不等于机器人订阅成功，需观察用户单聊发消息后是否收到最终回复。当前只支持单聊文本，最终发送成功指平台 ACK，不是用户已读；重连后旧回复目标不补投。协议与验收边界见 [IM 收口](im-acceptance-closure.md)及[企微切片](wecom-text-slice.md)。

无需 Bot 凭据的本地协议集成测试使用临时 schema，可运行：

```bash
TRPC_SERVICE_SESSION_INTEGRATION=1 \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
go test -race -count=1 -timeout 120s ./trpcservice/channels/wecom ./trpcservice/telemetry
```

结束服务后可用 `docker compose -f deploy/docker-compose.session.yml stop postgres` 停库，保留卷；其他服务也在使用该库时保持运行。不要为本次演示执行 `down -v` 删除已有数据。

## 可选本地 Collector

```bash
docker run --rm --name trpc-otel-local \
  -p 127.0.0.1:4318:4318 \
  -v "$PWD/deploy/otel-collector.yaml:/etc/otelcol/config.yaml:ro" \
  otel/opentelemetry-collector:0.120.0 \
  --config=/etc/otelcol/config.yaml
```

在另一个终端把私密配置的 `TRPC_SERVICE_TELEMETRY_ENABLED` 改为 `true`，`TRPC_SERVICE_OTLP_ENDPOINT` 保持 `http://127.0.0.1:4318`，停止旧服务后重新启动。Collector 端口占用可改主机映射和端点；远端 Docker 同样需要本机端口可达且绑定配置文件在 daemon 所在机器可读。

仅启用网页不会产生这三个阶段的记录，当前观测消费者是已启用的企微。收到单聊并完成回复后，在 Collector 终端查看 `channel.accept`、`channel.execute`、`channel.deliver`，用 `trpc.request_id` 关联；阶段可能属于不同 Trace。计数名为 `trpc.channel.stage.count`，耗时名为 `trpc.channel.stage.duration`，单位 `ms`。Span 通常批量导出，指标默认约每 60 秒导出，也会在正常退出时刷新。此 debug exporter 仅为本地观察，不提供查询 UI 或持久历史。

Collector 失败不改变业务执行/发送结果，队列满或进程崩溃可丢遥测；指标不能作为持久账本。遥测只采集白名单，启用时 OTel 错误诊断统一为固定文字，完整细分 Trace 与生产 Collector 权限、保留期、容量策略见[观测切片](observability-slice.md)和[目标设计](solution.md#57-可观测性)。停止 Collector 可按 Ctrl-C 或在另一终端运行 `docker stop trpc-otel-local`。

## 本轮验证

2026-09-07 在隔离工作树完成，Go 1.25.12、Node 26.7.0；未修改 Go 业务实现。

- `./build.sh` 通过；从模板创建 0600 的 `data/local.env`，标准解析器启动成功，`git check-ignore` 确认不会进入版本库。环境文件缺失时固定错误且退出码 1。
- 独立 `18082` 端口运行 `node scripts/verify-reference.mjs`：健康、认证隔离、HTTP、SSE/续聊、租户断言拒绝、发布保持 Pin、Pin 冲突及回滚保持 Pin 共 8 项通过；临时服务已停止。测试工具会清理跨调用后台进程，最终验证在同一受控会话完成完整启停。
- 既有 PostgreSQL 16 开发容器中的全新临时 schema：`postgres` profile 启动、建表、预置 Revision 和 HTTP echo 通过；测试服务已停止，临时 schema 已删除。最初过长 schema 被现有配置校验拒绝，改用短名称后通过，未扩展实现。
- `docker compose ... config --quiet` 通过；官方 `otel/opentelemetry-collector:0.120.0` 对本配置执行 `validate`，退出码 0。当前 Docker 环境以 `docker cp` 注入配置完成校验，未宣称本机挂载示例或真实 Bot 到 Collector 已实测；校验容器已移除。
- `node --check scripts/start-local.mjs`、文档链接检查及 `git diff --check` 通过。未重跑业务全仓测试，本切片 Go 代码与已通过 race/vet/build 的 `f5ed53c` 相同。

发布阻断：无。风险登记：`stop.sh` 仅发信号不等待；真实 Bot 的开启/遥测仍需操作者选择时机；生产多角色、权限与持久观测不在此配置范围。当前真实网页/Bot 服务未重启，未发送新真实 IM 消息。远端推送和正式提交信息仍见[提交清单](submission-checklist.md#4-git-检查)。
