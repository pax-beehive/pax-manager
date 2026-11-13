# 统一 Node 模型：第一版只实现 summary

## Current generation amendment (2026-11-13)

Fresh Secretary jobs use `summary-sources-v2`: the model generates title, body
and selected source message IDs, without copying per-claim quotes. Manager binds
the selected IDs to authorized exact source references and freezes originals.
Source association is for traceability and does not prove semantic correctness.
There is no additional quotation alignment or model-based verifier.

Existing claims/evidence structures remain readable for immutable history and
already frozen legacy batches. They are not required for new-policy revisions.
The Node identity, summary version, coverage, snapshot, ownership, invalidation
and processing-ledger model below remains in use. This amendment supersedes
generation requirements for claims/quotes in the older sections of this document.

状态：summary 第一版由本 PR 实现；文档记录本地验证范围，尚未部署核验。
设计确认：2026-11-03；实现说明更新：2026-11-04。
本文从 `work/secretary-output-recovery` 的原设计复制，描述新工作区
`work/secretary-summary-nodes-manager` 的实现契约。旧工作区暂停的运行时补丁不作为本版实现。

本文替代先前“独立 topic + 每个 topic 一份摘要”和“长主题必须拆成摘要块”的草案。
当前代码入口、API、运行参数和验证命令见[运行说明](secretary_automation.md)，
交付状态见[问题 067](../issues/067-secretary-topic-capacity-and-execution-lifecycle.md)。
下面“未来”部分是兼容性边界，不代表已有 topic、关联或合并功能。

## 一、模型与第一版范围

**统一底层 Node，用 `type` 区分用途；第一版只允许 `type=summary`。**

- `summary` 是独立摘要，有自己的标题、正文、不可变版本和确切原文引用。
- `topic` 是未来的主题节点，可以组织多份摘要，也可以有自己的概述；本版不创建、不读写此类型。
- 摘要和主题将来使用同一种身份、版本及权限机制，不再设计一套独立的主题实体。
- 普通节点关联与事实来源引用是两回事，不能共用一种语义。

| 第一版实现 | 后续扩展，本版不实现 |
| --- | --- |
| summary 节点及其不可变版本 | topic 节点、主题目录和主题概述 |
| 一场 session 可以产生多份独立 summary | 节点之间的“收录”“相关”等普通关系 |
| 单份 summary 的增量修订、原文回查 | 跨 session 的主动归类、关联及合并流程 |
| 私有权限、进度、容量与执行恢复规则 | 共享授权、项目/方向 board、主动常驻和 draft 审批 |
| 摘要列表、详情和已有搜索能力的接入 | 知识图谱、递归聚合、按关联自动生成内容 |

第一版的自动整理对象仍是当前来源 session。模型不设置“一 session 唯一一 summary”的约束，
也不把 session ID 编码成新摘要的身份；未来扩展跨 session 不需要更换 ID 体系。
第一版不强制搜索其他 session，不自动把两个摘要合并成一个，也不维护 topic。

## 二、条目示例与身份

第一版可以得到：

| Node ID | type | 标题 | 来源 | 当前版本 |
| --- | --- | --- | --- | --- |
| S1 | summary | 搜索引擎选型 | session A 的确切消息 | 3 |
| S2 | summary | 部署故障排查 | session A 的另一些消息 | 1 |
| S3 | summary | 搜索结果展示 | session B 的确切消息 | 2 |

S1、S2 是两份摘要，不是同一份摘要的两个历史版本。
S1 的第 1、2、3 版才是同一摘要的修订历史。
同一条消息可以支持 S1、S2；两者也可以分别覆盖多个不连续的消息范围。
不要为了每个 turn 或每个小变化都新建摘要；agent 按相对独立的问题或结论判断是否拆分。

未来可新增 `T1(type=topic, title=搜索功能)`，再建立 `T1 收录 S1`、`T1 收录 S3`。
S1、S3 的身份、正文、版本和权限保持独立。本版不保存这些普通关系。
标题不是身份，同名不会自动归并；`type` 第一版不可修改，已有 summary 不自动变成 topic。

## 三、持久化结构

下列结构已落实到 `db/summaries.sql` 和 `db/secretary.sql`；继续演进原有摘要存储，避免建立两套事实源。

| 对象 | 必须保存的内容与约束 |
| --- | --- |
| Node 身份记录 `knowledge_nodes` | 稳定 `id`、`owner_user_id`、`type`、状态、`current_revision`、创建/更新时间；自动摘要另记创建时的整理 session，命名与现有设备 Node 区分 |
| 不可变版本，复用 `summary_versions` | `summary_id` 就是 Node ID；`(summary_id, revision)` 唯一；保存该版标题、正文和结构化 claims；不再要求属于另一个 topic |
| 精确事实来源，复用 `summary_sources` | 绑定具体摘要版本，以稳定 `message_id` 定位已完成原文并关联引用快照，或引用明确的子摘要 `node_id + revision` |
| 原文快照，复用 `message_snapshots` | 保存摘要采用时的完整可见正文及归属；沿用 `message_id + canonicalization_version + content_hash` 作为内部去重键，不建立逐 chunk 的消息版本系统 |
| 事实来源 session 目录 `knowledge_node_sources` | Node 与来源 session 的多对多投影，用于查看证据来源；由确切来源/已发布历史形成，不承担授权或处理进度 |
| 来源处理账本 `secretary_message_state` | 每个来源 session 中哪些已完成逻辑消息已处理，复用 hash 做幂等与异常变化检测；另记删除、无需摘要等明确结果 |
| 批次与执行记录 `secretary_batches` / `secretary_attempts` | 固定已完成消息输入、已读摘要 revision、输出提议与分配的 Node ID、尝试状态、幂等键和整批提交结果 |

