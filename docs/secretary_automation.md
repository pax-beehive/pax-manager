# Secretary 自动多摘要：当前实现

实现更新：2026-11-04。本文描述本次 summary Node 版本的代码与运行契约，
不代表已部署或验证生产数据。完整产品边界见[统一 Node 设计](secretary_topics_design.md)，
交付状态见[问题 067](../issues/067-secretary-topic-capacity-and-execution-lifecycle.md)。

## 启用与成果入口

用户在 Console `/settings/secretary` 选择自己的一个 agent 并启用。设置默认关闭，
打开页面不会启动总结；离线 agent 可以被选中，恢复 ACP 连接后才处理待办。
使用选中 agent 原有模型连接，不要求 Manager 配置另一份模型 API key。
Console 同页提供成果列表、全文、版本历史和原文导航，进度与成果是不同的数据。
同一个 session 可以有多份独立 summary，修订其中一份不会删除未返回的其他摘要。

第一版只有 `node_type=summary`；不创建 topic、普通节点关系、合并、project/direction board、
常驻 agent 或 draft 审批。数据库只调度事实变化，agent 自己决定是否搜索相关资料。
ES 搜索和总结发布解耦；没有 ES 时搜索明确不可用，已授权的 PG 摘要和原文读取仍可用。

## 数据链路

```text
messages / message_parts 的持久变更
  -> 同事务标记 secretary_jobs 待处理
  -> source idle + 静默窗口 + 完成证明
  -> Prepare 固定 secretary_batches 输入并重新授权
  -> Begin 持久 secretary_attempts，再通过选中 agent 的 ACP 执行
  -> 保存输出与可信读取账本
  -> 校验完整输出、来源、权限、CAS、设置和租约
  -> 单事务发布多份 summary + 处理结果 + checkpoint + ES outbox
  -> 独立索引 worker 将 current 投影到 ES
```

模型运行不持有数据库事务锁。每个批次用新的内部执行上下文，避免旧会话中的敏感上下文
跨任务复用；并不因此启动新的 agent 进程。内部执行通过注册表和专用 session 标识隔离，
不进入普通 session 列表、搜索或另一个 Secretary 的输入。

## 表与稳定身份

| 表 | 当前责任 |
| --- | --- |
| `knowledge_nodes` | Node 稳定身份、owner、type、状态、current revision、origin source；不复制另一份可变正文 |
| `summary_versions` | 沿用不可变 `(summary_id, revision)`、标题、正文、claims 和请求幂等 |
| `summary_sources` / `message_snapshots` | 每版确切事实来源、完整 canonical 可见原文快照 |
| `knowledge_node_sources` | 从历史证据派生的来源 session 目录；不是 ACL、进度或未来普通关联 |
| `knowledge_node_invalidations` | 来源变更影响哪个 current revision，持久安排原整理来源修订 |
| `secretary_settings` | 选中 agent、enabled、设置 CAS revision |
| `secretary_jobs` | 每个来源 session 的合并队列、desired/processed revision、租约、backoff、retry epoch |
| `secretary_batches` | 固定输入、设置/来源范围、当前 attempt、提交 manifest 和请求 hash |
| `secretary_attempts` | 每次实际 agent 调用的状态、持久输出、读取账本和错误 |
| `secretary_message_state` | 消息已处理 hash、canonicalization、owner、batch 和 summarized/no_summary 结果 |
| `secretary_internal_sessions` | 内部执行 session 注册和隔离 |

新增 Node ID 由 `SecretaryNodeID(batch_id, proposal_key)` 确定，身份不编码 session。
旧 summary ID 保持可用。`origin_source_id` 是创建该摘要的整理来源，用于成果列表与 session
搜索分组；不是唯一证据来源或权限依据。额外引用另一个 session 不会把同一摘要复制成其成果卡片，
也不会推进另一个 session 的处理位置。第一版只自动修订当前整理来源下的 Node。

不新建 `message_versions` 表，不保存每个 token/chunk 的历史。原始采集层提供稳定 `message_id`，
完成前先拼装 parts；hash 用于去重及异常变更检测，快照用于固定已发布摘要采用的正文。
消息真的在完成后修改时进入显式失效路径；不能用当前正文冒充旧引用。

