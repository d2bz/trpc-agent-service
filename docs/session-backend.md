# 持久化 Session 后端 Spike

本文记录 `trpcservice/sessionbackend` 这一次 Spike 的验证结论。目的只有一个：在写共享 Session 之前，先把上游 PostgreSQL 与 Redis Session 实现的真实行为测出来，把它们与内存实现的语义差异写成事实，而不是等到平台层依赖了某个假设才发现。

**当前状态是 `partial`。** Spike 本身只交付了一个构造函数和它的测试。之后 `cmd/trpc-service` 接了一个进程级存储 profile：默认（不设 `TRPC_SERVICE_STORAGE_PROFILE`，或设为 `inmemory`）仍然启动 InMemory Session，进程不建立任何连接；设为 `postgres` 时，控制面、Session Directory 和上游 Session 存储会一起切到同一个 PostgreSQL 的同一个 schema。见 [§8 进程存储 Profile](#8-进程存储-profile)。

Redis 后端仍然只有工厂能构造，没有任何进程配置能启用它——本文第 4 节以后关于 Redis 的"支持"指的是"工厂能构造出来并通过集成测试"，不指"平台已经在用"。

## 1. 交付范围

| 交付物 | 路径 | 说明 |
| --- | --- | --- |
| 后端工厂 | `trpcservice/sessionbackend/sessionbackend.go` | `New(Config) (session.Service, error)`，三种后端 |
| 单元测试 | `trpcservice/sessionbackend/sessionbackend_test.go` | 不触网，随 `go test ./...` 默认执行 |
| 集成测试 | `trpcservice/sessionbackend/integration_test.go` | 默认跳过，需显式开关 |
| 本地依赖 | `deploy/docker-compose.session.yml` | PostgreSQL + Redis，仅监听 `127.0.0.1` |

工厂刻意做得很小：只有后端名、该后端的一个连接串，以及集成测试为了共享服务器所必需的命名空间开关。其余上游选项一律保持默认。这次没有做、也不应该在这一层做的：后端能力表（`BackendCapabilities`）、存储捆绑（`StorageBundle`）、后端注册表、控制面 Repository。它们属于后续切片，现在加进来只会变成一份会漂移的上游选项副本。

## 2. 依赖版本与 Go 版本要求

```
trpc.group/trpc-go/trpc-agent-go                 v1.11.2   （核心，原有）
trpc.group/trpc-go/trpc-agent-go/session/postgres v1.11.0   （新增）
trpc.group/trpc-go/trpc-agent-go/session/redis    v1.11.0   （新增）
```

两个 Session 子模块是独立 Go module，各自带入间接依赖 `storage/postgres v0.8.0`、`storage/redis v0.0.3`、`jackc/pgx/v5 v5.7.2`、`redis/go-redis/v9 v9.11.0`。

### 2.1 go directive 被抬到 1.24.1

本仓库 `go.mod` 的 go directive 从 `1.21` 变为 `1.24.1`。**这不是主动升级，是依赖强制的**：`trpc.group/trpc-go/trpc-agent-go/storage/redis@v0.0.3` 的 `go.mod` 声明 `go 1.24.1`，而 Go 1.21 起 go directive 具有传递约束力——主模块的 go 版本不得低于任何依赖声明的版本，否则 `go build` 直接报错。

影响与取舍：

- **构建工具链下限从 Go 1.21 抬到 Go 1.24.1。** 低于该版本的环境无法构建本仓库，CI 镜像和开发机需要相应更新。本次验证环境为 `go1.25.12 darwin/arm64`。
- **不能靠回退依赖绕开。** 回退 `storage/redis` 会同时回退 `session/redis`，等于放弃这次要验证的目标；为了保留 1.21 而降级依赖是本末倒置，因此不做。
- **这是一次单向决定。** 后续引入的任何依赖都不会再把下限降回去，所以文档和 CI 应直接以 1.24.1 为基线。

## 3. 兼容性验证

两个子模块声明的核心依赖是 `trpc.group/trpc-go/trpc-agent-go v0.2.0`，并带有指向 `../../` 的 `replace`。**这两条信息在本仓库里都不生效**：依赖模块的 `replace` 只对它自己作为主模块时有效，而 MVS 会选中本仓库直接依赖的 v1.11.2。也就是说，模块图不提供任何"子模块 v1.11.0 与核心 v1.11.2 兼容"的保证——声明的 v0.2.0 下限没有意义。

风险更进一步：两个子模块跨模块引用了核心的内部包。

```
trpc.group/trpc-go/trpc-agent-go/internal/session/sqldb   （postgres、redis 都用）
trpc.group/trpc-go/trpc-agent-go/internal/session/hook    （redis 用）
```

Go 的 internal 规则按导入路径前缀判定而非按模块判定，所以这种引用合法。但 `internal` 包不承担任何兼容承诺，核心版本变动可以在不违反 semver 的前提下破坏子模块编译。

**结论：唯一的兼容性证据是编译加测试通过，没有版本号层面的保证。** 因此：

- `trpcservice/sessionbackend/sessionbackend_test.go` 顶部有三条接口断言，把 `*inmemory.SessionService`、`*postgres.Service`、`*redis.Service` 钉死在 `session.Service` 上。上游改动方法集时，构建在这里失败，而不是在调用点或运行时失败。
- 每次升级核心版本，必须重新执行 `go build ./...` 与本包集成测试，不能只看 semver。

已验证：核心 v1.11.2 + 子模块 v1.11.0 组合下，`go build ./...`、`go vet ./...`、`go test -race ./...` 全部通过，集成测试对真实 PostgreSQL 16 与 Redis 7 通过。

## 4. 三种后端的语义差异

`session.Service` 是同一个接口，但三种实现的可观察行为并不一致。以下差异全部由 `integration_test.go` 针对真实服务验证，不是读代码推断的。

### 4.1 差异总表

| 行为 | InMemory | PostgreSQL | Redis |
| --- | --- | --- | --- |
| `GetSession` 会话不存在 | `(nil, nil)` | `(nil, nil)` | `(nil, nil)` |
| 第二次 `CreateSession` | 返回已有会话 | **返回错误** | 返回已有会话（含历史） |
| `DeleteSession` | 真删 | **默认软删，行保留** | 删除 key |
| 进程重启后数据 | 丢失 | 保留 | 保留 |
| `Close` 重复调用 | 安全 | 安全（`sync.Once`） | 安全（`sync.Once`） |

**`GetSession` 返回 `(nil, nil)`** 是三者一致的：会话不存在不是错误。调用方只检查 `err != nil` 就会对 nil 解引用。这是最容易写错的一条。

**第二次 `CreateSession` 的分歧**是最危险的一条，因为它在接口层完全不可见。PostgreSQL 返回 `session already exists and has not expired`；Redis 返回已存在的会话，连同全部历史事件。任何依赖其中一种形状的代码，在换后端时都会坏掉。平台层如果需要"创建或获取"语义，必须自己在上层实现，不能依赖后端行为。

### 4.2 PostgreSQL

**自动建表。** `NewService` 在构造时连接数据库并创建 6 张表（`WithSkipDBInit(true)` 可关闭，本工厂不使用该选项，保持自动建表）：

```
session_states  session_events  session_track_events
session_summaries  app_states  user_states
```

`WithTablePrefix` 会给每张表加前缀（不以 `_` 结尾时自动补 `_`），`WithSchema` 指定 schema。**schema 必须事先存在**——上游只建表，不建 schema。集成测试使用前缀 `spike`，实测建出 `spike_session_states` 等 6 张表。

这意味着服务账号在首次启动时需要 DDL 权限。生产环境若由迁移工具管理表结构，应改用 `WithSkipDBInit(true)`，但那超出本次 Spike 范围。

**默认软删。** `softDelete` 默认为 `true`，`DeleteSession` 只写 `deleted_at`。会话从读路径消失（`GetSession` 返回 `(nil, nil)`），但行和事件行都留在库里。实测：集成测试删除 2 个会话后，`spike_session_states` 仍有 2 行（`deleted_at` 均非空），`spike_session_events` 仍有 2 行。**软删的存储不会自己回收**，这是一条容量风险，不是可以忽略的实现细节。

**只持久化有效且非 partial 的 Response 事件。** 见 4.4。

### 4.3 Redis

**默认 `CompatModeLegacy`。** `defaultOptions.compatMode = CompatModeLegacy`，即"读时回退到 zset，但不双写"。这是上游为 zset→hashidx 存储迁移准备的兼容档位，新部署拿到的就是这个默认值。本工厂不改它——改动这个开关等于替上游做存储格式决策，应该在真正需要迁移时单独评估。

**`WithKeyPrefix` 不被上游校验。** 前缀原样拼进每个 key。因此本工厂自己校验：只允许 `[A-Za-z0-9_.:-]`，最长 32 字符。拒绝空格（会让 key 空间变形）和 `{}`（Redis Cluster 的 hash tag，会静默改变分片归属）。集成测试用 `spike:<8位随机>` 做每次运行的隔离，并验证了两个不同前缀的服务看不到彼此的会话。

**Redis 不需要预置结构**，客户端也是懒连接，所以一个指向空地址的 URL 通常不会让 `New` 失败，而是在第一次 Session 调用时才报错。

### 4.4 事件必须是有效的 Response，且必须包含 user 消息

有两条上游规则决定"写进去的事件下次还在不在"，任何忽略其中一条的测试都会在空会话上通过：

1. **事件只有在 `Response` 非 nil、非 partial 且带 payload 时才被记录。** `session.Session.UpdateUserSession` 的条件是 `event.Response != nil && !event.IsPartial && event.IsValidContent()`，对所有后端生效，PostgreSQL 的持久化路径再校验一次。
2. **`session.Session.ApplyEventFiltering` 保证结果里至少有一条 user 消息，否则清空整个列表。** 它先从首条 user 消息开始截断（此前的事件被丢弃），找不到时回头从原列表补一条最后的 user 消息，仍然找不到就把 `Events` 置空。所以只追加 assistant 事件的会话，`AppendEvent` 返回 nil，但读回来是空的。

第 2 条在 `TestAssistantOnlySessionReadsBackEmpty` 中被固定为事实（用 InMemory 后端即可复现，无需外部服务）。所有集成测试因此都使用"user 消息 + assistant 回复"的完整一轮作为夹具——这本来也是真实对话的形状。

## 5. 工厂本身的约定

### 5.1 默认后端是 InMemory

`DefaultConfig()` 返回 `BackendInMemory`。这是唯一不需要外部服务的后端，`cmd/trpc-service` 的默认启动路径不变，空机器上 `./build.sh && ./start.sh` 仍然能跑。

`Config.Backend` 为空是配置错误，不是"要默认值"——想要默认值就调 `DefaultConfig()`。这条选择是为了让配置拼写错误在启动时就暴露，而不是静默退化成内存后端、跑到重启丢数据时才被发现。

### 5.2 Validate 必须先于上游选项执行

上游的 `WithTablePrefix` 和 `WithSchema` 走的是 `sqldb.MustValidateTablePrefix` / `MustValidateTableName`，**校验失败时 panic 而不是返回 error**。所以 `Config.Validate()` 必须在 `New` 把值交给上游之前拒绝非法输入，否则一个配置笔误会变成进程崩溃。

本包的校验比上游更严：正则相同（`^[A-Za-z_][A-Za-z0-9_]*$`），但长度上限取 32 而非上游的 64。原因是 PostgreSQL 标识符在 63 字节处截断，而最长的上游表名 `session_track_events` 已占 20 字节；一个上游接受的 64 字符前缀，截断后仍可能撞名。

`Validate` 不联网。通过校验的配置仍然可能连不上。

### 5.3 PostgreSQL 空 DSN 必须被拒绝

上游 `NewService` 的连接优先级是 DSN → 直连参数 → 实例名 → **回退到默认连接串**，而默认串是 `host=localhost port=5432`。也就是说，一个空 DSN 不会报错，而会静默连上本机的 5432。生产环境里这可能是另一个真实数据库。

因此 `PostgresConfig.validate()` 明确拒绝空白 DSN。Redis 的空 URL 同理被拒绝。

### 5.4 错误脱敏的边界

`New` 返回错误前，会把连接串里的密码替换成 `[REDACTED]`——驱动经常把解析失败或拨号失败的连接串原样回显，未处理的错误会把密码写进调用方的日志。实现同时覆盖 URL userinfo 形式（含 percent-encoded 拼写）和 libpq keyword 形式（含单引号包裹的带空格值）。

这段逻辑以 `Scrub(err error, connectionString string) error` 导出，因为拿着 DSN 的不只是本包：`cmd/trpc-service` 建连接池、Ping、跑迁移和关闭资源时产生的错误，全部经过同一个 `Scrub`。它是幂等的，重复调用不会二次改写。

**pgx 自己的脱敏不够，这一点是这个函数存在的直接原因。** `pgconn.ParseConfigError.Error()` 只脱敏它自己保存的那份连接串副本，而它包着的解析错误是拿原始连接串去解析失败得到的，里面可能带着从原串里切出来的片段。（当前 pinned 的 pgx **不会**把整条原始 DSN 拼进消息——它把 `url.Parse` 的 `*url.Error` 拆到只剩裸消息；早前文档里"回显原文"的说法据此更正。`Scrub` 作为公开 API 仍然覆盖"驱动整条回显 DSN"这种情况，但那是通用契约，不是当前 pgx 的行为。）实测下来真正会漏的是这一条：密码里带一个未编码的 `/`，authority 就在那里截断，本该分隔 userinfo 的 `@` 根本轮不到被看见，解析器把 `/` 之前的那段当成端口原样引用出来——

```
postgres://user:s3cret/x@host:5432/db
  -> cannot parse `postgres://user:xxxxxx@host:5432/db`:
     failed to parse as URL (invalid port ":s3cret" after host)
```

注意 host 和 port 本身完全合法，是密码让这个串不可解析的，所以「DSN 其它部分写对了」并不构成保护；pgx 把自己那份 userinfo 改写成了 `xxxxxx`，密码照样印了出来。

于是密码不能只靠 `url.Parse` 提取，因为泄漏恰好发生在解析失败的时候。`urlPasswords` 分两条路：能解析的 URL 取 `url.URL.User.Password()`（驱动真正拿去认证的解码后拼写）加上串里写的原始拼写；解析不了、且按 URL 语法的边界（authority 止于第一个 `/`、`?`、`#`，userinfo 止于其中最后一个 `@`）也切不出密码时，才退回保守猜测：取第一个 `:` 到最后一个 `@` 之间的整段，再加上这段里每个不含分隔符的片段——因为被回显的正是重新解析切出来的片段。

**被引用出来的片段可以只有一个字符，所以脱敏分两遍。** 密码是 `p/secret` 时，pgx 报的就是 `invalid port ":p" after host`：全局替换单个 `p` 会把 `parse`、`port`、`host` 一起打烂，但把它留在原地就是凭据片段泄漏，两者都不能接受。所以长度 ≥ 3 的密码走全局子串替换，短于 3 的**按位置**替换——只在 `quotedPortPattern`（`invalid port ":<片段>" after host`，即目前唯一实测会回显密码片段的错误结构）命中的那个位置动手，且只在该片段确实属于本连接串的密码时才动。因此真正的端口笔误（`invalid port ":notaport"`）原样保留，仍然是可用的诊断信息；这条判断由 `TestScrubKeepsAQuotedPortThatIsNotPartOfThePassword` 钉住。

**这个保证的范围必须说清楚，不能夸大：**

- 只覆盖 **交给 `Scrub` 的 error**，且只按渲染出来的文本判断——藏在某个 error 结构体字段里、不出现在 `Error()` 文本中的值，够不着。
- **不覆盖** 上游或驱动自己写到日志、metrics、trace span 或 stderr 的内容——本包看不到那些路径。
- 脱敏后的 error 是一个全新 error，**故意不 wrap 原错误**，否则 `errors.Unwrap` 会把密码原样交回去。代价是上游的错误类型无法用 `errors.As` 取出；这是有意的取舍。
- 只处理密码。用户名、主机、库名照常出现在错误里。
- 按子串匹配。驱动若把密码改写成本包不认识的形状再打印，只有已知的那一种改写（`quotedPortPattern`）会被按位置脱敏；出现新的改写形状时，需要补一条同样有实验依据的定向规则。
- `Config.Describe()` 用于日志，只报告连接串"存在/不存在"，从不输出内容。

### 5.5 Close 所有权

`New` 返回的 service 归调用方所有，**调用方必须且只需 Close 一次**，且必须在所有共享它的 Runner 停止之后。本包不 Close 它返回过的任何 service，也不持有引用。

两个持久化后端的 `Close` 都用 `sync.Once` 保护并返回 nil，重复调用安全——`TestIntegrationCloseIsIdempotent` 对真实服务验证了这一点。Resolver 的关闭路径依赖这条性质（正常关闭一次、defer 清理再关一次）。

## 6. 尚未实现的部分（准确表述）

这一节的措辞需要格外小心，避免把"计划"写成"已有"。

**当前仓库没有 AppendEvent hook 接入，也没有任何 fencing（写入准入）实现。**

Spike 之后交付了一把 Run 租约（`trpcservice/sessionlease`，见 [Session Run Lease](session-lease.md)），但它是**合作型**的：它决定谁被允许进入 Run，不决定谁被允许写。下面这条上游限制正是"为什么只能做到这一步"的原因，本节的结论没有因为租约的到来而改变。

上游 PostgreSQL 与 Redis Session 模块确实提供 `WithAppendEventHook` / `WithGetSessionHook` 选项，后续的写入准入检查**计划**经由 `WithAppendEventHook` 接入。但必须记录一个上游层面的限制：

> **hook 检查与后端写入之间不是原子的。** hook 先执行、通过后才写后端，两步之间没有任何屏障。上游没有提供原子 fence 提交的入口——没有"带 fencing token 的条件写"这类接口。因此 hook 只能做尽力而为的准入检查，**不能**用来实现"只有持有有效租约的 writer 才能写入"这种正确性保证。

真正的单写者语义需要在存储层做条件写（例如 Redis Lua 脚本做 token 比较后再写，或 PostgreSQL 用带版本号的条件 UPDATE），这超出上游 Session 接口的能力，必须由平台层自建。本次 Spike 不做，租约切片同样没有做——`Lease.Fence()` 只是观测句柄，不参与写入准入。

同样不在本次 Spike 范围内的：PostgreSQL 控制面 Repository、跨进程 Session Directory、Redis 租约、双 Worker、Inbox/Outbox、真实 IM 接入。其中前四项已由后续切片交付（见 [验收矩阵](acceptance.md) 的 I10、I11）；Inbox/Outbox 与真实 IM 接入**仍然没有**实现。

### 6.1 持久 Session 与进程内 Pin 的重启不变量破裂

这是本次 Spike 发现的、必须写进已知限制的组合性问题。

`trpcservice/sessiondir` 的 `MemoryDirectory` 把 Session→Revision 的 Pin 存在进程内存里。一旦 Session 改用 PostgreSQL 或 Redis 持久化，两者的生命周期就不再对齐：

| | 进程重启前 | 进程重启后 |
| --- | --- | --- |
| Session 数据（持久后端） | 存在 | **存在** |
| Revision Pin（内存目录） | 存在 | **丢失** |

结果是：一段旧会话在重启后仍能读到完整历史，但它的 Pin 没了。下一轮对话会走"无 Pin"分支，被重新 Pin 到**当前默认 Revision**。如果期间发布过新版本，这段会话就在用户无感知的情况下换了 Agent 版本——而"发布不改变进行中会话的行为"正是 Pin 机制存在的唯一理由。

**换句话说，Session 持久化会把 Pin 的不变量从"重启即全丢，语义一致"降级为"数据在但 Pin 不在，语义破裂"。** 现在 Session 在内存里，重启后会话和 Pin 一起消失，反而是自洽的；单独把 Session 持久化会打破这个自洽。

**这一格已经由 §8 的进程存储 profile 关掉——只关掉了这一格。** profile 不提供"只持久化 Session"这个选项：三者绑在同一个 DSN 和同一个 schema 上，要么一起在内存里，要么一起在 PostgreSQL 里，上表右列因此无法被配置出来。

多个进程指向同一个 schema 时，控制面与 Pin 是一致的。对同一个会话的并发写入，现在由 Run 租约在**入口**串行化（`TRPC_SERVICE_SESSION_COORDINATION=redis`，见 [Session Run Lease](session-lease.md)）：第二个 Worker 拿不到租约，收到 `409 session_busy`，持有者失效后按 TTL 接管。这是合作型互斥，**不是**存储层的写入准入——已经在写的旧 Worker 不会被原子拒绝。仍然没有解决的：fencing/CAS 写入准入、等待队列、租户级路由、Redis 存储 profile。该限制的当前表述见[验收矩阵](acceptance.md#已知限制)。

## 7. 集成测试的运行方式

### 7.1 默认不触网

`go test ./...` 在没有 PostgreSQL、没有 Redis、没有网络的机器上必须保持通过。为此：

- `sessionbackend_test.go` 里的所有用例都只做配置校验或使用 InMemory 后端，不建立任何连接。
- `integration_test.go` 里的所有用例在构造任何配置之前先检查开关：`TRPC_SERVICE_SESSION_INTEGRATION` 不等于 `1` 就 `t.Skip`。
- 连接串各自独立门控：只配了 `TRPC_SERVICE_POSTGRES_DSN` 的机器只跑 PostgreSQL 用例，Redis 用例跳过，不会失败。

### 7.2 重复运行不互相碰撞

- **Redis** 用每次运行随机生成的 key 前缀 `spike:<8位>` 隔离，测试结束删除自己写入的会话。
- **PostgreSQL** 用固定表前缀 `spike`，隔离改在行级——app name 带上每次运行的随机后缀。表前缀故意不随机化：上游只建表、从不删表，随机前缀会在每次执行后留下一组新的 6 张表。
- 每个测试有独立的 30 秒超时上下文；**cleanup 使用自己新建的超时上下文**，不复用测试主体的上下文——后者在 cleanup 运行时通常已经取消，继承它会让每次失败的运行都留下垃圾数据。
- Close 通过 `t.Cleanup` 在建立 service 后立刻注册。`t.Cleanup` 是 LIFO，所以后注册的删除清理先跑、Close 最后跑，删除时连接池仍然可用；断言中途失败也不会泄漏连接池。

### 7.3 命令

```bash
# 起依赖（仅监听 127.0.0.1）
docker compose -f deploy/docker-compose.session.yml up -d --wait

# 跑集成测试
TRPC_SERVICE_SESSION_INTEGRATION=1 \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
TRPC_SERVICE_REDIS_URL='redis://:trpc-local-dev@127.0.0.1:56379/0' \
go test -race -timeout 120s ./trpcservice/sessionbackend/...

# 清理（-v 一并删除 named volume）
docker compose -f deploy/docker-compose.session.yml down -v
```

Compose 默认宿主端口为 **55432**（PostgreSQL）和 **56379**（Redis），不是 5432/6379：开发机上经常已经跑着真实数据库，把 Spike 绑到真实库上比端口冲突更糟。通过 `TRPC_SERVICE_POSTGRES_PORT`、`TRPC_SERVICE_REDIS_PORT` 覆盖，用户名/密码/库名分别由 `TRPC_SERVICE_POSTGRES_USER`、`TRPC_SERVICE_POSTGRES_PASSWORD`、`TRPC_SERVICE_POSTGRES_DB`、`TRPC_SERVICE_REDIS_PASSWORD` 覆盖。

> Compose 文件里的 `trpc-local-dev` 是**本地开发占位口令**，为了让 Spike 在空机器上可复现才写进仓库。它不是生产 secret，也不得被当作生产 secret：两个服务都只绑定 `127.0.0.1`，主机之外无法访问。真实部署必须自行通过环境变量提供凭据，绝不能继承这里的默认值。

健康检查两处细节值得留意，都会影响 `up --wait` 的正确性：`pg_isready` 必须带 `-h 127.0.0.1`，因为首次 initdb 期间入口脚本会先起一个只监听 unix socket 的临时服务，走 socket 的探测会过早报告就绪；`redis-cli ping` 必须匹配 `PONG` 而不是只看退出码，因为数据集加载中的 `LOADING` 回复退出码同样是 0。

## 8. 进程存储 Profile

`cmd/trpc-service` 用一个进程级 profile 决定自己把状态放在哪里。实现在 `cmd/trpc-service/storage.go`。

### 8.1 一个 profile，不是三个开关

进程要存的三样东西——控制面（租户 / 应用 / Revision）、Session→Revision 的 Pin、会话历史——是一组，不是三个独立选择。§6.1 那张表就是拆开配置的后果：Pin 活过重启但它指向的 Revision 随内存控制面一起没了，等于没有 Pin；反过来 Pin 丢了而会话还在，会话会被静默重新 Pin 到当前默认 Revision。因此 profile 只有一个，三者一起动。

**Redis 不是一个 profile。** 工厂能构造 Redis Session 服务，但仓库里没有 Redis 的控制面 Repository，也没有 Redis 的 Session Directory；把它开出来，开出来的恰好就是上面那个已知会坏的组合。

### 8.2 环境变量

| 变量 | 取值 | 说明 |
| --- | --- | --- |
| `TRPC_SERVICE_STORAGE_PROFILE` | 空 / 未设 / `inmemory` / `postgres` | 大小写敏感，不做 trim。`Postgres`、`pg`、`redis`、带空格的值一律拒绝启动，并在错误里列出合法值 |
| `TRPC_SERVICE_POSTGRES_DSN` | 连接串 | 当且仅当 profile 为 `postgres` 时必需且不得为空白。**从不写日志**，所有相关错误经 `Scrub` 脱敏 |
| `TRPC_SERVICE_POSTGRES_SCHEMA` | 标识符 | 可选，规则复用 `sessionbackend` 的校验（`^[A-Za-z_][A-Za-z0-9_]*$`，最长 32）。schema **必须事先存在**，进程只建表不建 schema |

两条刻意的选择：

- **`inmemory` 完全不读 PostgreSQL 变量。** 环境里遗留的 DSN 不会改变 inmemory 进程的行为，更不会把进程"升级"成持久化——DSN 的存在**永远不能**选择 profile。要写共享数据库，必须显式点名。
- **拼错就拒绝启动，不静默回退。** 回退到内存的进程看起来是健康的，直到重启丢掉全部会话才被发现。

启动日志只打印 profile、DSN 存在与否、schema 名，从不打印连接串内容。

### 8.3 启动与关闭顺序

顺序本身就是实现的主要内容，逐条都有原因：

1. 先校验监听地址。本进程只服务明文 HTTP，可路由的监听地址会把 Admin Bearer token 明文放到网络上，因此这条守卫必须排在任何可能连数据库的动作之前。（早期版本的理由是"Admin API 无鉴权"；Admin 现在已认证，守卫的位置不变，理由换了。）
2. 加载并整体校验安全配置（Security Manifest 或 demo profile）。凭据、角色和租户 entitlement 全部在这一步定型，排在存储之前：一份配错的清单不该先建出连接池、跑完迁移再失败。详见[身份、权限与密钥治理](security-and-governance.md#8-启动顺序)。
3. 读取并整体校验存储配置，此时还没有创建任何资源；schema 拼错在这一步就失败，而不是迁移跑到一半才失败。
4. 解析 pgx 连接池配置，把校验过的 schema 写进 `search_path`（写在 pool config 上而不是 checkout 后 `SET`，这样连接池后来新开的连接也带同一个 `search_path`）。
5. 建池，**并立即登记关闭动作**——之后任何一步失败都不会漏掉这个池。
6. 在带超时的上下文里 `Ping`。`pgxpool.NewWithConfig` 不拨号，而上游 Session 构造函数建表时用的是它自己的、**不可取消**的 background 上下文；不可达的库必须在进入上游之前、在调用方的 deadline 还有效时暴露出来。
7. 依次跑 `tenantpostgres.Migrate` 和 `sessiondirpostgres.Migrate`（都持咨询锁、都是 `IF NOT EXISTS`，每个 worker 每次启动都跑是安全的）。
8. 构造 Repository 与 Directory（共用同一个池，两者都只借用、都不关闭）；再构造上游 Session 服务（它自己持有并拥有另一个池）。
9. 最后才 `SeedDemo`——它要写控制面，必须在迁移之后。

关闭顺序是它的严格逆序：HTTP 优雅关闭（在 `waitForStop` 里）→ Resolver（等待在途 runtime 交还租约）→ Session 服务 → 共享连接池。启动中途失败时只关闭已经建成的那些资源，同样逆序；**关闭错误用 `errors.Join` 合并进进程退出错误，不再是打条日志就丢掉**——一个"关闭时没刷完"的 Session 服务，一周后表现为"每段会话最后一轮不见了"。

### 8.4 快速开始

```bash
docker compose -f deploy/docker-compose.session.yml up -d --wait

# schema 必须先存在；进程只建表。
psql 'postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session' \
  -c 'CREATE SCHEMA IF NOT EXISTS trpc_service'

TRPC_SERVICE_STORAGE_PROFILE=postgres \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
TRPC_SERVICE_POSTGRES_SCHEMA=trpc_service \
./bin/trpc-service -addr 127.0.0.1:8080
```

不设 `TRPC_SERVICE_STORAGE_PROFILE` 时行为与以前完全一致：空机器、无网络也能启动。

### 8.5 这一层的测试

- `cmd/trpc-service/storage_test.go`：不触网。覆盖默认 profile、`inmemory` 忽略遗留 DSN、缺失/空白 DSN、非法 profile、非法与超长 schema、连接串解析错误不泄漏密码（含 percent-encoded 拼写）、以及启动中途失败时按逆序关闭且关闭错误不被吞掉——失败注入用的是一个只有五个函数的 seam，不是 mock 框架。
- `cmd/trpc-service/integration_test.go`：默认跳过，门控与 §7 相同（`TRPC_SERVICE_SESSION_INTEGRATION=1` 加 `TRPC_SERVICE_POSTGRES_DSN`）。每个用例建一个一次性 schema 并在结束时 `DROP ... CASCADE`（上游只建表不删表，这是唯一会回收那 6 张表的地方）。断言走真实的 profile 构造路径：两族迁移和上游 6 张表都在、Pin 与真实会话历史跨"重启"存活、重启后的 `SeedDemo` 不会把已发布的 `echo-v2` 改回 `echo-v1`。

```bash
TRPC_SERVICE_SESSION_INTEGRATION=1 \
TRPC_SERVICE_POSTGRES_DSN='postgres://trpc:trpc-local-dev@127.0.0.1:55432/trpc_session?sslmode=disable' \
go test -race -count=1 -timeout 300s ./cmd/trpc-service/...
```
