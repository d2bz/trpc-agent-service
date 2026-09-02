# Admin API 与动态路由

## 1. 当前边界

当前接口用于本地开发和验收最小闭环。控制面默认使用 InMemory Repository，也可通过 `TRPC_SERVICE_STORAGE_PROFILE=postgres` 使用 PostgreSQL；Admin API 尚未接入身份认证，因此服务默认绑定 `127.0.0.1`，不能直接暴露到公网。管理员授权会保持本文的资源语义，但可能增加认证头、分页和审计字段。

对话面已经要求 Bearer 凭据（见第 4 节），Admin 面没有。两者的安全边界不同：**Admin 未认证这一条决定了整个进程仍然只能跑在本机**，对话面的认证不改变这个结论。

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

对话路径仍为上游兼容的 `/v1/chat/completions`。租户、主体和 Session 归属全部由服务端从凭据推导，请求体中的 `user` 字段被忽略。

| Header | 必填 | 说明 |
| --- | --- | --- |
| `Authorization` | 是 | `Bearer {api_key}`；平台由此确定 Tenant、Principal 和可用 App |
| `X-Agent-App-ID` | 是 | Tenant 内的稳定 Agent App ID，必须在凭据授权范围内 |
| `X-Tenant-ID` | 否 | 只是客户端对自己凭据的断言；与认证结果不一致返回 `403` |
| `X-Agent-Revision-ID` | 否 | 仅对**新 Session 的首轮**生效的开发用指定版本；对已 Pin 的 Session 只能重复相同值 |
| `X-Session-ID` | 否 | 客户端会话 ID；缺失时由平台生成 UUID 并在响应头回传 |

响应头（成功和 Adapter 返回的 `400`/`500` 都会带上）：

| Header | 说明 |
| --- | --- |
| `X-Session-ID` | 本次实际使用的 Session ID，客户端用它续接同一段对话 |
| `X-Agent-Revision-ID` | 本次实际执行的 Revision，即该 Session 的 Pin |

```bash
curl -i http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer local-development-key-not-a-secret' \
  -H 'X-Agent-App-ID: echo' \
  -H 'X-Session-ID: conversation-1' \
  -d '{
    "model":"deterministic-echo",
    "messages":[{"role":"user","content":"hello"}]
  }'
```

路由顺序为：

```text
OPTIONS 预检直接返回（不认证）
→ 解析 Bearer 并认证，得到 {tenant_id, principal_id, allowed_app_ids}
→ 校验 X-Tenant-ID 断言（不一致 403）
→ 校验 X-Agent-App-ID 并检查授权（越权 403）
→ 校验或生成 X-Session-ID
→ Session Directory 查 Pin；没有 Pin 则解析候选 Revision 并原子 EnsurePin
→ Runtime Resolver 按 (tenant, app, pinned revision) 加载或复用 Runtime
→ 写入响应头，注入可信 RunContext
→ OpenAI-compatible Adapter
→ contextRunner（丢弃协议层 userID/sessionID）
→ Runner.Run(userID = u/{principal_id}, sessionID = 已 Pin 的 Session)
→ App 级共享 Session Service
```

错误语义：

| 情况 | 状态码 | `code` |
| --- | --- | --- |
| 缺少或错误的 Bearer 凭据 | `401`（带 `WWW-Authenticate: Bearer`） | `unauthenticated` |
| `X-Tenant-ID` 断言不符，或 App 不在凭据授权内 | `403` | `forbidden` |
| 缺少或非法 `X-Agent-App-ID` | `400` | `missing_route` |
| 非法 `X-Session-ID` | `400` | `invalid_session_id` |
| 已 Pin 的 Session 收到不同的 `X-Agent-Revision-ID` | `409` | `pin_conflict` |
| Resolver 已关闭 | `503` | `runtime_unavailable` |

Session 的 Revision Pin 在首轮请求中原子写入，键为 `{tenant_id, app_id, principal_id, session_id}`。因此：发布新版本不会改变已经开始的 Session，回滚同样不会；新 Session 才会采用当前默认版本。并发首轮只有一个候选能成为 Pin，落败的请求会释放自己的 Runtime 租约并改用胜出的 Revision。

Runtime 的框架 `AppName` 为 `t/{tenant_id}/a/{app_id}`，不包含 Revision。因此发布或回滚后，同一 Tenant、App、Principal 和 Session 仍读取同一段会话历史。不同 Tenant 即使使用完全相同的 App、Revision、Principal 和 Session ID，也会进入不同的 Session 命名空间和 Runtime 缓存键。

