# 身份、权限与密钥治理

本文描述当前**已实现**的安全切片：控制面认证、角色模型、Security Manifest、租户 Entitlement 和 Runtime 构建顺序。所有描述以 `trpcservice/identity`、`trpcservice/security`、`trpcservice/secretref`、`trpcservice/web/admin.go`、`trpcservice/agent/agent.go` 和 `start.sh` 的源码与测试为准。第 10 节列出仍未实现的能力，不能在验收中被当作已解决。

## 1. 一句话边界

- 控制面（Admin API）和对话面（`/v1/chat/completions`）都要求 Bearer 凭据，且**使用两套互不相通的凭据体系**。
- 凭据是静态的：进程启动时一次读入，运行期不可变更，没有热加载、轮转、过期或撤销。
- 授权粒度是**角色 + 租户**（`platform_admin` / `tenant_admin`）和**租户 Entitlement**（某租户的 Revision 可以引用哪些 SecretRef 和 PolicyRef），不是动态 RBAC。
- 进程仍然只允许绑定回环地址。原因已经变了：不再是"Admin 未认证"，而是本进程只服务明文 HTTP，可路由的监听地址会把 Admin Bearer token 明文放到网络上；且 demo profile 仍然可以用公开的开发 chat key 启动。TLS 终止属于反向代理，不属于本二进制的环境变量。

## 2. 两条凭据链路

| | Chat Key | Admin Key |
| --- | --- | --- |
| 认证接口 | `identity.Authenticator.Authenticate` | `identity.AdminAuthenticator.AuthenticateAdmin` |
| 身份类型 | `identity.Identity`（tenant + principal + allowed_app_ids） | `identity.AdminIdentity`（role + principal + tenant） |
| 实现 | `identity.StaticAPIKeyAuthenticator` | `identity.StaticAdminAPIKeyAuthenticator` |
| 可达路径 | `/v1/chat/completions` | `/admin/**` |
| 最短长度 | 16 | 32 |
| Manifest `purpose` | `chat` | `platform_admin` / `tenant_admin` |

两者是**不同的 Go 类型和不同的方法名**，`PlatformServer` 也用两个独立字段持有。因此"chat key 能不能调 Admin"不是某个 handler 里的一次比较，而是类型系统的结论：没有任何一个值能同时满足两个接口，一个 chat key 送到 Admin 面只会命中 admin 摘要表的 miss，返回 `401`。测试见 `identity.TestAdminAndChatAuthenticatorsAreDistinctTypes` 与 `web.TestAdminAuthenticatesBeforeRouting`。

Admin Key 的下限是 chat 的两倍，这是有意的：一个 chat key 只能和一个租户的一个 App 对话，一个 admin key 能创建租户、发布 Revision，并决定平台执行什么。已有 chat key 的下限不上调（会拒绝正在使用的凭据），admin key 是新的，所以从一开始就定在该在的位置。

### 2.1 凭据的长期存储与可传输性

两个静态认证器都**只长期保存 key 的 SHA-256 摘要**，明文在构造过程中用完即弃：

- 长期 map 里不存在可被内存转储或误打印结构体泄漏的凭据。
- 查表按摘要进行，消除了逐字节字符串比较的前缀时间信号。

两个认证器都会在**构造时**拒绝无法可靠作为 Bearer 传输的 key（`identity/credential.go`）：

- 空 key；
- 首尾带空白的 key——`Authorization` 头在解析时会被 trim 两次，配置的 key 和到达的 key 不是同一个字符串，摘要永远对不上；
- 含有不能出现在 HTTP 头值里的字节（CR、LF、NUL、DEL）。

规则刻意收窄到"可证明不可能工作"的集合，不是 RFC 6750 token68：`:` 和 `|` 不是 token68 字符但出现在真实 API key 里且逐字节传输正常，因此不拒绝。这条检查不可能拒掉一个今天能用的 key（`identity.TestStaticAuthenticatorsAcceptOrdinaryKeys`），但能把"进程正常启动、日志报告凭据数量正确、然后对着刚刚报告的 admin key 返回 401"这种故障提前成一条启动错误。长度检查在它之后：32 个空格能过长度下限却谁也认证不了，而"你的 key 是空白"才是有用的那一半信息。

## 3. Admin 角色模型

`AdminRole` 是**闭集**，manifest 不能发明新角色：