## 什么输入可以总结

必须同时满足 source 空闲、默认 30 秒静默窗口和完成证明。`secretary_completed_sources.go`
接受消息自身 `complete`，或同 agent/session、匹配 turn 且覆盖到该消息的可靠终态：
`turn_done` 的 complete 状态，或结构化 `end_turn` 的 `result.stopReason=end_turn`。
没有完成证明就等待，不消耗一次模型尝试；不能只看 idle 或字符串匹配猜完成。

Prepare persists the missing-completion reason while releasing the job lease for
the existing 30-second source wait. Task details report `waiting_source` with
`source_incomplete`, rather than implying a queued model request or connection
retry. Successful preparation clears only that waiting reason; running and
completed executions do not retain it. Other failures and retry budgets remain
unchanged. This does not create completion evidence for historical messages.

默认输入是用户完整可见正文和每条 assistant 消息的最后一个自然段。
part 不是段落，message 不是 turn。`first/last/full` 只是同一可见正文的视图。
聊天 tool call、tool result、thought/reasoning 不进入总结输入、hash、coverage、evidence 或快照；
混合消息保留自然语言正文。`read/history/expand` 的 full 同样不暴露工具日志或思考。
assistant 在正文报告“测试通过”等结论仍可总结。纯工具事件不计可总结消息数，但扫描可以越过。

## 增量、覆盖和多摘要输出

每次输入是尚未处理的已完成变化、删除 ID、有界 current summaries 和现有 session 元数据。
不把所有历史消息放进 prompt，不由 PG 自动做语义联想。agent 可以修订已有 summary、
为独立问题创建新 summary，或为没有有效结论的变化明确给出 `no_summary`。
不建议每个 turn 创建一份摘要。修改使用准确的 summary ID 和 expected revision；标题不是身份。

输出为 `summaries` 和 `outcomes`。每条变化和删除都必须有 outcome；`summarized` 指向本批
实际修订/新建并覆盖该消息的摘要，`no_summary` 必须有原因且不包含输出键。
遗漏、不正确的引用或一个输出失败会回滚整批，不推进部分进度。
每版 coverage 显式选择当前保留结论的来源，不自动并集全部历史，也不串联所有旧摘要版本。
历史版本不改写，当前压缩 coverage 不改变处理账本。

有 invalidation 的目标必须修订，不能靠 `no_summary` 保留旧结果。
完全没有可保留内容时，允许为该失效的既有 Node 发布固定的空结果：
标题 `Unavailable summary`，正文 `No supported content remains in this summary.`，无 claims 和来源。
这不是任意新建无依据摘要的入口。当前状态 `empty` 在 UI 明确显示；`stale` 表示可读但需更新，
不等于 empty；来源撤权则直接拒绝正文和元数据，不只加一个 badge。

## 有界上下文和只读 MCP

Manager 给 Secretary 的 MCP 子进程注入执行范围 capability，paxd 通过原有 node-auth bridge
调用 `POST /api/v1/node/agents/:agent_id/secretary/tools`。不需要用户 API key 或手工注册 Python adapter。
普通会话仍用原 conversation 工具；Secretary capability 模式只暴露五个只读工具：

| 工具 | 作用 |
| --- | --- |
| `workspace_search` | 可选的标题/正文 ES 搜索；片段是线索，不能当新原文证据 |
| `workspace_read` | 原文或明确 summary revision；整版 summary 不分页 |
| `workspace_expand` | 同一条消息的 first/full 可见正文；固定精确 hash 引用 |
| `workspace_history` | 按 session/turn/seq 有界回查上下文，按 cursor/方向继续读取 |
| `workspace_summaries` | 当前整理来源下已有 summary 的有界分页 |

`workspace_summaries` 必须传当前来源的真实 `session_id`，不是内部执行 session，也不是 subject ID。
limit 省略或 0 默认 20，最大 50。响应 `{items,next_cursor?}`，正常 item 是完整 SummaryVersion。
工具页最多 64 KiB，可能少于 limit；以 cursor 判断续页，不以条数判断结束。
单份旧摘要过大时，item 只含 `{summary_id,revision,title,requires_read:true}`，没有被截断的正文。
目录头不记入正文/版本已读账本，修改前必须调用：