CORS 预检允许 `Authorization`、`X-Tenant-ID`、`X-Agent-App-ID`、`X-Agent-Revision-ID`、`X-Session-ID`，并向浏览器暴露 `X-Session-ID` 和 `X-Agent-Revision-ID`。预检本身不认证，因为浏览器不会在预检请求上附带凭据。

## 5. 尚未完成

- Admin 身份认证、租户管理员授权和管理操作审计。
- PostgreSQL Repository、事务、分页和跨节点配置通知。
- 跨进程 Session 目录：默认 profile 下 Pin 只在单进程内存中，多节点部署会各自 Pin；`postgres` profile 下 Pin 落库，这一条才不成立。
- 主体间共享 Session、显式 Retire/Unpin（`Key.Epoch` 已预留但恒为 0）。Redis Run 租约已实现（见 [Session Run Lease](session-lease.md)），但它只做 Run 入口的合作型互斥，不做写入准入。
- 静态 API Key 之外的凭据体系：轮转、过期、撤销、按 Principal 的配额与限流。
- 权重灰度、白名单路由、Runtime TTL/LRU 淘汰和配置失效通知。
- `openai-compatible` 模型 Provider 已支持运行时构造。Revision 的模型配置需要 `base_url`，可用 `secret_ref: "env:VAR_NAME"` 从 Worker 环境解析 API Key；未设置 `secret_ref` 表示无凭据调用。因为 `base_url` 由租户的 Revision 指定，上游 openai-go 从 Worker 进程环境派生的请求头都会被显式删除，具体是且仅是这三个：

  | 请求头 | 环境变量 | 删除时机 |
  | --- | --- | --- |
  | `OpenAI-Organization` | `OPENAI_ORG_ID` | 总是 |
  | `OpenAI-Project` | `OPENAI_PROJECT_ID` | 总是 |
  | `Authorization` | `OPENAI_API_KEY` | 仅 `secret_ref` 为空时；显式 `secret_ref` 保留自己解析出的那一个 |

  前两个是运营方 OpenAI 账号的标识，Revision schema 不建模这两个值，所以无论是否配置 `secret_ref` 都删除。`OPENAI_WEBHOOK_SECRET` 是 openai-go 的第四个环境默认值，但它不设置请求头，因此不在此列。这只是这一个客户端上的请求头抑制，**不等于**进程级 Secret 隔离：进程环境仍然可读，显式 `env:VAR_NAME` 仍会为引用它的 Revision 解析。Secret Manager、Tool/Knowledge/Policy 组装仍未完成。

## 6. 已知限制

- **合法凭据可以制造无界内存 Session。** Session 目录和 Session Service 都在内存中，且没有配额或 TTL，一个持有有效 key 的调用方可以用无限多的 `X-Session-ID` 撑爆进程内存。
- **首轮 OpenAI 历史可以伪造。** 平台只决定 Session 归属，不校验请求体里的 `messages`。新 Session 的第一轮里，调用方可以自行编造一段"历史对话"送进模型上下文。后续轮次会与服务端存储的 Session 事件合并，但首轮注入无法阻止。
- **Adapter 拒绝的请求也会建立 Pin。** Session ID 和 Revision 在调用上游 Adapter 之前就已确定并写入响应头，因此一个 JSON 格式错误的首轮请求同样会把该 Session 钉在当时的 Revision 上。
- **`secret_ref` 的 `env:VAR_NAME` 没有租户授权和白名单。** 解析器只检查名字是不是合法的环境变量名，不检查这个 Revision 所属的租户有没有权限引用这个变量。因此任何能发布 Revision 的主体，都可以引用 Worker 进程里的**任意**环境变量，并把解析结果当作 `Authorization` 发往自己在 `base_url` 里指定的地址——上一节删除的是进程环境**默认**派生的请求头，拦不住一次显式的引用。今天之所以安全，只是因为 Admin 面仍然是 loopback-only 且未认证的本地控制面，能发布 Revision 的就是运行这个进程的人。**在把 Admin 面暴露给租户管理员之前，必须先解决这一条**：至少需要按租户的变量白名单，或者直接换成带授权的 Secret Manager 引用。
- 对话面认证只覆盖 `/v1/chat/completions`。**不能据此认为平台整体达到生产安全标准。**
