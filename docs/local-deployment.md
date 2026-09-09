# 本地部署

本指南提供单进程网页、PostgreSQL、企业微信/飞书文本和可选 Collector 的运行方式。生产节点拓扑见[架构设计](architecture.md#6-节点部署)，当前能力与验证结果见[实现与验证](acceptance.md)。

## 环境与版本

需要 Git、Bash、curl 和 Go >= 1.24.1。配置文件启动和自动演示检查另需 Node.js >= 22；默认 `./start.sh` 不需要 Node。可选 PostgreSQL/Collector 需要 Docker Compose v2 和可用 Docker daemon。首次克隆、Go 模块下载及容器镜像下载需要网络；默认 echo 演示在构建完成后不访问外部模型、数据库或 CDN。

源码与启动示例见 [README](../README.md#快速开始)。请从干净检出构建，切换版本后重新运行 `./build.sh`。

## 默认网页

在干净检出的项目根目录执行：

```bash
./build.sh
TRPC_SERVICE_STORAGE_PROFILE=inmemory \
TRPC_SERVICE_SESSION_COORDINATION=inmemory \
TRPC_SERVICE_WECOM_ENABLED=false \
TRPC_SERVICE_FEISHU_ENABLED=false \
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

首次准备时才运行 `install`，再次运行会覆盖已填配置。私下编辑 `data/local.env` 后再启动；示例字段为空，不含真实 Bot 或模型密钥。`start-local.mjs` 使用 Node 的 `loadEnvFile` 按数据解析，不执行 shell 替换，不输出环境值；已导出的同名环境变量优先。多个文件按参数顺序读取，较早文件的值优先（包括空值），因此凭据文件放在带空占位字段的模板配置之前。文件变更需要停止旧进程后再启动，运行中的服务不会热加载。仅示例模板可提交，私密文件保持 0600 并留在已忽略的 `data/` 目录。

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

同一 Bot 只启动一个实例；第二个连接可能接管旧连接。健康 HTTP 通过不等于机器人订阅成功，需观察用户单聊发消息后是否收到最终回复。当前只支持单聊文本，最终发送成功指平台 ACK，不是用户已读；重连后旧回复目标不补投。协议与恢复边界见 [IM 接入指南](im-channels.md)。

无需 Bot 凭据的本地协议集成测试使用临时 schema，可运行：

```bash
TRPC_SERVICE_SESSION_INTEGRATION=1 \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
go test -race -count=1 -timeout 120s ./trpcservice/channels/wecom ./trpcservice/telemetry
```

结束服务后可用 `docker compose -f deploy/docker-compose.session.yml stop postgres` 停库，保留卷；其他服务也在使用该库时保持运行。`down -v` 会删除已有数据，需要保留数据时请使用 `stop`。

## 可选飞书

沿用 PostgreSQL 单进程配置，并先发布绑定 App 的 Revision；当前 IM 只支持进程默认的持久 Session/Pin，不接受独立 BackendProfile。飞书与企微同时启用时，同租户须使用不同 Binding ID。

在 `data/local.env` 中设置：

```dotenv
TRPC_SERVICE_FEISHU_ENABLED=true
TRPC_SERVICE_FEISHU_TENANT_ID=demo
TRPC_SERVICE_FEISHU_AGENT_APP_ID=echo
TRPC_SERVICE_FEISHU_BINDING_ID=feishu-local
```

另建私密 `data/feishu.env`（权限 `0600`），只填写：

```dotenv
TRPC_SERVICE_FEISHU_APP_ID=
TRPC_SERVICE_FEISHU_APP_SECRET=
```

`AGENT_APP_ID` 是服务内部应用，`APP_ID` 是飞书应用，两者不能混用。执行只读身份预检，再从已构建的同一检出启动：

```bash
node scripts/check-feishu.mjs data/feishu.env
node scripts/start-local.mjs data/feishu.env data/local.env
```

预检读取 dotenv 数据、固定访问飞书官方地址并拒绝重定向，15 秒超时，仅输出 HTTP 状态、业务码和布尔检查，不输出凭据或外部身份；任一校验失败退出 1。预检不会建立长连接或发送消息。

| 飞书后台项目 | 配置 |
| --- | --- |
| 应用能力 | 企业自建应用，开启机器人 |
| 单聊接收 | `im:message.p2p_msg:readonly` |
| 机器人发送 | `im:message:send_as_bot` |
| 企业身份查询 | `tenant:tenant:readonly`，用于本实现额外的企业绑定校验 |
| 应用身份事件 | 只订阅 `im.message.receive_v1` |
| 接收方式 | 使用长连接；保存时必须有 SDK 客户端在线 |
| 可用范围 | 包含实际测试用户 |

日志中的固定 `channel feishu connected` 表示 SDK 已建立过连接。客户端在线后，在后台保存长连接方式、添加事件、创建版本并发布，使权限和订阅生效；普通企业自建应用可能需要管理员审核。官方测试版应用有自动生效例外。之后由测试用户私聊机器人发送文本，检查正常回复与持久 Run/Outbox 状态。

同一飞书 App 只运行一个实例，多连接可能分摊事件。HTTP 健康、凭据有效、长连接建立和真实收发是不同检查，不能互相替代。无需公网 Webhook URL，不需要用户 OAuth、群消息或卡片权限。

官方依据：[接收消息](https://open.feishu.cn/document/server-docs/im-v1/message/events/receive)、[回复消息](https://open.feishu.cn/document/server-docs/im-v1/message/reply)、[长连接配置](https://open.feishu.cn/document/server-docs/event-subscription-guide/event-subscription-configure-/request-url-configuration-case)、[应用可用范围](https://open.feishu.cn/document/home/introduction-to-scope-and-authorization/availability)。

## 可选本地 Collector

```bash
docker run --rm --name trpc-otel-local \
  -p 127.0.0.1:4318:4318 \
  -v "$PWD/deploy/otel-collector.yaml:/etc/otelcol/config.yaml:ro" \
  otel/opentelemetry-collector:0.120.0 \
  --config=/etc/otelcol/config.yaml
```

在另一个终端把私密配置的 `TRPC_SERVICE_TELEMETRY_ENABLED` 改为 `true`，`TRPC_SERVICE_OTLP_ENDPOINT` 保持 `http://127.0.0.1:4318`，停止旧服务后重新启动。Collector 端口占用可改主机映射和端点；远端 Docker 同样需要本机端口可达且绑定配置文件在 daemon 所在机器可读。

仅启用网页不会产生这三个阶段的记录，当前观测接入公共文本消费者，可随企微或飞书启用；已有观测集成证据使用模拟平台，未宣称真实 Bot 到 Collector 已实测。收到单聊并完成回复后，在 Collector 终端查看 `channel.accept`、`channel.execute`、`channel.deliver`，用 `trpc.request_id` 关联；阶段可能属于不同 Trace。计数名为 `trpc.channel.stage.count`，耗时名为 `trpc.channel.stage.duration`，单位 `ms`。Span 通常批量导出，指标默认约每 60 秒导出，也会在正常退出时刷新。此 debug exporter 仅为本地观察，不提供查询 UI 或持久历史。

Collector 失败不改变业务执行/发送结果，队列满或进程崩溃可丢遥测；指标不能作为持久账本。遥测只采集白名单，启用时 OTel 错误诊断统一为固定文字，完整细分 Trace 与生产 Collector 权限、保留期、容量策略见[目标设计](local-deployment.md#生产观测设计)。停止 Collector 可按 Ctrl-C 或在另一终端运行 `docker stop trpc-otel-local`。

## 观测边界

三个阶段使用独立 SDK TracerProvider/MeterProvider，不注册全局 provider，也不启用上游自动捕获模型/工具内容的 tracing。Span 只记录内部 Tenant/App/Binding、request/run/outbox、固定阶段/结果/错误类别、attempt 和耗时；不记录 Principal、Session、外部账号/消息、正文、目标载荷、工具参数或原始错误。指标标签仅为静态租户/App及有限阶段分类，不使用请求 ID。

启用观测时会安装进程级 `otel.SetErrorHandler`，将导出器诊断统一为固定文本；这会同时减少其他 OTel 诊断的详情。导出使用有界队列和超时，失败不改变业务结果或尝试次数，关闭在业务排空后 flush。阶段关联依赖持久 request_id，不承诺同一连续父子 Trace，指标也不是持久账本。

## 生产观测设计

目标设计中 OpenTelemetry Span 覆盖 Channel、Gateway、Run、Model、Tool、Session、Memory 和 Outbox。核心指标包括每租户请求量、并发 Run、模型/Tool/后端延迟、错误率、IM 投递成功率、token、费用和队列等待时间。审计记录包含 `tenant_id`、`channel`、`user_id`、`session_id`、`agent_name`、`tool_name`、`decision`、`latency`、`error_type`、`cost` 和 `trace_id`。

| 指标 | 采集点和统计口径 |
| --- | --- |
| 新请求与重复入站 | Inbox 首次提交计一个新请求，重复命中单独计数；持久化内部重试不能重复增加受理次数 |
| 阶段、模型、Tool、后端与排队耗时 | 各阶段起止点记录带明确单位的直方图，区分等待与实际调用；失败样本保留 outcome，不能只统计成功耗时。当前阶段指标使用毫秒 `ms` |
| IM 投递结果 | 平台明确成功 ACK / 实际发送尝试数为成功率；拒绝、未知、目标过期分开。Outbox 状态写入重试不是新发送，ACK 不表示用户已读 |
| 错误率与并发 | 错误阶段数 / 同阶段处理总数；活跃 Run 为已开始且未结束数量，不能将排队数计作运行并发 |
| token 与租户成本 | 模型返回的 usage 按输入/输出及供应商语义记录，以模型与价格版本计算费用；缺失 usage 或价格时标记未知，不填零。跨租户查询由受控成本明细按租户聚合 |

完整 Trace 为目标设计：受理端创建上下文，将 W3C `traceparent` 随 Inbox/Run/Outbox 持久化，Worker/发送器恢复上下文，Runner、Model、Tool 与存储适配器传递同一 Context；异步派生 Memory 使用父上下文或 Span Link 关联。当前表结构没有持久 Trace 上下文字段。

当前[可选观测](local-deployment.md#观测边界)使用独立 provider，在受理、执行、发送三个阶段生成 Span、次数与耗时，以持久 `request_id` 关联；阶段可能属于不同 Trace，尚未实现跨队列连续父子 Trace、Model/Tool/Session/Memory 细分采集或成本统计。指标标签只用有限阶段、通道、结果类别及静态绑定的内部租户/App；request/user/session 等高基数 ID 不进入指标。观测只采集白名单，上游自动 tracing 保持关闭，具体结果见[实现与验证](acceptance.md#验证结果)。

当前 `trpc.channel.stage.count` 统计阶段处理次数，`trpc.channel.stage.duration` 记录对应毫秒耗时。`execute` 的结果是本次执行决策，不能替代持久 Run 终态；`deliver` 还含发送前跳过和旧目标记录，计算实际投递率时须区分 outcome，不直接用全部阶段次数作分母。启用时 OTel 进程级错误处理器只输出固定诊断，避免 SDK 将 Collector 原文写入日志；它不安装全局 provider，但会影响进程中其他 OTel 错误的诊断详细度，关闭遥测时不安装。

生产 Collector 使用内网认证写入与租户授权查询，按策略配置保留期、采样和有界导出队列；Secret 解析、第三方诊断及统一脱敏出口的约束见[Secret 与遥测出口](security-and-governance.md#113-secret-与遥测出口)。这些生产管控尚未实现，本地 debug Collector 不提供相同保证。

## 容量估算

容量以实测 P95 模型延迟、平均 Tool 次数和消息大小校准。初始估算示例：

- 单 Worker 允许 100 个并发 Run，平均一轮 15 秒，则理论吞吐约 `100 / 15 = 6.7 RPS`；按 60% 安全水位规划约 4 RPS。
- 峰值 20 RPS 时，Worker 数量至少为 `ceil(20 / 4) = 5`，再增加 1 个故障冗余，共 6 个。
- 每轮平均写入 6 个 Event，则 Session Backend 峰值写 QPS 约 `20 × 6 = 120`，按两倍突发准备 240 QPS。
- 每轮输入输出合计 4,000 token，20 RPS 时模型消耗约 `80,000 token/s`，必须按租户和模型供应商设置预算与限速。
- IM 回调按日均峰值系数 10 估算，Inbox 和 Gateway 保留至少两倍突发余量。以上数值是容量规划假设，不是实测吞吐；部署前需通过压测校准。