第一版新增统一 Node 身份层；继续复用原有版本、来源、快照表，不另建一份平行的 `node_versions`。
表名中的 `summary` 是兼容历史命名，不表示未来 topic 必须另建身份或版本系统。
现有 `subject_type/subject_id` 是旧版摘要对象定位信息，不能直接解释成新 `Node.type`。
旧 Secretary 记录即使带有 `fragment` 等 subject 值，迁移后仍是 `type=summary`。

Node 行不再保存另一份可独立修改的摘要正文或标题，当前展示内容通过 `current_revision` 解析。
新建 Node 和首版发布一起提交，不向列表暴露没有有效首版的半成品。
未来增加类型时显式升级存储约束和接口；第一版遇到非 summary 类型必须拒绝，不能静默按 summary 处理。

来源目录不等于证据：某个 session 曾贡献内容，不代表其中所有消息都支持当前摘要。
来源目录也不是未来的 `node_relations`，不得借它偷偷实现主题收录关系。
自动摘要的 `origin_source_id` 只记录在哪个 session 的整理批次中创建，第一版不支持迁移这个整理归属。
它用于该 session 的成果列表和现有搜索分组，不是唯一事实来源，也不定义 Node 身份或权限。
第一版只自动修订当前整理 session 下的摘要；从别处读取到摘要不会自动取得更新归属。
未来全局 Node 浏览不依赖此字段；原 session 删除不级联删除 Node 身份和历史，读取仍遵守实际来源的当前权限与删除规则。

## 四、摘要怎样固化原文

每个已发布版本不可修改；“当前版本”只是 Node 上的指针。
精确引用使用 `node_id + revision`，不能只保存一个会漂移的 latest。

这里的 revision 属于摘要，不要求给原始消息另建 revision。
第一版按“消息完成后正文不可变”的正常路径设计：原文读取以 `message_id` 为入口，不要求调用者提供消息版本号。
只有已完成的内容进入总结，普通 message parts 在完成前拼装，不逐 chunk 生成可总结版本；聊天中的 tool call、tool result 和 thought/reasoning 不进入摘要原文。
hash 和快照继续复用，但分别承担异常检测和引用留档，不把原始消息做成多版本事件流。

- coverage 冻结该版本实际采用的全部消息引用，不只是带引文的几条。
- claim evidence 是 coverage 中支持某条结论的证据子集，关联同一引用快照和 quote。
- 快照针对 canonical 完整可见正文计算 hash；默认展示片段和 ES highlight 不参与定义内容身份。
- 可信读取层把实际内容、hash 和视图绑定，发布时保存采用的原文快照；agent 不自行声明一套消息版本。
- 若确有完成后编辑或异常补写，沿用 hash 检测并显式失效修订。旧快照已存在则可按引用解析；未保存且已变化则返回 `mismatch/version_unavailable`，不能用当前正文冒充当时依据。
- `last/first/full` 是同一已完成正文的视图；quote 必须出现在 agent 实际获得的文字中。
- 子摘要作为事实依据时也固定确切版本，校验依赖无环并限制展开量；不会因为对方 current 更新就改写旧版本。

稳定 message ID 由采集/入库层负责，ES 和 Secretary 不再分配一套。
同一逻辑消息的 parts 增补、拆分或重排不产生新的 message ID；完成前的拼装不触发逐 part 总结。
不新增 `message_versions` 表、不保存每个 streaming chunk 的历史，也不要求每条消息维护递增版本号。
若来源实际提供了两条不同逻辑消息，不能仅凭正文相同或 seq 相近就合并。
来源 ID 映射不可靠时保留不同身份，不用行号、正文 hash 或裸 seq 冒充身份。

## 五、增量整理与 agent 契约

当前 turn 完成、来源 session 空闲后才开始自动整理，以已持久化的完成状态为准；静默窗口只用于合并调度。
完成证明已在 `secretary_completed_sources.go` 中校验：消息自身为 complete，或同 agent/session、匹配 turn 的可靠终态覆盖到该消息。当前接受 complete 的 turn_done 或结构化 end_turn 的 stopReason=end_turn。无法确认完成则保持待处理，不消耗模型尝试；不能仅凭 idle、字符串出现 end_turn 或静默推断完成。
正常输入是尚未处理的已完成消息，以及在该 session 下整理的已有 summary；编辑、删除属于显式失效路径。
采集中的 parts 可以合并标记待处理，但在完成前不领取为总结输入，不每个 chunk 启动一次任务。
数据库不做语义联想，不根据标题、关键词或项目字段自动归类。

默认视图继续使用用户全文、assistant 的 `last` 消息视图；传输 part 不等于自然段，也不等于 turn。
保留真实 turn ID 供上下文导航。必要时 agent 主动按稳定消息锚点分页读取上下文或展开该条完整可见回复，
仍只返回用户和 assistant 的可见正文。改变视图规则需要变更输入策略版本，不能复用旧执行假装它已看到新内容。

