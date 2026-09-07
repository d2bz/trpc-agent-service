# 2026-09-07 当前工作树验证输出

以下保留本轮命令输出；不是对最终交付 commit 的验收。基线和适用边界见[验收记录](../../../../docs/verification-2026-09-07.md)。

## 默认全仓 Race

命令：`env TRPC_SERVICE_MODEL_INTEGRATION=0 TRPC_SERVICE_SESSION_INTEGRATION=0 go test -race -count=1 -timeout 900s ./...`，退出码 0。

```text
ok  github.com/liuzengh/trpc-agent-service/cmd/trpc-service 2.051s
?   github.com/liuzengh/trpc-agent-service/trpcservice [no test files]
ok  github.com/liuzengh/trpc-agent-service/trpcservice/agent 3.310s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/channels 16.189s
?   github.com/liuzengh/trpc-agent-service/trpcservice/channels/channelstest [no test files]
ok  github.com/liuzengh/trpc-agent-service/trpcservice/channels/postgres 2.682s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/channels/rediswaker 2.368s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/config 3.918s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/identity 3.536s
?   github.com/liuzengh/trpc-agent-service/trpcservice/log [no test files]
?   github.com/liuzengh/trpc-agent-service/trpcservice/metrics [no test files]
ok  github.com/liuzengh/trpc-agent-service/trpcservice/secretref 4.071s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/security 4.357s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/sessionbackend 4.301s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir 2.926s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir/postgres 2.946s
?   github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir/sessiondirtest [no test files]
ok  github.com/liuzengh/trpc-agent-service/trpcservice/sessionlease 6.013s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/sessionlease/redis 3.211s
?   github.com/liuzengh/trpc-agent-service/trpcservice/sessionlease/sessionleasetest [no test files]
ok  github.com/liuzengh/trpc-agent-service/trpcservice/sessionrun 4.221s
?   github.com/liuzengh/trpc-agent-service/trpcservice/skill [no test files]
ok  github.com/liuzengh/trpc-agent-service/trpcservice/storagebundle 3.351s
?   github.com/liuzengh/trpc-agent-service/trpcservice/storagebundle/profiletest [no test files]
ok  github.com/liuzengh/trpc-agent-service/trpcservice/tenant 2.402s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/tenant/postgres 2.388s
?   github.com/liuzengh/trpc-agent-service/trpcservice/tenant/tenanttest [no test files]
ok  github.com/liuzengh/trpc-agent-service/trpcservice/tool 2.640s
ok  github.com/liuzengh/trpc-agent-service/trpcservice/web 3.145s
?   github.com/liuzengh/trpc-agent-service/trpcservice/workspace [no test files]
```

## 本地 HTTP 演示

构建后使用 `TRPC_SERVICE_ADDR=127.0.0.1:18080`、`TRPC_SERVICE_STORAGE_PROFILE=inmemory`、默认 demo security profile 和公开 demo chat key 启动；Admin Key 由启动脚本生成，仅输出文件路径。服务与验证器在同一个 shell 生命周期内执行，退出时调用 `./stop.sh`。

`node scripts/verify-reference.mjs http://127.0.0.1:18080`，退出码 0：

```jsonl
{"check":"health","result":"PASS"}
{"check":"authentication-and-credential-separation","result":"PASS"}
{"check":"http-echo-and-server-identifiers","result":"PASS","session_id":"617938c1-97a0-46d9-b38b-431b06cbbd15","request_id":"e2d0bbd9-722f-4313-baf2-9538c8687ad8","revision_id":"echo-v1"}
{"check":"sse-and-session-continuation","result":"PASS","session_id":"617938c1-97a0-46d9-b38b-431b06cbbd15","revision_id":"echo-v1"}
{"check":"tenant-assertion-refused","result":"PASS"}
{"check":"publication-keeps-existing-session-pin","result":"PASS","old_revision":"echo-v1","new_revision":"verify-ea9a3e49-03bc-42a2-85a9-c962234fd252"}
{"check":"revision-pin-conflict","result":"PASS"}
{"check":"rollback-keeps-existing-session-pin","result":"PASS"}
{"result":"PASS","checks":8,"base_url":"http://127.0.0.1:18080","boundary":"Local deterministic HTTP/SSE only; no real model, IM, database or cross-worker claim."}
```

首次脚本调试曾因服务跨工具调用退出而连接失败，随后发现验收脚本误把 `routing_policy.default_revision_id` 读成顶层字段；修正脚本后上列 8 项通过。这两项没有修改服务功能代码。

`go vet ./...`、`node --check scripts/verify-reference.mjs` 和 `git diff --check` 均无输出、退出码 0。`./build.sh` 输出构建完成；`./stop.sh` 停止本轮 PID，随后 `test ! -e data/trpc-service.pid` 通过。