| 角色 | 租户绑定 | 可做 |
| --- | --- | --- |
| `platform_admin` | 不属于任何租户，且**必须**不携带 `tenant_id` | 创建租户；管理任意租户的 App / Revision |
| `tenant_admin` | 绑定且仅绑定一个租户 | 管理自己租户的 App / Revision |

`AdminIdentity.Validate` 拒绝任何无法完整定界的身份：带租户的 `platform_admin` 会读起来像"有作用域"却实际无作用域，因此直接判非法；`tenant_admin` 必须有合法 `tenant_id`。`AllowsTenant` 做**精确字符串比较**，没有前缀、通配或大小写折叠。

创建租户是唯一不属于任何租户的控制面操作，因此归属唯一不属于任何租户的角色：`tenant_admin` 调 `POST /admin/v1/tenants` 得到 `403 forbidden`（`web.TestAdminTenantCreationIsPlatformAdminOnly`）。这里用 `403` 而不是 `404`，是因为"我不能建租户"对这个调用方本来就不是秘密。

## 4. Admin 请求处理顺序

整个 `/admin` 子树**在 mux 之前**被 `adminFirst` 接管，然后由 `handleAdmin` 按下列顺序处理：

```text
1. 认证 Bearer 凭据                       → 401 unauthenticated（带 WWW-Authenticate: Bearer）
2. POST 必须声明 Content-Type: application/json → 415 unsupported_media_type
3. 路径前缀匹配（原始 URL.Path，精确比较）  → 404 not_found（admin route not found）
4. 租户作用域检查（路径含 tenant 时）       → 404 not_found（resource not found）
5. 方法匹配                                → 405 method_not_allowed（带 Allow）
6. 角色检查（仅 POST /admin/v1/tenants）    → 403 forbidden
7. Entitlement / Tool Registry / Repository
```

**认证在最前面**是这套设计的核心。没有有效凭据的调用方，在真实路由、不存在的路由和错误方法上都得到同一个 `401`，因此这个 API 不回答任何关于自己形状的问题。路由发生在认证之后，也意味着下游没有任何 handler 能被一个尚未被识别的调用方到达。

`/admin` 子树没有注册在 `http.ServeMux` 上，而是在 mux 之前拦截，原因是 `ServeMux` 会先清理路径：`/admin//v1/tenants`、`/admin/./v1/tenants`、`/admin/v1/tenants/../secrets` 都会得到一个 `301`，而这个重定向是写给一个**没有凭据**的调用方的。现在这些路径先认证，然后按原始路径精确比较——不解析任何 traversal，所以"拼写奇怪的真实路由"不是那条路由，而是一个 `404`（`web.TestAdminOddPathsAreRefusedBeforeTheRouterAnswers`、`TestAdminOddPathsAreNotRedirectedForAValidCredential`）。非 `/admin` 流量照常进 mux（`web.TestNonAdminTrafficStillRoutesThroughTheMux`）。

### 4.1 跨租户统一 404，且零 Repository 调用

`tenant_admin` 访问别的租户时，在**任何 Repository 调用之前**返回和"资源真的不存在"**逐字节相同**的 `404 not_found`。两半都重要：

- 状态码或响应体只要差一个词，这个接口就成了枚举租户是否存在的 oracle。因此 `writeNotFound`（`404 not_found` / `resource not found`）是**唯一一个资源级** not-found 写出口，跨租户短路和 `ErrTenantScope` / `ErrNotFound` 共用它，逐字节相同。路由层的 `admin route not found` 是另一条消息，但它只回答"这个 URL 形状不是一条路由"，与任何租户是否存在无关，而且同样在认证之后。
- 先到 Repository 再拒绝，会让一个租户的管理员驱使本进程代表另一个租户查库——这是它无权制造的负载和审计痕迹。

测试用一个"被调用即 fail test"的 `failingRepository` 把"零调用"变成正面观测而不是从错误信息倒推（`web.TestAdminTenantAdminCannotReachAnotherTenant`、`TestAdminCrossTenantRefusalMatchesARealNotFound`）。

### 4.2 Admin 面没有 CORS

Admin 的任何响应——成功和失败——都**不带任何 `Access-Control-Allow-*` 头**，也没有预检分支。`OPTIONS` 不被特殊处理：它和别的请求一样先认证，然后作为这些路由不接受的方法得到 `405`。

