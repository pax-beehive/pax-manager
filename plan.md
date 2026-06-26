# Pax Manager ACP SSE Session Run Plan

## 背景

现在前端和用户层对话仍然依赖 ACP WebSocket tunnel。前端打开 user tunnel 后，自己发送 `initialize`、可选 `authenticate`、`session/new`、`session/prompt`，manager 在 tunnel 中间负责：

- agent WebSocket 长连接和 reliablemq envelope
- manager `sess_*` 与 agent native session id 的映射
- user -> agent 时把 manager session id 改写成 native id
- agent -> user 时把 native id 改写回 manager session id
- ACP history/runtime/approval 投影

目标是新增 HTTP + SSE 形态，让前端发消息不再直接持有 ACP WebSocket。第一版用户入口：

```json
{
  "input": "用户输入",
  "session_id": "可选，续聊时传"
}
```

`agent_id` 和 `node_id` 来自 URL。`options`、`model`、`auto_approve` 等字段第一版都不暴露；只保留未来扩展空间。

## 核心判断

第一版先只定义单 agent、单 session、单次 SSE run 的行为，不讨论多 session 并发或 multiplex。

用户发起一次 session run 时只有两条合法路径：

1. 请求带 `session_id`：这是续聊。manager 必须先校验这个 session 存在、属于当前用户、属于目标 agent，并且已经有可用于 ACP 的 native session id 或可被当前 agent 识别。校验失败就返回错误；不允许拿用户给的 `session_id` 去创建一个新的 session。
2. 请求不带 `session_id`：这是新会话。manager 生成自己的 `sess_*`，发送 `initialize`，通过 ACP `session/new` 创建 native session，绑定 `sess_* -> native session id`，然后再发送第一条 `session/prompt`。

前端和 URL 永远只看 manager `sess_*`。native session id 是 manager 与 agent/paxd 之间的内部实现细节。

因此第一版 SSE 的重点不是并发，而是把这两个状态机做清楚：

- `existing session -> prompt`
- `new session -> bind native id -> prompt`

这也意味着错误语义要明确：

- 带了未知 `session_id`：`404`
- 带了不属于该 agent 的 `session_id`：`404`
- 带了还没有 native id、无法续聊的 `session_id`：`409`
- 没带 `session_id` 但 `session/new` 失败：不进入 prompt，返回/流出错误

## 模块拆分

### 1. Agent ACP Transport

只负责和 paxd/agent 之间的 WebSocket transport。

职责：

- agent tunnel register / unregister
- reliablemq envelope marshal / unmarshal
- ACK / replay
- 写 raw ACP JSON-RPC frame 到 agent
- 接收 agent raw ACP JSON-RPC frame

不负责：

- HTTP/SSE
- session run 编排
- URL/session 页面语义
- 前端连接状态

建议文件方向：

- 保留现有 `acp_tunnel.go` 中 transport 和 reliablemq 相关代码
- 逐步把 user-facing WebSocket relay 从 transport 中剥离

### 2. Conversation Handler / Runner

第一版只服务单次 session run，不做多 session multiplex。

职责：

- 续聊路径：根据 manager `session_id` 找到已有 session 和 native session id
- 新建路径：生成 manager `sess_*`，发送 ACP `initialize` 和 `session/new`，绑定 native session id
- 发送 ACP `session/prompt`
- user/HTTP 输入侧：通过现有 ACP frame middleware 把 manager session id 改写成 native session id
- agent 输出侧：通过现有 ACP frame middleware 把 native session id 改写回 manager session id
- 等待当前 run 所需的 ACP response
- 把当前 run 的 ACP update 写成 conversation stream event

实现提醒：

- 不要在 SSE 新代码里手写另一套 session id rewrite。
- 复用现有 `acpSessionIDMiddleware`，它已经负责 user-to-agent 和 agent-to-user 的 `sessionId` 改写。
- 复用现有 `acpSessionLifecycleMiddleware`，它已经负责 `session/new` response 到 manager/native session id 的绑定。
- 复用现有 `userACPFramePipeline()` / `agentACPFramePipeline()`，确保 history、runtime state、approval 等投影行为和 WebSocket tunnel 一致。

第一版需要的运行态只是当前 HTTP request 内的临时上下文，不是新的持久模型：

```text
RunContext
  agent_id              // 本次请求目标 agent
  manager_session_id    // 前端/URL 看到的 sess_*
  input                 // 本次用户输入
  current_request_id    // 当前等待的 ACP JSON-RPC response
  event_sink            // 当前 SSE writer / event channel
```