### 聊天中的 tool 与 thought 的处理

- 被整理 session 内的结构化 tool call、tool result、thought/reasoning 不作为总结输入、coverage、claim evidence 或原文快照内容。
- 按消息与 part 的结构化类型过滤。普通 assistant 消息混有正文和 tool/thought parts 时，保留可见正文，仅排除非正文 parts；不能只看 role 就收下全部内容，也不能因此丢掉整条正文。
- `read/history/expand` 的原文读取分支复用同一可见正文规则；`first/last/full` 都不暴露工具参数、执行日志、返回值或 thought。`full` 只展开完整可见正文，读取 summary 的能力不变。
- 用户或 assistant 在正文中谈论工具、报告工具结论，仍是可总结内容。例如 assistant 正文中的“测试通过”可被引用；单独 tool result 里的测试日志不直接作为摘要依据。
- 排除的事件不计入可总结消息数，也不因其补充而生成摘要修订；采集/扫描位置仍可越过它们，不能让纯工具区段永久阻塞进度。
- 这里排除的是原 session 的工具事件。Secretary 自己仍可调用只读 MCP 搜索或读取原文；返回的授权正文可作为证据，引用指向原 message ID，其 MCP 调用日志本身不成为摘要来源。

原始工具事件继续由既有聊天与调试链路保存和展示。本设计延续正文过滤边界；若未来改变过滤口径，
必须显式升级 canonicalization 和输入策略，不能重新解释旧 hash 或旧快照，也不将文档更新视为历史成果已重算。

### 摘要输入与输出

#### Long user message previews (input policy v2)

New Secretary jobs freeze `InputPolicyVersion=user-head-tail-assistant-last-v2`.
User text up to 32 KiB remains complete; longer user messages expose only a
UTF-8-safe beginning and end within a combined 32 KiB preview budget, including
an explicit middle-omitted marker. The message is labeled `view:head_tail`,
`truncated:true`, and includes its original byte count and the available full
view. Assistant messages keep their existing last-paragraph view.

Manager code performs this projection before prompt serialization, including
the inline omission marker. Secretary receives the already projected text;
it does not decide where to cut it or calculate preview budgets. The model's
only input-view decision is whether additional original context is useful.
The existing v2 marker, slicing and frozen-policy compatibility stay unchanged.

The agent uses the visible request and the corresponding assistant reply to
decide whether the omitted paste matters. If the reply falls outside the batch,
it can use the existing history tool and turn identity to inspect context.
Reading the entire middle is optional. Existing full reads support offset/limit
and receipts, so only necessary ranges need to be retrieved. A message may be
summarized or explicitly marked `no_summary` from this selective view; processed
does not mean every character was read.

Evidence without an additional read must fit wholly within a real head or tail
segment. The marker and joins across omitted text cannot support quotes. Middle
quotes require actual authorized reads of the same canonical source version.
`head_tail` is internal to the initial prompt, not a new public tool view.
Original message IDs, full visible text, canonical hashes, snapshots and ES
documents remain unchanged.

Empty or v1 policy fields in existing frozen batches retain full-user semantics,
including verification of old outputs. New batch identities include the policy.
No historical message-state reset or summary recomputation occurs. Unsupported
future policy values fail closed. Rollback must retain a reader that understands
v2 while v2 executions or saved outputs remain unresolved.

The existing full-source safety budget still applies. Deterministic preparation
size errors block the task with their recorded reason and no checkpoint advance;
they do not enter the fixed 30-second retry loop. An actual source change or an
explicit task retry can make that preparation eligible again. This does not
restart unresolved native executions or weaken their terminal-proof rules.

#### Summary selection policy (2026-11-11)

Recurring activity is retained as a pattern, not an event log. A repeated
procedure is not necessarily repeated information. Use `no_summary` only when
a message adds no useful fact, state change or supported association; this
advances processing without a new summary revision, claim or coverage entry.
Retain representative sources sufficient to support the abstraction; do not
invent counts or infer a workflow from the session title. Important failures,
lasting fixes, workflow changes, new associations and unresolved risks still
warrant supported updates. This policy is applied by the agent; Manager does
not classify or discard messages by keywords or session identity.

Stable artifact references, repository-qualified PRs, commit IDs and source
links may connect a routine-looking event to work in another session. Preserve
useful references rather than discarding them as operational detail. When useful,
the agent searches with that identity and project/repository context, then reads
authorized original messages. A search snippet, bare PR number or matching word
does not establish the relationship. Do not assume that a merge commit and a
different commit identify the same change; keep unverified connections uncertain.
Manager does not perform these semantic associations or inject related sessions.

The current schema can express a verified connection as concise text in the
current session's summary, supported by selected messages from both sessions.
Additional original messages require exact `related_sources` references and
read receipts. This does not create a topic/relationship entity, revise the
other session's summaries or advance its processing. Other summaries remain
navigation/background rather than original evidence. Permissions and normal
source/version validation still apply. This is not a graph or board update.