配合"所有 POST 必须 `Content-Type: application/json`"，浏览器页面既读不到 Admin 响应，也**发不出** Admin 写操作：`application/json` 让每个写请求都落在 CORS "simple request" 集合之外，浏览器必须先发预检，而预检没有任何东西可以成功。`Content-Type` 检查不是为了解析（`decodeAdminJSON` 本来就会拒绝非 JSON，publish 甚至不带 body），就是为了这条。测试见 `web.TestAdminNeverPublishesCORSHeaders`、`TestAdminWritesRequireJSONContentType`。

对话面 `/v1/chat/completions` 仍然发布 CORS 头，两者是不同的边界。

### 4.3 `created_by` 来自认证 Principal

`createRevisionRequest` **没有** `created_by` 字段。作者身份不是请求可以声明的东西，它就是发出这次请求的凭据的 principal：

```go
CreatedBy: admin.PrincipalID,
```

字段是"不存在"而不是"接受后覆盖"，因此仍然携带 `created_by` 的请求体会被 `DisallowUnknownFields` 拒绝（`400 invalid_json`），而不是安静地表达一个和字面意思不同的语义。测试见 `web.TestAdminRevisionAuthorshipComesFromTheCredential`。

## 5. Security Manifest

### 5.1 两个来源，不混用

| `TRPC_SERVICE_SECURITY_CONFIG_FILE` | 生效配置 |
| --- | --- |
| 未设置（严格等于空串） | demo profile |
| 指向文件 | 该 manifest 是**全部**配置 |
| 只有空白 | **启动失败**，不回退 |

设置了 manifest 时，`TRPC_SERVICE_API_KEY` 和公开的开发 key **完全不参与**：写了 manifest 的部署没打算同时保留第二条环境凭据入口。设成空白直接拒绝而不回退，是因为回退正是"运维以为自己的 manifest 生效了，实际跑的是 demo profile"的成因。路径也不 trim——带杂散空格的路径不会被猜成它像的那个路径。

demo profile 是：一个 chat 凭据（`TRPC_SERVICE_API_KEY`，未设置时用公开的 `local-development-key-not-a-secret`）、一个 platform admin 凭据（`TRPC_SERVICE_ADMIN_API_KEY`，**没有任何默认值**，未设置则拒绝启动），以及一条 entitlement：`demo` 租户可以引用 `builtin.safe-tools`，**不能引用任何 SecretRef**。demo profile 走的是和 manifest 完全相同的校验路径。

chat key 有公开占位值而 admin key 没有，理由是不对称的：公开的 chat key 只能和一个 demo App 对话；公开的 admin key 能创建租户和发布 Revision，而它在任何人读源码的那一刻就已经泄漏。

### 5.2 格式

```json
{
  "version": 1,
  "credentials": [
    {
      "purpose": "chat",
      "principal_id": "tenant-a-user",
      "key_ref": "env:CHAT_KEY",
      "tenant_id": "tenant-a",
      "allowed_app_ids": ["assistant"]
    },
    {
      "purpose": "platform_admin",
      "principal_id": "ops",
      "key_ref": "env:ADMIN_KEY"
    },
    {
      "purpose": "tenant_admin",
      "principal_id": "tenant-a-ops",
      "key_ref": "env:TENANT_ADMIN_KEY",
      "tenant_id": "tenant-a"
    }
  ],
  "tenant_entitlements": [
    {
      "tenant_id": "tenant-a",
      "allowed_secret_refs": ["env:TENANT_A_MODEL_KEY"],
      "allowed_policy_refs": ["builtin.safe-tools"]
    }
  ]
}
```

字段规则：

| `purpose` | `tenant_id` | `allowed_app_ids` |
| --- | --- | --- |
| `chat` | 必填 | 必填，非空，不可重复 |
| `platform_admin` | **必须缺省** | **必须缺省** |
| `tenant_admin` | 必填 | **必须缺省** |

"缺省"和"必填"一样严格地检查：携带 `tenant_id` 的 `platform_admin` 条目说明作者对这条授权有一个不成立的信念，接受它同时忽略该字段等于确认这个信念。

**key 的值永远不在文件里**，只有 `key_ref: "env:VAR_NAME"`。

### 5.3 严格解析

security 文件是最不能容忍"解析器忽略了它不认识的那部分"的一类文件——被忽略的正是有人以为自己写下的授权。因此解析在每个可能藏错误的方向上都是严格的：