`native_session_id` 不需要作为 run context 的主字段长期持有；它应该存在于 session store / middleware 映射里，由 middleware 在转发 ACP frame 时解析和改写。

第一版可以假设同一个 agent 同一时间只有一个 active run。这样 request id 不需要先做复杂重写；只要保证当前 run 自己发出的 request 能收到对应 response。

### 3. Conversation implementation placement

第一版不新开 package，不抽公共 service/interface。实现先放在 `package manager`：

```text
pax-manager/internal/manager/conversation.go
pax-manager/internal/manager/conversation_test.go
```

原因：

- 第一版目标是替换浏览器侧 WebSocket，不是整理 manager 架构。
- 现有 ACP tunnel、pipeline、middleware 都是 `package manager` 内部未导出能力，留在同一个 package 能少写 bridge/interface。
- conversation 的 SSE writer 是 endpoint 内部 wire format，不应被其他 handler 顺手复用。

职责：

- `handleConversation`：HTTP auth/body/headers/stream lifecycle
- `runConversation`：编排 initialize、session resolve/create、prompt、stream output
- `resolveConversationSession`：带 `session_id` 时只校验和解析已有 session
- `createConversationSession`：未带 `session_id` 时创建 manager/native session 绑定
- `writeConversationEvent`：`Service` 私有方法，写默认 SSE `data: <json>\n\n` 并 flush

私有类型：

```go
type conversationRequest struct {
    SessionID string `json:"session_id,omitempty"`
    Input     string `json:"input"`
}

type conversationEvent struct {
    Type      string          `json:"type"`
    NodeID    string          `json:"node_id,omitempty"`
    AgentID   string          `json:"agent_id,omitempty"`
    SessionID string          `json:"session_id,omitempty"`
    Frame     json.RawMessage `json:"frame,omitempty"`
    Message   string          `json:"message,omitempty"`
}
```

`writeConversationEvent` 放在 `Service` 上，且保持小写：

```go
func (s *Service) writeConversationEvent(
    w http.ResponseWriter,
    flusher http.Flusher,
    event conversationEvent,
) error
```

### 4. HTTP/SSE Adapter behavior

薄适配行为，不抽公共 adapter。

职责：

- route / auth / body decode
- 调用 `s.runConversation`
- 把 run event 写成 SSE
- client disconnect 时 cancel run context

Endpoint：

```http
POST /api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/conversation
Accept: text/event-stream
Content-Type: application/json
```

`conversation` 比 `session-runs` 更贴近前端/用户语义。`session-runs` 偏内部实现词，容易让人误会它是 session 资源的子生命周期。

Response format:

- `Content-Type: text/event-stream`
- use default SSE messages only; no named `event: acp`
- each message has one `data: <json>` payload followed by a blank line
- `data` is a Pax JSON envelope
- 如果错误发生在 SSE header 写出前，使用正常 HTTP `4xx/5xx`
- 如果 stream 已经开始，只能继续写 `{type:"error"}` event，再结束 stream

```text
data: {"type":"session","session_id":"sess_xxx","agent_id":"agent_xxx","node_id":"node_xxx"}

data: {"type":"acp","session_id":"sess_xxx","agent_id":"agent_xxx","frame":{"jsonrpc":"2.0","method":"session/update","params":{}}}

data: {"type":"done","session_id":"sess_xxx"}

data: {"type":"error","message":"..."}
```

### 5. Frontend Session Runtime

替换现在的 `AgentTunnelRuntime`。

职责：

- `fetch(..., { method: "POST" })` 发起 conversation request
- 读取 `text/event-stream`，解析默认 SSE message 的 `data:` JSON
- 收到 `session` 后用 `window.history.replaceState` 更新 URL，避免强刷新
- 收到 `{type:"acp", frame}` 后复用现有 `normalizeTunnelFrame`
- 维护 timeline streaming state

注意：

- 原生 `EventSource` 不支持 POST body，所以这里应使用 `fetch` streaming parser。虽然用 `fetch` 解析，但 wire format 仍然是标准 `text/event-stream`。
- 如果将来想用原生 `EventSource`，需要拆成 `POST /conversation` 创建 run，再 `GET /conversation/:run_id/events`

## 分阶段计划

### Phase 0: 文档与边界确认

- 写清楚 current WebSocket flow
- 确认 endpoint 命名和 node-scoped 路径
- 确认第一版只做单 session run
- 确认续聊和新建 session 的互斥语义
- 确认 SSE event shape

建议结论：