Secretary writes selective memory for future follow-up. It keeps key current
conclusions, decisions, constraints, material results, corrections and unresolved
questions. It may omit routine acknowledgments, repetition, command/log detail,
exploratory dead ends and superseded intermediate states, except when they are
needed to explain a key conclusion, reproduce a result or preserve an unresolved
risk. Repeated updates should become their latest meaningful state rather than
a full chronology. Reported results and verified facts remain distinguishable.

When already revising for a meaningful change, the agent may consolidate or
omit low-value old claims and their unused coverage. Claims are optional, not
one per message or body sentence; evidence uses short exact quotes for the
facts retained. Important still-current conclusions and their supporting
sources remain. A routine-only batch does not require cleanup revisions, and
unrelated summary pages need not be enumerated.

Every changed message still receives an explicit processing outcome.
`no_summary` is valid for routine or repetitive content and advances processing
without adding that message to this batch's published coverage. It cannot excuse
skipping important new conclusions or failing to repair invalidated summaries. Source
messages remain available to authorized search/read, and immutable old summary
versions retain their original claims and coverage. No schema migration,
historical rewrite, new hard content quota or permission change is introduced.
The existing byte and claim limits remain rejection ceilings, not generation
targets. This policy guides model selection; protocol tests do not prove the
quality or runtime of a real model's summaries.

已有摘要通过有界列表提供：包括稳定 ID、current revision 和本页内容；prompt 的首批不超过 64 KiB。
`summaries_has_more`为 true 时可以调用 `workspace_summaries`；
非空 `summaries_cursor` 用于续页，cursor 为空则从第一页获取，不能据此判断已读完。
工具页也限制为 64 KiB，单份旧摘要过大时只返回
`{summary_id, revision, title, requires_read:true}` 目录头，不截断或伪造正文。
目录头不算已读，修改前须调用
`workspace_read({ref:{kind:"summary",id:summary_id,revision:N}})` 读取整版。
整版摘要读取不分页，并计入任务累计 4 MiB 读取预算；超限在响应前明确拒绝，不记入已读账本。
修改某份摘要必须已获得其完整、确切的当前版本。
不把所有历史消息、所有历史摘要或所有关联对象塞入每次 prompt。

agent 可以提出：

1. 修订已有 summary，提供 Node ID、读取的 revision 和完整的新版本内容。
2. 新建独立 summary，提供本批内稳定的提议键，由 Manager 分配 Node ID 并保存对应关系。
3. 明确记录某些已读变化不需要新增或修改摘要，例如重复表达或没有新的有效结论。

每条变化必须有处理结果，可以支持多份摘要；“无需摘要”单独记录，不能冒充已发布总结。
语义没有变化时无需制造新 revision，仍可在事务中推进处理账本。
修订输出只包含本次修改的摘要，不把未返回的其他摘要解释为删除。
第一版不提供删除/归档整份 Node 的 agent 操作；来源失效仍必须及时拦截受影响内容。
删除或编辑影响了已有摘要时，任务须列出所有待修订目标，不能仅记录“无需摘要”便让旧结果继续冒充有效成果。
已无可保留内容的摘要可发布明确的空结果修订，并在成果列表中标明，不要求新增 topic 或合并能力。

agent 可自行提取关键词，选择是否调用已有搜索，再读取结果纳入考虑；不强制搜索一次，也不自动搜索。
搜索正文不是原文证据，必须经可信 read/expand 取得确切版本。
额外读到的其他 session 消息只作显式证据，不推进那个 session 的处理位置，也不触发跨 session 的自动合并。
第一版不暴露创建 topic、维护普通关系、合并节点或更新 board 的工具。
所有发布都由 Manager 绑定来源、校验后提交；agent 不自行指定 owner、最终 revision 或直接写库。

## 六、进度与容量必须分离

需要分别回答三件事：

- 哪些已完成消息已经处理，是否检测到需要重新处理的异常变化。
- 当前这版摘要实际保留什么、依赖哪些确切来源。
- 哪些 session 历史上贡献过内容。

一条消息支持两份摘要，进度仍只计算一次；多处引用不能放大消息数。
进度基于已完成 canonical 消息身份，保留现有 hash 防重复或异常覆盖，不使用 coverage 行数、ES 分页 cursor 或索引完成量。
普通流式 parts 在完成前合并成同一待处理消息；完成后的异常补写、旧消息编辑和删除才进入失效处理。
只有元数据变化且 canonical 正文未变时不重复调用模型。
结构化 tool call/tool result、thought/reasoning 不计入 canonical 可见正文 hash，也不因其补充而触发摘要更新。
批次固定已领取的完成消息，生成期间开始的新 turn 留给之后的批次，不能被旧批次顺便标成已处理。

当前摘要记录重要结论、状态和待解决问题，不承诺保留全部历史细节。
允许压缩旧细节并调整当前 coverage；历史版本与其确切引用不改写。
当前 coverage 不再自动并集所有历史消息，也不无条件引用上一版摘要。
不能以压缩摘要为由清除处理账本，更不能以移除 coverage 冒充处理完成。

以下是当前实现的第一版预算；原始模型响应还有独立的 1 MiB 解析保护，不能用它绕过 64 KiB 发布预算：