```json
{"ref":{"kind":"summary","id":"summary-node-id","revision":3}}
```

把该对象传给 `workspace_read` 后获得整版。整版 JSON 计入任务累计 4 MiB 读取预算，
超限在返回之前拒绝且不记已读，不静默截断。prompt 的摘要首批也限制 64 KiB；
`summaries_has_more:true` 表示仍有成果，`summaries_cursor` 为空时可从第一页列举，不能当作已经结束。
实际完整读取才允许修订对应当前版本；目录、搜索片段和不完整历史页不能冒充已读。

可选跨 session 证据必须经可信 read/history/expand 取得精确引用及 read receipt。
不强制搜索，不强制关联；工具调用不发布内容、不推进处理位置，也不授予另一个用户的数据权限。
独立的手工 Python adapter 见 [secretary_search_mcp.md](secretary_search_mcp.md)，与自动执行工具不同。

| 预算 | 当前限制 |
| --- | --- |
| 输入变化 | 最多 64 条消息、64 个删除 ID；默认暴露文本最多 128 KiB |
| 采用的完整原文 | 按确切来源去重，整批最多 8 MiB；已存在快照也计入 |
| session 元数据 | 16 KiB |
| prompt 摘要 / summaries 工具页 | 各 64 KiB；大项转目录头后显式读整版 |
| 一次执行累计 MCP 读取 | 4 MiB，最多 512 份原文读取凭证、100 个 summary revision |
| 原始模型输出解析 | 1 MiB 的外围保护；不代表可发布 1 MiB 摘要 |
| 绑定后的发布结构 | 单摘要及整批各不超过 64 KiB，含服务端补齐来源元数据，排除快照正文 |
| 整批结构化输出 | 合计最多 100 claims、512 evidence；依赖展开最多 4096 项 |

不能按摘要数量放大整批预算。旧摘要超过整版读取预算时明确阻塞；本版没有自动拆分历史大摘要的模型迁移。

## 一次 job 的持久生命周期

`secretary_jobs` 是来源队列，batch 是固定输入，attempt 是一次实际模型调用，三者不是同一个 ID。

1. Claim 取得队列租约；Prepare 优先恢复当前 owner/source/settings/retry epoch 下的未结束批次。
   新 turn 留给下一批，不扩大旧输入或 checkpoint。空 delta 也不能绕过旧 open batch。
2. 新建批次前校验完成证明；恢复批次前校验持久范围，并重新授权所有缓存消息和 current summaries。
   原文已删除、撤权或固定版本不可用时，将 batch/current attempt superseded，滚动 epoch，返回
   `SourceVersionUnavailable`，绝不把缓存的混合正文交给 agent。
3. 持久 Begin attempt 后才执行模型；同一有效 batch 最多三次真正失败的尝试。
   prompt/schema 预算校验在尚无 attempt 时失败会直接 blocked，避免无限领取。
4. 得到输出先保存 output 和可信 reads，再解析/校验/提交。重启后存在 `output_received` 就恢复同一输出，
   不调用模型；发布成功与 checkpoint、manifest、摘要版本、来源和 ES outbox 同事务完成。
5. 恢复 `running/unknown` 只检查同一 attempt 的确切原生终态。传输 ACK、idle 或过期租约不足以证明失败；
   结果未知不会自动重发。有效新输入不替换尚未确定结果的旧 attempt。
6. 同批次相同 manifest/hash 重放得到同一组 ID/revision；不同 hash 冲突。
   关闭设置、改选 agent、设置 CAS 变化、失去权限、旧租约、错误 attempt 或迟到输出不能提交。

Batch 状态为 `pending/running/output_received/committed/blocked/unknown/superseded`；
attempt 状态为 `running/output_received/invalid/failed/unknown/canceled/superseded/committed`。
手工 run 是明确调度/重试请求，不是返回模型结果；活动执行不轮换 epoch、不制造第二次调用。
自动恢复不会因 idle、超时或有效新输入重新发送 unknown 执行。显式 run 对无有效租约的待办建立新 retry epoch；这是用户主动重新执行，旧 attempt 被隔离，迟到输出仍不能提交。

