# 文档目录

本目录保存项目事实、架构决策、设计、运行和验收文档。文档必须与当前实现同步更新。

## 已建立

- [2026-08-27 正式提交方案](submission-2026-08-27.md)：本次方案提交的唯一主文档，包含架构图、核心时序、数据模型、重点技术、容量、计划和风险。
- [方案设计底稿](solution.md)：方案形成过程中的设计底稿和详细支撑入口。
- [参赛项目背景与实现基础信息](project-foundation.md)：题目理解、项目定位、范围、角色、边界、术语、技术基线、实施阶段和完成定义。
- [总体架构设计](architecture.md)：组件职责、控制面/数据面、Agent 生命周期、路由和部署拓扑。
- [核心消息时序](sequence.md)：企业微信完整链路、并发、故障恢复和 HTTP 流式差异。
- [数据模型设计](data-model.md)：Tenant、Agent、Channel、Session、Event、Memory、Summary、Audit 等核心实体。
- [多后端、数据同步与幂等设计](storage-and-consistency.md)：后端能力、并发、一致性、幂等、迁移和降级。
- [演示与验收计划](demo-plan.md)：从当前最小链路到最终验收的场景、步骤、预期结果和证据要求。
- [Admin API 与动态路由](admin-api.md)：Tenant/App/Revision 管理接口、发布回滚和多租户对话调用方式。
- [提交检查清单](submission-checklist.md)：8 月 27 日材料提交、验收分支、验证命令和提交证据。
- [验收矩阵](acceptance.md)：题目要求到设计、代码、测试和演示证据的映射。
- [持久化 Session 后端 Spike](session-backend.md)：上游 PostgreSQL/Redis Session 子模块的版本、兼容验证、语义差异、集成测试运行方式和未实现边界。

## 待建立

- `security-and-governance.md`：身份、权限、密钥、Guardrail、预算、脱敏和审计。
- `observability.md`：日志、指标、Trace、成本和告警规范。
- `operations.md`：故障恢复、灰度、回滚、容量和部署方案。
- `adr/`：影响多个模块或长期兼容性的架构决策记录。
