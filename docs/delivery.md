# 当前交付入口

## 范围与版本

2026-09-08 的交付入口整理以 `29a6b906c93230d83dcace63f4239fd5740db209` 为历史参考基线。其后完成[公共文本适配器提取](im-adapter-extension.md)，并按用户追加范围推进[飞书单聊切片](feishu-text-slice.md)；各切片的代码和验证证据分别记录。原工作区实验不纳入交付，冻结的 8 月 27 日方案不修改。

原题要求完整具体的架构设计和基于设计的 GitHub 代码，不要求实现完整生产系统。评审请从本页进入[当前方案](solution.md)、[设计验收矩阵](acceptance.md#原题设计验收)和[运行说明](local-deployment.md)。[8 月 27 日正式稿](submission-2026-08-27.md)是冻结历史版本，其旧排期和实现预期不代表本次代码状态。

## 八类交付物

| 原题交付物 | 当前材料 |
| --- | --- |
| 架构设计文档 | [方案](solution.md)，含租户、拓扑、生命周期、IM、治理、安全、容量与故障恢复 |
| 系统架构图 | [架构图](architecture.md#2-系统架构图)，含 Gateway、Worker、Channel、Storage、治理与 Telemetry |
| 核心消息时序图 | [企业微信时序](sequence.md#1-企业微信完整链路)，含 Runner、Tool、Session/Memory、回复和 request/trace 关联 |
| 数据模型 | [实体及 ER 图](data-model.md)，含 tenant、agent、binding、session、event、memory、summary、audit |
| 数据同步与幂等 | [一致性设计](storage-and-consistency.md)，含并发、派生数据、入站去重、恢复和迁移 |
| 多后端方案 | [数据放置与能力矩阵](storage-and-consistency.md#2-数据放置)，覆盖 SQL、Redis、向量库、对象存储 |
| 至少八项风险及缓解 | [十二项风险](solution.md#9-主要风险)，区分当前保证、残余风险、检测/降级和生产缓解 |
| GitHub 实现代码 | 参赛 Fork `d2bz/trpc-agent-service` 的 `feature/d2bz`；既有提交与追加飞书代码均已本地验收，飞书真实收发另行记录，远端状态见下文 |

至少两类 IM 的差异由企业微信与飞书设计覆盖；飞书自建应用单聊接入的最新实现和验证见[切片记录](feishu-text-slice.md)，网页聊天是另一条参考交互入口。框架复用与新增平台职责见[能力基线](project-foundation.md#6-上游能力基线与平台新增职责)。

## 实现与证据

| 已交付的参考范围 | 验收依据与限制 |
| --- | --- |
| Tenant/App/Revision、Runtime、Runner、Session 与 HTTP/SSE | [验收矩阵](acceptance.md)保留控制面、租户隔离、Pin、Tool Policy、多后端和双 Worker 的分项证据；测试日期与适用版本分别标明 |
| 网页聊天 | [I16](acceptance.md#网页切片验收2026-09-07)，发送、SSE、停止、会话与凭据设置；页面不持久保存凭据 |
| 企业微信单聊文本 | [I17](wecom-text-slice.md#验证结果)，持久 Inbox/Run/Outbox 与真实 Runner；[真实正常单聊](wecom-text-slice.md#真实单聊验证)已有证据，故障与去重仍为本地协议验证 |
| 飞书单聊文本 | 官方 SDK 长连接复用公共文本消费者；[两轮真实正常收发](feishu-text-slice.md#真实单聊验证)已获平台确认，同 Session/Revision，一条最终回复；群聊、媒体、卡片仍为设计 |
| 可选 OTel | [`f5ed53c` 验证](observability-slice.md#验证结果)，独立三阶段 Span、次数/毫秒耗时及脱敏，按持久 request_id 关联；完整 Model/Tool/存储 Trace 未实现 |
| 本地部署 | [`29a6b90` 验证](local-deployment.md#本轮验证)，构建、8 项 HTTP/SSE、配置文件、临时 PostgreSQL schema 启动及 Collector 配置校验 |

`f5ed53c` 的全仓默认 race、vet、build 和真实 PostgreSQL 企微协议集成通过；`29a6b90` 没有改 Go 或依赖，仅补部署文档/脚本并完成上述部署检查。这些是历史版本证据，后续公共适配器和飞书切片各自记录测试，不能相互替代。完整 Memory/Summary、迁移、预算审批、动态 Binding、媒体/卡片、多角色生产部署保持设计及风险说明，不作为当前已实现能力。

## 运行与演示

干净检出并准备 Go >= 1.24.1、Bash、curl 后运行 `./build.sh` 和 `./start.sh`，打开 `http://127.0.0.1:8080/`。初次下载需网络，构建后的默认 echo 演示无需外部模型、数据库或前端构建。启停、可选数据库、私密环境文件与 OTel 步骤统一见[本地部署](local-deployment.md)。

推荐先演示网页发送/续聊，再展示真实企微和飞书单聊、三阶段本地协议测试和设计材料。飞书的[两轮真实正常收发](feishu-text-slice.md#真实单聊验证)已有证据，独立配置见同页。群聊、媒体、生产故障恢复不列入已完成演示。实际 Bot/模型密钥只由操作者本地配置，提交材料不携带它们。

## 发布状态

2026-09-08 只读核对：

- 参赛仓库为公开的 `https://github.com/d2bz/trpc-agent-service`，当前账号有推送权限。
- 远端 `feature/d2bz` 为 `ab64d81c153beb8e1dab304a0c8168203c540184`；本地参考基线 `29a6b90` 领先 12 个提交，本页所在文档提交另计。远端尚不含本轮网页/企微/观测成果。
- 题目源仓库 `liuzengh/trpc-agent-service` 当前账号无推送权限。现有 `feature/d2bz` 符合已记录的分支形态；组织者是否要求代码直接位于源仓库、姓名口径及正式提交平台填写结果仍需由提交者核对。
- 发布目标仅为个人 Fork 的 `feature/d2bz`，使用普通快进推送。当前代码已经逐切片提交，原工作区未提交实验不包含在提交中；不要用 `git add .` 将实验和运行文件混入交付。

远端发布后重新核对 `git ls-remote origin refs/heads/feature/d2bz` 与本地提交一致，并在正式提交处填写仓库、分支、最终 SHA、本页及运行说明入口。发布和赛事提单状态以[提交清单](submission-checklist.md#4-git-检查)为准。