连接重试独立于模型输出校验预算。只有取得 runner 前明确返回 tunnel 未连接或已占用，才记为
`failed` 且 `not_dispatched=true`。首次失败后最多自动重试 8 次，名义间隔为
5、10、20、40、80、160、300、300 秒，附加约 20% 抖动且严格不超过 5 分钟。
`secretary_batches.connection_failures/retry_after` 与队列可领取时间同事务保存并释放租约；
等待期间不占用 worker。重启、新 turn 和原文变化不清零连接预算；自动替换过期输入的批次继承预算，
显式 run 才建立新的重试预算。第 9 次连接失败后暂停，等待用户 Retry run。
已经发出但结果不明的执行仍只回查原 attempt，不进入此自动重试流程。

## 权限、失效和状态

第一版 owner-only。Node、该版全部必要来源、来源目录和 origin 展示元数据分别授权。
单凭旧 ID、ES 命中、缓存、快照或 read receipt 不授予权限。来源删除、agent/session/node 不可读或 owner
变化时，先拦截含旧敏感内容的标题、预览、claims、正文、历史和搜索结果，再安排原整理来源修订。
跨 session 证据变更同样反向安排 origin source，不只标记被引用 session 自己的进度。
组织来源已经不可调度时，不会为修复扫描其全部历史或把旧混合正文交给模型。

普通 source 新消息只排队，不在 PG 内联想。canonical 正文没有变化的元数据更新不重复生成；
真实变化/删除可以使 current 标记 stale，历史精确版本仍按当前权限和删除规则检查。
origin source 标题或链接无权限则省略，不用显示归属绕过事实来源权限。

状态为 `disabled/idle/waiting_for_agent/waiting/processing/error`，增加可选安全枚举：

- `execution_state`: `queued/waiting/running/output_received/needs_recovery/unknown/blocked`。
- `blocked_reason`: `lease_expired/execution_unknown/invalid_output/execution_failed/canceled/retry_required/connection_backoff/connection_retries_exhausted`。

`running` 只表示持久 attempt 与有效租约，不能宣称原生端已确认运行。无待办时新字段可省略；
disabled 优先；连接退避显示 waiting，连接重试耗尽显示 error，其余执行仍保留 agent offline 状态。
UI 用白名单原因，不显示 capability 或原生原始错误。

进度统计已处理的可见逻辑消息，包括明确 `no_summary`；一条消息被多份摘要引用仍只计算一次。
它不是 coverage 行数、模型读完百分比或 ES 索引进度。进度查询有 8 秒上下文/数据库预算。

## 用户 API

所有接口沿用登录态 owner 和普通响应 envelope；不可读对象不返回敏感旧内容。

| Method | Path | 用途 |
| --- | --- | --- |
| GET / PATCH | `/api/v1/user/self/secretary` | 设置、CAS revision、兼容状态及可选执行阶段 |
| POST | `/api/v1/user/self/secretary/run` | 调度待办/明确重试，返回 202 |
| GET | `/api/v1/user/self/secretary/progress` | 总进度及来源分页 |
| GET | `/api/v1/user/self/secretary/summaries` | current 成果 `{items,next_cursor?}` |
| GET | `/api/v1/user/self/secretary/summaries/:summary_id/revisions` | 该 Node 已授权历史版本 `{items,next_cursor?}` |
| POST | `/api/v1/user/self/search/read` | 消息或确切摘要版本正文 |
| POST | `/api/v1/user/self/search` | ES 搜索，按 session 分组 |

成果/历史 limit 默认 20、最大 100；因授权过滤可能出现 items 为空但仍有 next_cursor，继续分页即可。
历史按 revision 倒序，不要求连续。返回可选 `node_type`、`node_status`、`origin_source_id`、
`origin_agent_id`、`origin_session_id`、`origin_session_title`；旧响应没有这些字段时仍可展示。
不能从旧 `subject_id` 解析 session ID。subject_type 的旧 fragment/topic 等值不是新 Node.type。
current 的 stale 状态不能套到所有历史版本。

搜索仍按原 session 分组，一组内可显示多份 summary，title/message 命中仍保留。
summary 按 origin source 归组，附加证据来源不会复制卡片；返回前复核 current revision 与权限，
过期索引命中可以丢弃并有界补取，可能暂时不足一页，不能返回旧片段或假造精确总数。
详情、原文及历史读取依赖 PG，不依赖 ES。Console 保留从原文返回结果卡片的导航状态。