- endpoint 不复用现有 `/sessions` 创建接口；使用 node-scoped `conversation`
- URL 使用 manager `sess_*`
- native session id 只做 manager 内部映射
- 带 `session_id` 必须续聊，不允许隐式创建

### Phase 1: 保守版 SSE

目标：先替换浏览器到 manager 的 WebSocket，把单 session 的新建和续聊语义做稳定。

约束：

- 仍然沿用当前 agent tunnel 单活 claim
- 同一 agent 同时只允许一个 active conversation request，冲突返回 `409`
- 不做 multiplex、不做跨 run request id 重写

实现：

- 新增 HTTP/SSE endpoint
- manager server-side 只在新建 ACP session 时发送 `initialize`
- ACP session id 改写和 session/new 绑定必须走现有 ACP middleware/pipeline
- 如果请求带 `session_id`：
  - 校验 session 存在并属于 agent
  - 找到 native session id
  - 直接发送 `session/prompt`，不发送 `initialize` / `session/new`
- 如果请求不带 `session_id`：
  - 生成 manager `sess_*`
  - 发送 `initialize`
  - 发送 `session/new`
  - 绑定 native session id
  - 发送 `session/prompt`
- agent response/update 通过 SSE 回前端
- 前端收到 `session` 后静默改 URL

风险：

- agent tunnel 被一个 run 占用时，另一个 run 返回 `409`
- 续聊 session 如果没有 native id，第一版不尝试修复，返回 `409`

### Phase 2: 更完整的 conversation runner

目标：如果第一版的 `conversation.go` 变胖，再整理模块边界。

实现：

- 视情况抽出 `conversationRunner`
- 视情况抽出 `createConversationSession`
- 视情况抽出 `resolveConversationSession`
- 视情况抽出 `promptConversationSession`
- 补 storage / tunnel 边界测试

风险：

- 不改变用户语义，只做模块边界清理；不要为了抽象提前新开 package

### Phase 3: 前端替换

目标：`session-workbench` 不再依赖 `useAgentTunnel`。

实现：

- 新增 `ConversationRuntime`
- `handleSubmit` 调 POST SSE
- timeline 合并 REST history + SSE event
- session id 首次确定后 `history.replaceState`
- 旧 WebSocket runtime 保留为 fallback 一段时间

### Phase 4: 并存与灰度

目标：保留旧 user WebSocket path，SSE conversation 成为新前端路径或可配置路径。

原则：

- 不删除旧 WebSocket tunnel；现在还有依赖。
- 前端可以通过 feature flag 或 runtime fallback 在 SSE 和 WebSocket 之间切换。
- 旧 WebSocket path 继续作为调试、兼容和回滚通道。
- 只有在未来确认没有调用方依赖时，才单独讨论 deprecation。

## 已定决策和暂缓项

1. Endpoint 用 node-scoped `conversation`：

   ```http
   POST /api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/conversation
   ```

   `session-runs` 偏技术化，因为它暴露的是内部一次 run 的生命周期；用户和前端关心的是“继续/开始一段 conversation”。`conversation` 更贴近产品语义，也避免和现有 `/sessions` 资源创建接口混淆。

2. 带 `session_id` 但该 session 没有 native id：第一版统一返回 `409`。这表示 session 存在，但当前还不能通过 ACP 续聊。

3. `options`：第一版请求体不暴露 `options`。未来需要时再加入，例如 `cwd`、`mcp_servers`，并明确它们只在新建 session 时生效，还是也允许影响续聊。

4. `model`：理论上允许覆盖，尤其新建 session 时可以进入 `session/new`。第一版不实现这个 field；请求里先假装没有 `model`。

5. `auto_approve`：之后再加。第一版不实现这个 field，也不创建临时 approval grant。

6. `run_id`：第一版先不加。当前 event 只携带 `type`、`node_id`、`agent_id`、`session_id` 和可选 `frame/message`。

7. Response frame shape：使用 Pax envelope，把 ACP frame 放到 `frame` 字段。

## Stream frame shape

有两个选择。

### Option A: 直接返回 rewritten ACP frame as JSON

```json
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_xxx"}}
```

优点：

- 前端可以最大程度复用现有 `normalizeTunnelFrame`
- 和当前 WebSocket tunnel 的消息体一致
- 第一版最少转换逻辑

缺点：

- stream event 缺少统一 metadata，比如 run id、agent id、node id、event id
- 以后混入非 ACP 事件时，前端要同时理解多种形状

### Option B: Pax envelope 包一层 ACP frame

```json
{
  "type": "acp",
  "agent_id": "agent_xxx",
  "session_id": "sess_xxx",
  "frame": {"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_xxx",...}}
}
```

