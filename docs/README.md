# 文档目录

本目录保存项目事实、架构决策、设计、运行和验收文档。文档必须与当前实现同步更新。

评审从[当前交付入口](delivery.md)开始，按八类交付物进入当前方案、实现证据和运行说明。

2026-09-07 起按[原题](../README.md#验收标准)收口：[验收矩阵](acceptance.md)分别记录设计覆盖与代码证据。原题要求完整设计和 GitHub 参考实现，不要求全部平台功能编码落地。旧全平台排期不再作为当前开发清单。

## 已建立

- [当前架构方案](solution.md)：当前设计主文档，区分目标架构和已验证的参考实现。
- [参赛项目背景与实现基础信息](project-foundation.md)：题目理解、项目定位、范围、角色、边界、术语、技术基线、实施阶段和完成定义。
- [总体架构设计](architecture.md)：组件职责、控制面/数据面、Agent 生命周期、路由和部署拓扑。
- [核心消息时序](sequence.md)：企业微信完整链路、并发、故障恢复和 HTTP 流式差异。
- [数据模型设计](data-model.md)：Tenant、Agent、Channel、Session、Event、Memory、Summary、Audit 等核心实体。
- [多后端、数据同步与幂等设计](storage-and-consistency.md)：后端能力、并发、一致性、幂等、迁移和降级。
- [演示与验收计划](demo-plan.md)：从当前最小链路到最终验收的场景、步骤、预期结果和证据要求。
- [Admin API 与动态路由](admin-api.md)：Tenant/App/Revision 管理接口、发布回滚和多租户对话调用方式。
- [提交检查清单](submission-checklist.md)：8 月 27 日材料提交、验收分支、验证命令和提交证据。
- [验收矩阵](acceptance.md)：题目要求到设计、代码、测试和演示证据的映射。
- [2026-09-07 设计复核与参考实现验收](verification-2026-09-07.md)：七项设计内容复核、八类交付物入口、当前工作树测试结果及交付剩余项。
- [持久化 Session 后端 Spike](session-backend.md)：上游 PostgreSQL/Redis Session 子模块的版本、兼容验证、语义差异、集成测试运行方式和未实现边界。
- [Session Run Lease](session-lease.md)：多 Worker 同 Session 的合作型 Run 租约——作用域与 key 布局、续约与 TTL 接管、HTTP 409/503 与释放规则、进程配置组合，以及"不是 enforcement fencing"这条边界。
- [Tool 与 Policy Runtime](tool-policy.md)：静态 Registry、Revision 工具白名单、模型工具循环、结构化审计、离线 SSE 闭环测试和当前授权边界。
- [身份、权限与密钥治理](security-and-governance.md)：对话面/控制面两条互不相交的凭据链路、`platform_admin`/`tenant_admin` 角色模型、Admin 请求处理顺序、Security Manifest 的严格解析、租户 SecretRef/PolicyRef entitlement、Runtime 构建顺序和发布态摘要校验，以及明确未实现的部分。
- [cc-connect IM 接入参考笔记（仅作参考）](im-reference-cc-connect.md)：飞书与企业微信协议实现的可借鉴经验、与本项目架构的边界、明确不采用的做法，以及后续实现和测试清单；不作为架构、实现、依赖或验收依据。
- [Channel 持久化流水线契约](channel-pipeline.md)：Inbox/Run/Outbox 状态机、attempt token CAS、同 Session 顺序、Redis/PostgreSQL 恢复边界、Tool 重放条件和关闭顺序。
- [IM 设计验收收口](im-acceptance-closure.md)：四项 IM 要求、真实单聊证据和设计边界。
- [最小企微可观测性](observability-slice.md)：三阶段 Span、指标、脱敏和本地验收记录。
- [可复现本地部署](local-deployment.md)：网页、私密配置、可选 PostgreSQL/企微与 Collector 的运行步骤。

## 历史材料

- [2026-08-27 正式提交方案](submission-2026-08-27.md)：冻结的历史版本，原文保持不变；其企微接入模式和旧实施排期不能替代当前方案与验收矩阵。

## 待建立

- `observability.md`：日志、指标、Trace、成本和告警规范。
- `operations.md`：故障恢复、灰度、回滚、容量和部署方案。
- `adr/`：影响多个模块或长期兼容性的架构决策记录。
