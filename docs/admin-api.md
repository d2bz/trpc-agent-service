# Admin API 与动态路由

## 1. 当前边界

当前接口用于本地开发和验收最小闭环。控制面使用 InMemory Repository，进程重启后只恢复内置 demo 配置；Admin API 尚未接入身份认证，因此服务默认绑定 `127.0.0.1`，不能直接暴露到公网。后续 PostgreSQL Repository 和管理员授权会保持本文的资源语义，但可能增加认证头、分页和审计字段。

平台启动时预置：

| 资源 | ID |
| --- | --- |
| Tenant | `demo` |
| Agent App | `echo` |
| Agent Revision | `echo-v1` |
| Model | `deterministic-echo` |

## 2. Admin 端点

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| `POST` | `/admin/v1/tenants` | 创建 Tenant |
| `GET` | `/admin/v1/tenants/{tenant_id}` | 读取 Tenant |
| `POST` | `/admin/v1/tenants/{tenant_id}/apps` | 创建 Agent App |
| `GET` | `/admin/v1/tenants/{tenant_id}/apps/{app_id}` | 读取 Agent App 和当前路由 |
| `POST` | `/admin/v1/tenants/{tenant_id}/apps/{app_id}/revisions` | 创建不可变 draft Revision |
| `GET` | `/admin/v1/tenants/{tenant_id}/apps/{app_id}/revisions/{revision_id}` | 读取 Revision |
| `POST` | `/admin/v1/tenants/{tenant_id}/apps/{app_id}/revisions/{revision_id}/publish` | 发布新版本或把已发布旧版本切回默认 |

请求体最大 1 MiB，未知 JSON 字段、多个 JSON 对象和非法资源 ID 会返回 `400`。重复 ID 或 Revision 编号返回 `409`。跨租户和不存在的资源统一返回 `404 resource not found`，避免暴露其他租户的资源是否存在。

## 3. 创建和发布 Agent

创建 Tenant：

```bash
curl -X POST http://127.0.0.1:8080/admin/v1/tenants \
  -H 'Content-Type: application/json' \
  -d '{"id":"team-a","slug":"team-a","name":"Team A"}'
```

创建 Agent App：

```bash
curl -X POST http://127.0.0.1:8080/admin/v1/tenants/team-a/apps \
  -H 'Content-Type: application/json' \
  -d '{"id":"assistant","name":"Team Assistant"}'
```

创建 Revision：

```bash
curl -X POST http://127.0.0.1:8080/admin/v1/tenants/team-a/apps/assistant/revisions \
  -H 'Content-Type: application/json' \
  -d '{
    "id":"revision-1",
    "revision_no":1,
    "created_by":"local-admin",
    "config":{
      "agent_name":"team-assistant",
      "instruction":"Answer the user request.",
      "model":{"provider":"deterministic","name":"team-echo-v1"}
    }
  }'
```

发布 Revision：

```bash
curl -X POST \
  http://127.0.0.1:8080/admin/v1/tenants/team-a/apps/assistant/revisions/revision-1/publish
```

Revision 配置在创建时计算 SHA-256 `config_digest`，之后没有修改接口。更新配置必须创建新 Revision。再次发布一个历史 `published` Revision 会把它设为默认版本并递增 App 的 `routing_version`，形成可审计的回滚操作。

## 4. 动态对话路由

对话路径仍为上游兼容的 `/v1/chat/completions`，平台路由使用以下请求头：

| Header | 必填 | 说明 |
| --- | --- | --- |
| `X-Tenant-ID` | 是 | 明确的 Tenant ID，不存在公共默认租户 |
| `X-Agent-App-ID` | 是 | Tenant 内的稳定 Agent App ID |
| `X-Agent-Revision-ID` | 否 | 指定已发布 Revision；当前用于固定版本和验收测试 |
| `X-Session-ID` | 否 | 客户端会话 ID；未提供时由上游服务生成 |

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Tenant-ID: team-a' \
  -H 'X-Agent-App-ID: assistant' \
  -H 'X-Session-ID: conversation-1' \
  -d '{
    "model":"team-echo-v1",
    "user":"user-1",
    "messages":[{"role":"user","content":"hello"}]
  }'
```

路由顺序为：

```text
Tenant Header + App Header
→ Repository 解析默认/指定的 published Revision
→ Runtime Resolver 按 (tenant, app, revision) 加载或复用 Runtime
→ OpenAI-compatible Adapter
→ Runner.Run
→ App 级共享 Session Service
```

Runtime 的框架 `AppName` 为 `t/{tenant_id}/a/{app_id}`，不包含 Revision。因此发布或回滚后，同一 Tenant、App、User 和 Session 仍读取同一段会话历史。不同 Tenant 即使使用完全相同的 App、Revision、User 和 Session ID，也会进入不同的 Session 命名空间和 Runtime 缓存键。

## 5. 尚未完成

- Admin 身份认证、租户管理员授权和管理操作审计。
- PostgreSQL Repository、事务、分页和跨节点配置通知。
- Session 目录及服务端自动 Revision Pin；当前可选 Revision Header 只用于开发和验收。
- 权重灰度、白名单路由、Runtime TTL/LRU 淘汰和配置失效通知。
- 生产模型 Provider、Secret Resolver、Tool/Knowledge/Policy 组装。