- **版本按相等比较**，不是"至少"。`version: 2` 被拒绝而不是尽力兼容：为后续版本写的文件可能含有语义已变的字段，而它会被这个 build 静默按旧语义读取。
- **未知字段拒绝**（`DisallowUnknownFields`）。
- **重复成员拒绝**，任意深度。`encoding/json` 对重复成员取最后一个且不报告，于是 `{"purpose":"chat","purpose":"platform_admin"}` 会解码成 platform admin，而文件里还说了别的这件事没有任何痕迹。比较用 `strings.EqualFold`，因为这正是 decoder 把成员名匹配到结构体字段时用的折叠关系——`key_ref`、`KEY_REF` 和 `Key_ref`（U+212A KELVIN SIGN）都落进同一个字段，最后一个获胜。只折叠 ASCII 会让非 ASCII 拼写作为"不同成员"漏过去，即同样的静默 last-win 多加一步。
- **文件里只能有一个 JSON 值**，尾随第二个对象或任何垃圾都拒绝。
- **必须是常规文件**，且不超过 256 KiB。句柄被 `Stat` 而不是路径，读取限制到上界 +1 字节，因此 stat 之后才增长的文件由长度检查兜住。
- 错误信息**从不引用文件内容**：重复成员只报字节偏移，不复述成员名。

### 5.4 凭据解析

- `key_ref` 经 `secretref.EnvName` 解析，只接受 `env:VAR_NAME`，且是**精确匹配**——不 trim、不折叠大小写、不做任何规范化。
- 两个条目引用同一个环境变量 → 拒绝。
- 环境变量未设置或为空串 → 拒绝（导出成空等于运维要了一个不存在的凭据）。
- 两个条目**解析出同一个 key**（按 SHA-256 比较，而不是按变量名）→ 拒绝。否则把 admin key 导出到某个租户 chat 凭据读的第二个变量里，就能让一个凭据静默地当两个用，而一次请求命中哪个取决于哪个认证器先看到它。
- 反过来不限制：**一个 principal 持有两个不同的 key 是轮转**，必须允许（`security.TestLoadAcceptsTwoKeysForOnePrincipal`）。
- manifest 至少要有**一个 chat 凭据和一个 platform admin**。没有 platform admin 的进程会启动进一个无人能管理的控制面。
- 任何错误都不携带 key，只给条目下标、purpose 和查找过的变量名。

## 6. 租户 Entitlement

`tenant_entitlements` 按**租户**而不是按凭据组织：能不能引用一个能力，是"跑这个 Revision 的租户"的属性，而不是"碰巧创建它的人"的属性。

- `allowed_secret_refs`：该租户的 Revision 可以在 `model.secret_ref` 里引用哪些 `env:VAR_NAME`。
- `allowed_policy_refs`：可以在 `policy_refs` 里引用哪些策略。
- 匹配是**精确字符串**，没有通配符、大小写折叠或规范化。
- 条目内重复、租户重复都拒绝。
- `allowed_policy_refs` 在**加载时**对着运行中的 Tool Registry 校验（`tool.Registry.HasPolicy`）。这必须发生在这里而不是首次使用时：按设计调用方分不出"策略不存在"和"你没被授权"，所以 manifest 里的一个拼写错误否则就是一条永久的、无从读起的静默拒绝。

### 6.1 凭据 / Entitlement 分离

两条**不可逾越**的规则：

1. 任何租户都不能被授权引用**持有本平台自身凭据**的那些环境变量（由 manifest loader 检查，因为只有它知道这些变量是哪些）。
2. 任何租户都不能被授权引用 `TRPC_SERVICE_` 命名空间里的任何变量（由 `Entitlements.add` 检查，因此在代码里构造的 Grant 也受同一条约束）。

没有第一条，把某个租户授权到它自己的模型 key，就足以让它发布一个 `secret_ref` 指向 admin key 的 Revision，并让 Runtime 把这个 key 发到它自己选定的 `base_url`。检查按**精确名字**进行——一条比它所保护的查找更聪明的匹配规则，是一条带缺口的规则（`security.TestLoadRefusesToEntitleATenantToAPlatformVariable`、`TestEntitlementSeparationMatchesExactNamesOnly`）。

### 6.2 三个执行点，同一个 authorizer 实例