| 范围 | 第一版约束 |
| --- | --- |
| 本批输入 | 最多 64 条新增/变化消息、64 个删除 ID、128 KiB 默认暴露文本；完整正文预算独立限制为 8 MiB |
| 已有摘要上下文 | prompt 首批和 summaries 工具页各不超过 64 KiB；单大项返回 requires_read 目录头，再按确切引用读取整版，累计读取最多 4 MiB |
| 摘要发布结构 | 单摘要和整批结构都不得超过 64 KiB；包括全部待发布版本的标题、正文、claims 及服务端补齐的来源元数据，不包含原文快照正文，不能按摘要数量放大预算 |
| 结构化输出 | 本批合计不超过 100 条 claims、512 条 evidence；不是每份摘要各自用满 |
| 来源依赖 | 沿用 4096 项展开保护；单摘要及整批实际展开都需有界，超限拒绝，不静默丢引用 |
| 列表、原文读取和搜索 | 列表、消息正文和搜索分页，摘要整版读取不分页；服务端设置默认值、字节预算和硬上限，大 limit 不得绕过预算，搜索分页不充当处理 checkpoint |

这些限制放在同一套版本化策略里供提示词、校验与测试共用；接口同时约束原始响应大小，
摘要结构按 UTF-8 序列化及服务端补齐后的结构计算，排除原文快照正文、既有历史版本和临时读取凭证。
本批证据涉及的 canonical 完整正文按精确消息版本去重累计计算 8 MiB 预算，已存在快照也计入，
不能靠 read/expand 额外引入来源绕过；展开文本和分页工具读取同时受单次及任务累计读取预算约束。
读取预算耗尽要明确返回原因，不能无限分页或无限展开；这些读取预算同样进入输入策略并在工具调用前核验。
不能用模型输出字数或固定预留几个字节估算最终大小。
不规定一场 session 必须拆成几份摘要，也不通过多建节点绕过整批上限。
超限时明确缩小批次、压缩或停止并报告原因；不得无限提高上限或反复重放同一份不合法输出。
如果未来要求“当前摘要保存全部历史事实”，必须重新设计容量策略。

## 七、一次多摘要发布必须原子完成

固定本批已完成消息 ID、实际输入及视图、异常变化/删除集合、设置版本、输入策略和已读摘要 revision。
工具读取追加确切引用，复用 hash 记录实际内容；重试不切换输入，不要求增加消息版本号。

一次数据库事务同时提交：

- 本次新建的 summary Node、各新版本及 current 指针。
- 精确来源、完整快照、来源 session 目录投影。
- 每条输入变化的处理结果、来源 checkpoint、执行提交状态。
- 整批结果清单以及等待同步到 ES 的持久化变更。

每个输出有自己的稳定幂等请求号，整批另有 commit ID 和结果清单。
兼容现有 `UNIQUE(owner_user_id, request_id)` 时，不能让所有输出共用一个 request ID。
分配的 Node ID 必须随提议键持久化，响应丢失后返回同一组 ID 和版本，不能重复创建。
空输出批次也要验证处理结果、记录结果清单和推进 checkpoint，不能假装发布过摘要。

任一输出不合法、读版本冲突、设置变更、权限失效或租约不再有效，都不提交本批其他成果或进度。
多次调用单摘要 PublishSummary 不能代替这个事务。
同时更新同一 Node 时用 expected revision 检测冲突；重新读取并调整，不能覆盖另一份已提交结果。
模型生成期间不持有数据库事务锁。
ES 同步失败只重试索引，不重新调用模型或回滚已经提交的摘要。

## 八、权限与源内容失效

第一版只支持本人拥有的 summary，默认私有；所有者从登录态或可信任务上下文取得。
选中某个 agent 作为 Secretary，不会给它管理员身份或超出用户与本次任务范围的能力。
第一版无需提前实现共享 ACL，但必须保留独立 Node 授权入口，不能把未来权限绑定死在某个 session 上。

- 读取需要 Node 权限，以及该版全部必要事实来源的当前权限；来源 session 目录不代替授权。
- 任一必要来源不可读，默认拒绝整版混合内容，包括标题、预览和正文。可以重算只使用可读来源的新版本，不能只隐藏原文按钮。
- 列表、搜索、计数、历史读取和源消息展开都重新授权；不可读 Node 的存在性和元数据不能泄露。
- 历史来源目录中的每个 session 也单独授权后再展示名称、数量和入口；只通过当前版本的依赖检查不够。
- ES、缓存和读取凭证不是最终权限依据。撤权提交后发起的新请求必须被拦住，不能等待索引或 agent 重写。
- 旧快照、旧 ID 和历史版本不绕过当前所有权、删除与保留规则；不可变不表示永久可读。
- 原文编辑后标记受影响的当前摘要过时，并按确切引用安排有限重算；来源删除/撤权则先即时阻止泄露，再修订。
- 同一消息可影响多份摘要，通过反向来源查询定位；不能只修复该 session 的某一个固定 summary ID。

摘要整理归属为 A、引用了 B 的证据时，B 的变化须持久记录失效事件，将修订交给 A 或独立受限修订任务。
B 的普通批次不能跨整理归属直接修改摘要，也不能仅推进自身 checkpoint 就丢掉修订责任。
失效任务独立于普通消息 checkpoint；A 已删除或不能调度时，明确标记修订阻塞，需隐藏的内容继续隐藏，
不为修订重新扫描 A 的全部历史。只有元数据索引修复可以自动完成，重新生成仍受 Secretary 开关和任务授权约束。
修订不得把已失去必要来源权限的旧混合标题、正文或 claims 塞回 agent；只能用当前可读的原文安全重建，
或发布不含旧敏感内容的空结果。无法安全重建时保持阻塞，维护任务不能绕过读取权限。