## 启动、迁移和运维限制

`PostgresStore.EnsureSchema` 依次执行 `db/init.sql`、`db/summaries.sql`、`db/search.sql`、`db/secretary.sql`。
Node 身份和来源目录使用幂等元数据迁移，为旧成果复用 summary ID、current revision 和原版本/引用。
不清空处理 checkpoint，不把旧成果迁成 topic，不重算全部摘要，不因建表调用模型。
`Worker.Run` 初始化补齐缺失队列，已完成 source 不会因重启从头入队；开启且真正有 pending 工作时仍正常调度。

Worker 默认每 5 秒轮询、每次领取 1 个 source、租约 10 分钟、执行超时 5 分钟、失败 backoff 10 秒、
静默窗口 30 秒，运行中每秒检查设置/租约有效性。该版本没有增加 SECRETARY_* 配置开关；
用户开关和 agent 选择存 PG。搜索沿用 `SEARCH_URL`、`SEARCH_INDEX`、`SEARCH_API_KEY`、
`SEARCH_READ_RECEIPT_SECRET`，具体部署见[搜索契约](search_contract.md)。

发布顺序：先备份并在副本验证 schema 和历史读取，再部署同一新 Manager 版本，随后 Console，
最后升级 Secretary 所选 agent 主机的 paxd。不要让旧/新 worker 同时消费同一个队列。
旧 paxd 没有第五个工具；旧 Manager 不支持新分支时明确拒绝，不当作空页。
现有 OpenClaw 的单 session MCP 注入限制仍然存在，不能把 discovery 成功当作所有 ACP adapter 均可用。
PG 租约不是完整的多 Manager agent 连接路由；水平扩容仍需把执行请求路由到持有目标 ACP 连接的实例。

## 代码与验证

主要模块：

- `internal/manager/domain/secretary_nodes.go`、`secretary_lifecycle.go`：协议、稳定 ID 和存储接口。
- `internal/manager/storage/secretary_nodes*.go`、`secretary_node_invalidations*.go`：原子发布、来源及失效。
- `internal/manager/storage/secretary_lifecycle*.go`、`secretary_completed_sources.go`：固定批次、重启恢复、权限与完成门槛。
- `internal/manager/secretary/nodes_{prompt,output,publication,worker}.go`：提示词、结构校验、绑定、worker。
- `internal/manager/secretary_agent_nodes.go`、`secretary_tools*.go`：真实 ACP 执行和只读 capability bridge。
- `api/pax_manager.thrift`：用户路由源文件；变更后用 `make generate` 更新生成物。

相关本地验证已覆盖 memory 和每用例独立 schema 的真实 PostgreSQL、多输出原子性、manifest 幂等、
租约/设置/attempt fence、完成等待、三失败停止、未知不重发、保存输出后重启、删除/撤权后恢复拒绝。
新增后端代码要求至少 80% 单测覆盖；局部覆盖率不能代替全仓检查。

```bash
GOWORK=off GOCACHE=/tmp/pax-manager-go-cache go test ./internal/manager/secretary -count=1
# 显式指向隔离测试 PG；每个 fixture 创建自己的 schema。
PAX_MANAGER_SECRETARY_TEST_DATABASE_URL="$TEST_DATABASE_URL" GOWORK=off GOCACHE=/tmp/pax-manager-go-cache go test ./internal/manager/storage -run '^TestSecretary' -count=1 -timeout=120s
make secretary-acceptance
# 先从配套 paxd 工作区构建本地 binary，再执行真实 stdio bridge 验收。
PAX_SECRETARY_TEST_PAXD_BINARY=/tmp/pax-summary-nodes-daemon make secretary-acceptance
```

`make secretary-acceptance` 实际运行 `tools/verify_secretary.sh`，启动并清理独立 Docker PostgreSQL；
使用合成 ACP 输出验证真实 HTTP/ACP/PG 协议，不调用生产模型。
仓库完整格式、lint、单测、build 和集成门槛由合并前统一验证。
真实长对话的摘要拆分、结论保留质量和生产部署仍需另行观察，不能由固定响应测试推断。