| 位置 | 时机 |
| --- | --- |
| `POST .../revisions` | 写库**之前** |
| `POST .../revisions/{id}/publish` | 读出存储的 Revision 之后，发布之前 |
| Runtime 构建 | digest 校验之后，Tool Registry 和 Secret 解析之前 |

`cmd/trpc-service/main.go` 把 `securityCfg.Revisions` **同一个实例**同时交给 `web.NewPlatformServer` 和 Runtime 工厂。不是两个等价的值：Admin 接受而 Runtime 拒绝（或者反过来）是一次关于"这个租户能做什么"的分歧，而这种分歧在请求期没有正确的解法。

创建时就检查（而不是只在 publish 检查），是为了不让一个 draft 攒着看起来被接受的引用，然后在运维最难判断"是配置错了还是平台错了"的地方失败。

拒绝一律是同一个 `403 not_entitled`，措辞固定，不说明是哪个引用被拒。对"环境变量存在"和"不存在"、"策略已注册"和"从没听说过"都给出同一个答案——任何差异都会让这个端点变成对进程环境和 Tool Registry 的探针（`web.TestAdminRefusesUnentitledRevisionsIdentically`）。

`publish` 上 entitlement 先于 Tool Registry 检查，顺序和 Runtime 一致。Registry 失败返回 `400 invalid_revision` 并说明原因：走到这一步的调用方对它引用的每个 ref 都已被授权，剩下的故障在 Revision 自身，此时说清楚是"可修复的错误"和"谜"的区别。

## 7. Runtime 构建顺序

`agent.newRuntimeFromRevision` 的检查顺序本身就是安全属性：

```text
1. 状态必须是 published
2. 身份与 config 形状（tenant / app / revision id、config 校验）
3. config_digest 重算并精确核验     → tenant.ErrConfigIntegrity
4. RevisionAuthorizer               → security.ErrNotEntitled
5. Tool Registry 解析
6. 模型构造与 SecretRef 解析
```

**第 3 步**：Published Revision 是不可变的，所以它的 config 必须仍然哈希到创建时记录的值。对不上意味着这一行是在 Repository 之外被改过的，而这正是"评审之后再挪动 `secret_ref` 或 `base_url`"的做法。**空 digest 算不匹配，不算豁免**——无法被核验必须和被核验为错误以同样的方式失败。测试覆盖四种绕过 Repository 的篡改（挪 credential、挪 endpoint、改写 instruction、加 policy），且拒绝信息不回显被篡改的值（`agent.TestRuntimeReverifiesThePublishedDigest`、`TestRuntimeRefusesTamperingThatSurvivesTheDigest`）。

**第 4 步先于第 5、6 步**：一个租户无权运行的 Revision 必须在平台**没有读取那个环境变量**、也没有透露策略是否存在的前提下被拒绝。因此 `secret_ref` 指向未授权变量时，该变量是已设置、已导出为空还是根本不存在，得到的答案完全相同（`agent.TestRuntimeAuthorizesBeforeItReadsTheEnvironment`、`TestRuntimeResolvesTheCredentialOnlyAfterEntitlement`、`TestRuntimeRefusalDoesNotDistinguishRealPoliciesFromInvented`）。

`RevisionAuthorizer` 是**必填、非 variadic、无默认值**的构造参数：谁可以运行一个引用凭据的 Revision，正是那个不能靠"忘了传"来决定的选择。没有能力配置的调用方显式传 `security.DenyCapabilities()`——它不是"关掉授权"，而是最严格的答案。不引用任何 SecretRef / PolicyRef 的 Revision 在它下面照样能跑，因为那恰好就是不需要 entitlement 的那一类（`agent.TestRuntimeRunsCapabilityFreeRevisionsWithNoEntitlement`、`TestRuntimeRequiresAnAuthorizer`）。

Runtime 构建期的 `ErrNotEntitled` 和 `ErrConfigIntegrity` 在 HTTP 层都塌缩成 `409 revision_unavailable`，措辞与"未发布"完全一致，不透露是三者中的哪一个。

## 8. 启动顺序

```text
校验监听地址 → security.Load → 校验存储配置 → 开池 / Ping / Migrate → Resolver → HTTP Server
```

`security.Load` 只读文件和环境，不碰数据库，因此是**最便宜的拒绝**：凭据配错的进程不应该在发现这件事的路上连过共享数据库或跑过一次迁移。它也在存储之前拿到 Tool Registry，因此 manifest 授权的策略是对着**真实存在的**策略校验的。