普通关联将来也不授予任何权限。未来 topic 概述若引用其他节点作为事实依据，
其精确依赖同样参与权限检查；单纯收录一个节点不表示概述依赖该节点全部内容。

## 九、执行恢复仍是独立的交付条件

Node 类型、多份摘要和容量限制不会自动修好执行中断。
当前持久化结构区分三个生命周期，分别位于 `db/secretary.sql`：

| 表 | 一行代表什么 | 责任 |
| --- | --- | --- |
| 现有 `secretary_jobs` | 一个来源 session 的合并队列 | 待处理变化、checkpoint 和调度时间，不充当完整执行历史 |
| 新增 `secretary_batches` | 一批固定输入的总结任务 | 输入清单、已读摘要 revision、当前有效 attempt、批次状态、幂等提交清单 |
| 新增 `secretary_attempts` | 一次实际 agent 调用 | batch ID、原生执行关联、运行状态、持久输出、错误、开始/结束时间 |

同一 batch 可以有限重试并产生多个 attempt；新 turn 的输入属于后续 batch，不能追加到正在执行的 batch。
状态变更检查当前 attempt 与租约；旧 attempt 迟到不能覆盖新执行。摘要发布与 batch 提交成功、checkpoint 推进同事务完成。
每次实际调用前写入 attempt，不能只从聊天记录猜状态。
批次状态是 `pending/running/output_received/committed/blocked/unknown/superseded`；
attempt 状态是 `running/output_received/invalid/failed/unknown/canceled/superseded/committed`。
本版没有新建 draft 审批或常驻 agent 协议；原生权限等待继续服从现有 ACP 链路。

Prepare 优先恢复当前来源、owner、settings revision 和 retry epoch 下已有的未结束批次，
新 turn 不扩大其固定输入或 checkpoint。恢复前再次核验持久输入的范围身份，并重新授权每条消息和每版摘要。
若源已删除、撤权或固定输入不可用，批次及当前 attempt 设为 superseded，滚动 retry epoch，
返回 SourceVersionUnavailable，不将旧混合正文再次交给 agent。即使新领取的 delta 为空，也先处理旧批次，不能直接跳过。

收到输出和可信读取账本先保存，重启后继续校验或整批提交，不重新调用模型。
固定批次最多三次实际失败尝试（首次加两次重试）；没有完成证明的等待不计尝试。
未调用模型前的 prompt/schema 容量失败直接阻塞，避免无限领取。
真正失败后才消耗对应重试机会。
最终失败后停止空转并等待明确重试或有效新输入；安排了最后一次重试不代表它已经失败。
新尝试取代旧尝试后，迟到输出不能提交当前结果；租约续期不等于 agent 确实仍在运行。

模型派发前的连接失败使用独立预算，不占三次输出校验机会。runner 尚未取得时明确的 tunnel
未连接或已占用可记为 `not_dispatched`，持久化连接失败次数和下一次可重试时间，并立即释放租约。
最多自动重试 8 次，间隔从 5 秒倍增到最多 5 分钟，附加约 20% 抖动。重启、新 turn、原文变化
及自动替换固定输入均保留剩余预算；耗尽后暂停，显式 run 才重置。不能据此重发结果未知的原生执行。

传输 ACK 不等于原生端已执行，普通 idle 快照不能证明没有待执行请求。
只有确切终态才能继续恢复和校验；当前实现不会自动重发结果未知的原生执行，不足则明确显示结果未知，不能猜测并重复调用。
设置关闭、agent 离线、待审批、源 session 忙和验证阻塞须显示实际原因，不统称“处理中”。

进度同时区分已读处理、已发布摘要与待处理/阻塞，不用一次租约或旧错误推断实际执行阶段。
状态 API 增加可选的 `execution_state` 和白名单 `blocked_reason`，保留原有 state。
`running` 只证明持久 attempt 和租约仍有效，不能宣称已向原生端确认执行。
本版已增加 memory/独立 PostgreSQL 的生命周期、权限恢复及边界契约测试；
确定性测试不代替真实长对话质量或部署验证。

## 十、查看、搜索与导航

第一版提供当前 session 的 summary 列表、正文详情、历史版本和确切原文入口。
不能只有进度页；零份摘要、明确无需摘要、待处理与失败要能区分。
摘要搜索命中返回稳定 Node ID、确切 revision、标题、片段及有界来源信息，读详情再查 PostgreSQL。
从原文跳回时保留选中摘要、revision、分页和滚动位置。

