# pax-manager — Fleet Cloud API Gateway

> Pax 平台的控制面网关。路由 Dashboard UI 和 paxd Agent 之间的消息，基于 WebSocket 实时通信。

## 架构

```
Dashboard (browser) ──WebSocket──┐
                                 ├── pax-manager ──WebSocket── paxd (agent)
Dashboard (browser) ──WebSocket──┘
```

pax-manager 是一个轻量 WebSocket 中继：
- **UI → Agent**：Dashboard 发 `agent_prompt` → Gateway 变换为 paxd 可识别的 `chat` 消息 → 路由到 Agent
- **Agent → UI**：Agent 发 `model.*` 事件 → Gateway 广播到所有连接的 UI

无状态、无落库、纯透传。生产环境部署到 GCP Cloud Run。

## 端点

| 端点 | 用途 |
|------|------|
| `GET /` | Dashboard 静态页面 |
| `WS /ws` | UI WebSocket（角色自动检测） |
| `WS /api/agent/ws?key=` | Agent WebSocket（paxd 连接） |
| `POST /api/agent/register` | Agent HTTP 注册 |
| `POST /api/agent/status` | Agent 状态上报 |
| `POST /api/echo` | JSON echo（调试用） |

## 消息协议

### UI → Agent

```json
{
  "type": "agent_prompt",
  "agentId": "test-agent",
  "prompt": "hello",
  "sessionId": "api-xxx"
}
```

Gateway 自动变换为 paxd 格式：

```json
{
  "type": "chat",
  "content": "hello",
  "message_id": "msg-1",
  "session_id": "api-xxx"
}
```

### Agent → UI

Agent 发送 `model.*` 事件，Gateway 原样广播到所有 UI：

| entity_type | event_type | 含义 |
|-------------|-----------|------|
| `turn` | `started` / `done` | 对话轮次 |
| `message` | `delta` | 逐字内容（assistant/tool） |
| `tool` | `call` / `result` | 工具调用 |
| `agent` | `status` | 状态变化（thinking/working/idle） |
| `file` | `changed` | 文件变更 |

所有事件携带 `sessionId`。

## Session 连续性

```
前端 send({sessionId}) → manager routeMsg → paxd → Hermes
                                                      ↓
前端 ← manager ← paxd ←── sessionId: api-xxx ── X-Hermes-Session-Id
```

首次消息 Hermes 创建 session，paxd 从 response header 捕获 `X-Hermes-Session-Id`，通过事件回传给前端。后续消息自动带上 `sessionId` → Hermes 继续同一会话。

## 运行

```bash
# 前置：paxd + Hermes 在运行

go build -o pax-manager ./cmd/manager/
./pax-manager

# Dashboard: http://localhost:9879
```

## 部署（GCP Cloud Run）

```bash
gcloud builds submit --tag gcr.io/$PROJECT/pax-manager

gcloud run deploy pax-manager \
  --image gcr.io/$PROJECT/pax-manager \
  --port 9879 \
  --min-instances 1
```

Cloud Run 支持 WebSocket（session affinity 默认开启）。

## 开发

```bash
# 本地测试
./pax-manager                           # 端口 9879

# Mock agent（无需 Hermes）
go run ./cmd/mockagent/ ws://localhost:9879/api/agent/ws
```

## Pax 生态

| 组件 | 说明 |
|------|------|
| **pax-manager** | Cloud API Gateway（本仓库） |
| [paxd](https://github.com/pax-beehive/paxd) | Agent 侧守护进程 |
| Hermes | 本地 Agent 运行时 |

## License

MIT