优点：

- SSE payload 结构稳定，可以统一携带 `agent_id`、`node_id`、`session_id`、`run_id`
- 方便未来加 `message`、`approval`、`usage` 等非 ACP 事件
- 更像 Pax 对外 API，而不是把内部 ACP 完全暴露为顶层 schema

缺点：

- 前端需要从 `data.frame` 取 ACP frame 后再 normalize
- 比当前 tunnel 多一层兼容代码

当前决策：Option B；wire format 使用标准 `text/event-stream`，但不使用 named SSE events。每个 item 都是默认 SSE message，格式是 `data: <Pax JSON envelope>`。

## BDD 验收场景

### Scenario: 新建 conversation session

Given 当前用户有权限访问 URL 中的 `node_id` 和 `agent_id`
And 请求体没有 `session_id`
When 前端 POST `/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/conversation`
Then manager 生成新的 manager `sess_*`
And manager 向 agent 发送 `initialize`
And manager 向 agent 发送 `session/new`
And manager 绑定 `sess_* -> native session id`
And manager 向 agent 发送 `session/prompt`
And SSE 先返回 `{type:"session", session_id:"sess_*"}`
And 后续 ACP update 以 `{type:"acp", frame:{...}}` 返回，frame 中只出现 manager `sess_*`
And run 完成时返回 `{type:"done"}`

### Scenario: 续聊已有 conversation session

Given 当前用户有权限访问 URL 中的 `node_id` 和 `agent_id`
And DB 中存在属于该用户、该 node、该 agent 的 manager `session_id`
And 该 session 已绑定 native session id
When 前端 POST conversation request，body 带该 `session_id`
Then manager 不发送 `initialize`
And manager 不发送 `session/new`
And manager 直接向 agent 发送 `session/prompt`
And user-to-agent frame 中的 manager `session_id` 被改写成 native session id
And agent-to-user frame 中的 native session id 被改写回 manager `session_id`

### Scenario: 带未知 session_id

Given 当前用户有权限访问 URL 中的 `node_id` 和 `agent_id`
And DB 中不存在请求 body 带来的 `session_id`
When 前端 POST conversation request
Then manager 返回 HTTP `404`
And manager 不发送 `initialize`
And manager 不发送 `session/new`
And manager 不发送 `session/prompt`

### Scenario: 带不属于目标 agent 或 node 的 session_id

Given DB 中存在请求 body 带来的 `session_id`
But 该 session 不属于 URL 中的 `agent_id` 或 `node_id`
When 前端 POST conversation request
Then manager 返回 HTTP `404`
And manager 不向 agent 发送任何 ACP frame

### Scenario: 已有 session 没有 native session id

Given DB 中存在属于当前用户、目标 node、目标 agent 的 manager `session_id`
But 该 session 没有 native session id
When 前端 POST conversation request
Then manager 返回 HTTP `409`
And manager 不发送 `initialize`
And manager 不发送 `session/new`
And manager 不发送 `session/prompt`

### Scenario: session/new 失败

Given 请求体没有 `session_id`
And manager 已经发送 `initialize`
When agent 对 `session/new` 返回 error
Then manager 不发送 `session/prompt`
And 如果 SSE 尚未开始，manager 返回 HTTP `502`
And 如果 SSE 已经开始，manager 写入 `{type:"error", message:"..."}`

### Scenario: agent tunnel 正忙

Given 同一个 `agent_id` 已有 active conversation run
When 前端再次 POST conversation request
Then manager 返回 HTTP `409`
And manager 不向 agent 发送新的 ACP frame

### Scenario: client disconnect

Given conversation run 已经开始
When 前端断开 SSE 连接
Then manager cancel 当前 run context
And manager release 本次 run 占用的 agent claim

## 推荐先定的决策

- endpoint：`POST /api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/conversation`
- session id：URL 和前端永远使用 manager `sess_*`
- 第一版：单 agent 同时只做一个 active run，冲突返回 `409`
- 带 `session_id`：只续聊，校验失败就报错，不隐式创建
- 不带 `session_id`：新建 session，先绑定 native id，再 prompt
- session id rewrite / native id binding：复用现有 ACP middleware/pipeline，不另写
- stream：先发 `{type:"session"}`，再发 `{type:"acp", frame:{...}}`，前端取 `frame` 复用现有 normalize 逻辑
- 旧 WebSocket：保留并存，不在本计划里删除
- 后续：等单 session 路径稳定后，再讨论并发和 multiplex