会话结果继续按 session 分组，保留已有每页 20 个 session 的口径；一组内可以显示多份摘要，不能重复算成多个 session。
第一版自动摘要按 `origin_source_id` 归入创建它的整理 session；额外证据来源不自动多建一张同内容卡片。
这个展示归属不表示“主要事实来源”；详情按权限展示完整来源目录，Node ID 始终独立。
按 ID 访问或按来源目录列举时不依赖 ES；没有 ES 时搜索明确报告不可用，不暗中执行 PG 全表联想检索。
索引仅投影 current 且可展示的版本；历史内容仍按明确引用回源读取。
返回搜索结果前同时核验命中 revision 是否仍是 current；过期命中不能把旧标题或片段当成当前内容返回。
第一版可丢弃过期命中，并用索引自身 cursor 有界补取；索引滞后可能暂时少于一页，不伪造新的匹配片段或精确总数。

未来跨 session 的 Node 搜索应按 Node 身份去重；与会话结果混排时要另定分页协议，不能继续把混合项叫作“20 个 session”。
按某个 session 筛选只表示来源相关，不代表摘要只依赖该 session，也不能缩小实际授权检查范围。

## 十一、未来扩展边界

以下只约束兼容性，不进入第一版工具、任务或验收功能范围：

- 新增 `type=topic`，沿用 Node ID、版本和授权机制，不新增一套 topic 身份。
- `node_relations` 连接稳定 Node ID，例如“收录”“相关”；关系可以增加、撤销或调整，历史修订不会阻止撤销当前关系。
- 普通关系独立于不可变事实来源；删关系不修改旧摘要，新版本移除来源引用也不自动删除导航关系。
- 普通关系不拼接正文、不递归读取整个关系网、不推进其他来源进度、不自动令另一方摘要失效，也不传播授权。
- 展开关系需有界分页并逐个授权；无权目标的标题、预览、存在性与数量都不泄露。
- 允许跨 session 继续修订已有 summary，或保留独立摘要后建立弱关联；不按同名自动去重，不强迫每次写入先找唯一全局主题。
- 合并是另一个明确操作，核验两边管理权限和版本，默认要求相同归属/可见范围；不取读者并集。
- 合并保留旧 ID、历史版本和确切原文；普通旧链接可以跳转，精确旧版引用仍解析旧版，不能变成目标 latest。
- 合并需禁止循环并检测冲突，不无条件并集历史 coverage，不推进来源 checkpoint，不把全部合并历史串成无限依赖链。
- 跨类型关联、共享、合并及关系管理权限在相应功能接入时再显式定义，不能靠普通可读权限隐式允许。

## 十二、迁移与发布顺序

1. 为已有成果补 Node 身份，优先复用原 `summary_id`，类型统一为 summary；保留所有版本、快照和旧引用解析。
2. 移除新工作流的一 session 一 summary 假设，引入有界摘要列表、多输出提议、CAS 与原子批次发布。
3. 将已处理账本、当前 coverage 和来源目录分离，统一容量口径；接通摘要成果查看及现有索引投影。
4. 部署含批次/attempt 表、失败停止与恢复的新 Manager；同一队列不混跑旧 worker。配套部署 Console，所选 agent 主机升级支持第五个只读工具的 paxd。
5. 如需把旧大摘要拆成多份，单独运行有进度、有限批次、可断点续跑的整理任务；不因新增 Node 表自动调用模型。

元数据迁移不清空消息 checkpoint，不把旧成果迁成 topic，也不改变历史引用含义。
兼容读取已有格式合法的旧版本，不因新整批预算追溯性抹掉成果；旧摘要接近上限时，修订需显式压缩或走独立整理任务。
不能把超限旧摘要直接反复塞进新协议并自动重试，也不能在元数据迁移时静默丢掉旧 evidence。
旧的手工单摘要入口仍是独立的原子写入，遵守 Node 当前版本 CAS；自动 Secretary 走新的整批发布路径，不能用手工多次发布替代整批事务。
新旧 worker 通过明确版本切换，启用后仍只消费实际待处理变化；重启不能清队列、从头重算或重新调用全部历史 session。
历史整理有独立游标和进度，不能借用搜索 cursor 或伪装成普通已处理 checkpoint。
`PostgresStore.EnsureSchema` 依次应用 `db/init.sql`、`db/summaries.sql`、`db/search.sql`、`db/secretary.sql`。
Node 身份和来源目录使用有版本标记的幂等元数据迁移；已有 summary ID、历史版本和处理位置保留。
`Worker.Run` 的初始化 backfill 补齐缺失队列，不重置已处理的 session。
启动本身没有“调用模型迁移全部历史”的步骤；已启用且真实待处理的工作仍会按正常调度执行。
旧的超大摘要可以按目录头定位、显式读取后压缩修订；超过整版读取预算时明确失败，
本版不自动执行拆分或全面重算。发布前应备份 PG，先在副本验证 schema、运行单元及独立 PG 验收；
不要把新表创建成功当作可随意回退旧 worker 的保证。

## 十三、第一版验收