启动日志只打印 `securityCfg.Description`，它报告来源和数量，从不包含 key、摘要或任何解析出的值。

顺序被写成测试而不是只写进注释：给一个**同时**错两处的环境（manifest 指向不存在的文件 + postgres profile 缺 DSN），断言回来的是 security 的拒绝，并用一个记录每次调用的存储构造器 stub 把"没到存储"变成正面观测（`main.TestRunLoadsSecurityBeforeStorage`、`TestRunValidatesTheListenAddressFirst`）。

## 9. `start.sh` 与本地 Admin Key

demo profile 下 `TRPC_SERVICE_ADMIN_API_KEY` 没有默认值，所以 `start.sh` 在环境什么都没提供时生成一个，一次，并保留：

- 从 `/dev/urandom` 取 48 个 `[A-Za-z0-9_-]` 字符，远超 32 的下限。
- 以 `umask 077` 创建 `data/admin-api-key`，之后再显式 `chmod 600`，因此新建文件和早期宽松版本留下的文件都不会是组或全局可读。
- **重启复用同一个文件**，不重新生成。
- **key 本身从不打印**，终端和日志都没有；打印的只有路径，因为运维必须找得到它。

两种情况完全不生成文件：环境里已经有 `TRPC_SERVICE_ADMIN_API_KEY`（运维的 key 由运维自己管，把副本写进工作区等于这个脚本替他们决定 secret 放哪），或者设置了 `TRPC_SERVICE_SECURITY_CONFIG_FILE`（凭据来源由 manifest 决定，生成一个没人读的 key 只是看起来像配置的噪音）。

`data/` 已在 `.gitignore` 中（`data/*`，仅保留 `data/README.md`），生成的 key 不会进入 Git。

## 10. 明确未实现

这些能力**没有**实现，不能在验收中被当作已解决：

- **JWT / OIDC / 任何动态身份提供方。** 只有静态 API Key，没有轮转、过期、撤销、按 Principal 的配额与限流。轮转目前只能表现为"manifest 里给同一个 principal 配两个 key，然后重启"。
- **动态 RBAC。** 角色是编译期闭集的两个值，权限是代码里的分支，不是可配置的策略表。
- **热加载。** manifest 只在启动时读一次。改文件必须重启进程；已被 Resolver 缓存的 Runtime 保持它构建时的 entitlement 判定。
- **持久化审计。** 没有任何管理操作审计：谁在什么时候创建了租户、发布了哪个 Revision，都只有 `created_by` 这一个字段，没有独立、不可篡改、可查询、有保留期的 Audit Store。Tool 审计仍然只写结构化 `slog`。
- **生产 Secret Manager。** `secretref` 只解析 `env:VAR_NAME`。Vault / KMS / Kubernetes Secret 引用、租户级密钥托管和密钥轮转都未实现。
- **预算、审批与 Guardrail。** 没有 token / 成本预算，没有危险操作二次确认，没有 Plugin/Guardrail 治理链路。
- **全链路日志脱敏。** 已实现的是若干具体位置的不回显（SecretRef 错误、manifest 错误、Tool 审计不含参数/结果/错误正文、DSN 经 `sessionbackend.Scrub`），不是一条覆盖全进程的脱敏管道。
- **`config_digest` 是无密钥摘要。** 它防住的是"能写库但没有本二进制"的写者。同时能改行**并重算指纹**的写者可以改掉 `base_url` 并让本进程照常构建——`base_url` 不是可授权能力，没有任何 manifest 字段授予或收回一个 endpoint，所以 digest 是它唯一的防线。这条残余风险以数据库写权限为界，要关闭需要 keyed digest 或对 config 的签名，而不是在构建函数里再加一个检查（`agent.TestRuntimeDigestDoesNotDefendAgainstAWriterWhoCanRecomputeIt`）。
- **传输安全。** 进程只服务明文 HTTP，因此只能绑定回环地址，且没有绕过开关。TLS 由外部反向代理终止。

## 11. 相关文档

- [Admin API 与动态路由](admin-api.md)：端点、请求示例、路由顺序和错误码。
- [Tool 与 Policy Runtime](tool-policy.md)：Tool Registry、Policy 交集、工具循环上限和 Tool 审计字段。
- [验收矩阵](acceptance.md)：A01 / A05 / A19 / A22 / A23 与阶段性证据 I14。
