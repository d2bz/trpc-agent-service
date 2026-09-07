# Channel 实验代码选择性复用

2026-09-07：用户要求先从约两万行未提交 Channel 中挑选可用代码。本次已生成并验证独立候选；尚未把候选运行时代码提交或接入企微。原实验文件完整保留。

## 选择结论

先复用 PostgreSQL Store 数据层，共 11 个文件、6613 行（相对已提交的 3 行包占位净增 6610 行）。其中生产源文件 4632 行，测试及测试辅助文件 1981 行，均含注释和空行。它是可独立构建的候选，不是完整 IM 链路，也不是必须全部纳入的最终决定。

本次核对的未提交 Channel Go 文件合计 20605 行；加上相关 sessionrun、Session lease 和 Web 测试修改，未提交 Go 净增量共 25571 行。候选约占后者的 25.8%。这是当前数据层的抽取结果，不代表未来完整 IM 所需代码已经缩减到这个数字。

| 选取内容 | 文件 | 原因 |
| --- | --- | --- |
| 消息、状态与 Store 契约 | `channels.go`、`records.go`、`store.go` | PostgreSQL 实现的类型和校验依赖；原样保留，不在本轮改公开接口 |
| PostgreSQL 实现 | `postgres/inbox.go`、`runs.go`、`outbox.go`、`postgres.go`、`migrate.go` | 持久去重、Session 受理序、Run/Outbox attempt CAS、Run 终态与 Outbox 原子提交 |
| 对应测试 | `channelstest/channelstest.go`、`postgres/integration_test.go`、`postgres/postgres_test.go` | 原样运行 Store 行为契约、迁移、并发条件更新与错误脱敏测试 |

表中路径均相对 `trpcservice/channels/`。精确文件清单、原始 SHA-256 和基线提交见 [选择清单](../scripts/channel-selection.json)。选择按真实包依赖验证，不按文件名或行数猜测。

## 暂不纳入

- `memory.go` 与内存 Store 测试：候选只选择 PostgreSQL；同一套 Store 契约已在真实数据库运行，不另带一套运行时存储实现。
- `sender.go`、`dispatcher.go`、`ingress.go`、`worker.go` 及其测试：属于执行与收发编排，Store 并不依赖它们；后续按企微消费者需求决定复用哪些部分。
- `wakeup.go`、MemoryWaker、RedisWaker 及 `channelstest/waker.go`：本候选无通知消费循环。特别是测试辅助目录不能整目录复制，否则会把 Waker 类型重新带入依赖。
- `recovery.go` 及其测试：Store 保留恢复查询和 CAS 操作，但本候选没有周期扫描器，不宣称通知丢失或执行者退出后自动恢复。
- 未提交的 `sessionrun` Hold/Transcript、Redis Lease 精度修复和 Web 租约测试扩展：数据层不需要它们；候选继续使用基线已提交版本。

这些文件暂不进入候选，不删除、不批量改写、不仅为缩行数删掉已选实现的测试。当前契约中的媒体字段、迁移兼容和未消费方法仍在选取文件内；若进一步按符号精简，需要另行评估接口变化，本轮未做。

## 验证证据

基线为 `6e549b18441d2ceca35ec515c088a266607defb5`，在干净 `git archive` 上只覆盖清单中的 11 个文件。验证目录：`/tmp/trpc-channel-selection.VKE9k1`。

| 验证 | 结果 |
| --- | --- |
| 全仓默认 `go test -race -count=1 -timeout 900s ./...` | 通过，真实模型和数据库门控关闭 |
| `go vet ./...` | 通过 |
| `go build ./...` | 通过 |
| `TRPC_SERVICE_SESSION_INTEGRATION=1 go test -race -count=1 -timeout 180s ./trpcservice/channels/postgres` | 通过；本机 PostgreSQL 16，使用仓库 Compose 开发凭据与隔离测试 schema |
| 文件一致性 | 所选文件与原实验逐字节相同；未带入未提交 sessionrun 或其他执行组件 |

PostgreSQL 测试直接验证持久 Store，不调用 Runner，不模拟真实机器人已上线。数据库数据存在和状态转换正确，不等于自动恢复任务已接线。

重新生成候选：

```sh
node scripts/prepare-channel-candidate.mjs
```

脚本输出一个新目录；仅从固定 Git 基线和清单中的已校验文件生成，不读取 `.env.local`、data、密钥或任意未跟踪文件。源文件变化时拒绝生成，避免把后续实验悄悄带入。脚本本身不提交代码、不运行服务、不连接数据库；在输出目录执行上述测试时，数据库门控另按 `deploy/docker-compose.session.yml` 配置。

## Review 与后续边界

- **发布阻断（提交范围）**：候选净增 6610 行，仍超过 AGENTS.md 的单切片 3000 行阈值。先交付可审查候选与验证结果，未把这 11 个运行时/测试文件提交；需明确确认这一次选取的范围后才能纳入。不能把它按机械拆分提交伪装成多个小目标。
- **发布阻断（企微接线，未实施）**：已提交企微协议包的 `ReplyTarget` 字段不导出，不能直接作为持久 DeliveryTarget 序列化。还需明确持久目标映射、单条终态文本长度策略及发送未知结果的终止行为；不能因 Store 测试通过就宣布企微端到端完成。
- **风险登记**：自动恢复、Runner Event 对账、发送失败不重跑 Agent 等执行层保证在架构上保留，本候选不提供这些运行行为。纳入数据层后，下一切片只针对企微文本消费者决定执行层取舍，不能自动收编剩余实验。
- **可选优化**：跨进程 Redis 通知、媒体、群聊、卡片和飞书实现继续暂缓。