- 一场 session 讨论两个相对独立问题，得到两份 summary；续聊其中一个只修订对应 Node，另一份保留。
- 普通 streaming parts 在完成前不触发总结；消息完成后只处理一次，结构化 tool call/tool result、thought/reasoning 不进入摘要输入或来源。
- 混合消息保留可见正文，正文中的工具结论可总结；`full` 展开也不返回工具/思考内容。纯工具区段不计入消息总数，扫描仍能前进。
- Secretary MCP 返回的授权原文可被正确引用，但调用日志自身不进入 coverage；只补充聊天 tool/thought parts 不触发重新生成。
- 一条消息支持两份摘要、多个 parts 组成一条消息时，身份和进度正确；异常完成后编辑走失效路径，不引入逐 chunk 消息版本。
- 摘要来自不连续消息；coverage 冻结全部实际采用的版本，claim 引文只是子集；旧版可精确回查。
- 默认 last 以外的引文必须有实际读取证明；旧内容未保存时明确拒绝，不用当前正文替代。
- 明确无需摘要的消息仍可处理完成，但不增加已发布摘要数；压缩 coverage 不修改历史和处理账本。
- 多摘要中任一结果失败，全部回滚；响应丢失后重试返回同组 ID/版本；空输出批次也幂等。
- 并发修订、生成期间源变化、设置关闭、过期租约和迟到结果均不覆盖或误标新数据。
- 长期多轮接近容量上限时，最终绑定大小、整批大小和来源展开均受限，不靠多建摘要绕过限制。
- 拒绝 topic、普通关系、合并和 board 操作；没有隐藏的 PG 语义联想或强制跨 session 搜索。
- 搜索/列表/计数/历史读取均校验 Node 与全部必要来源；源删除、撤权和缓存滞后时不泄露。
- 可选跨 session 证据失效后，修订被正确投递且不越过整理归属或读取权限；无法安全重建时明确阻塞，不重跑全部历史。
- ES 停机仍可按 ID 读取成果；恢复索引不重新生成摘要；UI 能看到实际内容并来回定位原文。
- 在输出保存、发布事务、请求确认等持久化边界前后模拟重启，验证模型调用次数、状态、checkpoint 和引用一起正确。
- 最后一次实际失败停止空转；未知原生执行状态不重复调用；重启和元数据迁移不重置队列。

测试优先 BDD/TDD，Go 单测优先 testify 风格；新增后端代码达到至少 80% 单测覆盖率。
固定响应只能验证协议，另需真实长对话评估摘要划分与保留结论的质量；测试数量和覆盖率不能单独证明上线可靠性。
未来 topic、关联和合并另有验收，不作为 summary 第一版的交付前提。

## Owner guidance and human completion

Owner controls extend the summary-only workflow without adding a parallel summary
entity, an association graph, or another model pipeline.

- `secretary_guidance` stores owner defaults (`scope=user`) and persistent
  session preferences (`scope=session`, canonical Manager source ID). Each scope
  has an independent optimistic revision. Empty text clears that scope. Guidance
  is limited to 8192 UTF-8 bytes and 4096 Unicode characters per scope.
- Fresh automatic batches freeze both applicable scopes. An explicit operation
  freezes its scopes during preparation and adds one-operation instructions on
  submission. The same snapshot follows all batches and retries. Precedence is
  run, session, then user; source verification, permissions, output format and
  bounded execution remain fixed. Source text is not configured guidance.
- Saving preferences affects future work only. It does not reset checkpoints,
  change settings revisions, supersede frozen work, or invoke a model.
- `secretary_operations` records a reviewable draft and its exact complete-message
  targets. Kinds are `guided_summary`, `human_summary`, and `human_edit`; scopes
  are `pending`, `all`, or explicit `selected` message IDs. Preparation rejects
  more than 4096 targets rather than silently truncating. Incomplete messages
  and tool/reasoning-only content cannot be targets.
- Submission requires the draft revision and an owner-scoped idempotency key.
  The source versions and baseline summary revisions must still match. Confirmed
  replacement fences earlier work by changing its execution epoch; it does not
  assume that an unknown native execution was canceled. Late results cannot
  publish. A changed payload with the same request ID is a conflict.
- Human completion publishes an ordinary immutable summary version and marks
  exactly the selected message change revisions processed, in one transaction.
  Representative provenance is a separately selected subset (at most 512
  references); it does not determine which messages are acknowledged. Gaps
  remain pending. The contiguous source cursor cannot skip an earlier gap.
- For selected current source changes, manual completion also acknowledges the
  same-origin repair notifications. Earlier summaries remain stale and their
  immutable contents remain available; they are not silently repaired or
  deleted. A later source change resets that acknowledgement and queues new
  work. Unselected messages and other origin sessions are unaffected.
- Human edit creates the next revision of the same summary and inherits precise
  source references on the server. It acknowledges no additional messages.
  Authorship is recorded as `user`, `agent`, or `legacy_unknown`; historical
  writes are not retroactively attributed to an agent.
- Guided work reuses existing batches and attempts, including bounded retries,
  saved output recovery and unknown-result handling. Explicit reprocessing can
  remain queued even when all current messages were previously processed.
  Message counts stay complete while that new operation runs. Terminal work
  needing intervention remains visible; it is not silently retried forever.
- Human writes work with Secretary disabled. Guided execution requires enabled
  scheduling and a selected owned agent. Existing session pause controls remain
  separate from manual completion. Access is checked again on reads and writes.

The Console exposes default guidance in Secretary settings and session guidance,
one-operation guidance, manual completion and immutable human edits in session
controls. The current selection cap is explicit; advanced selected-ID input is
the escape hatch for a source larger than one preparation window. Search and
summary reads keep their existing PG source-of-truth and asynchronous ES
projection behavior. This change does not deploy, bulk retry, or regenerate
historical summaries automatically.
