namespace go paxmanager.api

typedef binary JSON

struct EmptyRequest {
}

struct OKData {
  1: optional bool ok
}

struct ErrorData {
}

struct HealthData {
  1: optional string status
}

struct RegisterNodeRequest {
  1: optional string name
  2: optional string hostname
  3: optional string machine_type
  4: optional string os
  5: optional string arch
  6: optional string paxd_version
  7: optional string api_endpoint
  8: optional JSON metadata
}

struct RegisterNodeData {
  1: optional string node_id
  2: optional string api_key
}

struct RegisterNodeAgentRequest {
  1: optional RegisterNodeRequest node
  2: optional CreateNodeAgentRequest agent
}

struct RegisterNodeAgentData {
  1: optional string node_id
  2: optional string api_key
  3: optional string agent_id
  4: optional Agent agent
}

struct NodeStatusReportRequest {
  1: optional string node_id
  2: optional string hostname
  3: optional string timestamp
  4: optional list<AgentStatusInput> agents
  5: optional JSON system
  6: optional JSON metadata
}

struct AgentStatusInput {
  1: optional string agent_id
  2: optional string name
  3: optional string agent_type
  4: optional string status
  5: optional bool online
  6: optional string last_heartbeat
  7: optional JSON capabilities
  8: optional JSON metadata
  9: optional list<SessionStatusInput> sessions
}

struct SessionStatusInput {
  1: optional string session_id
  2: optional string agent_type
  3: optional string native_id
  4: optional string name
  5: optional string project_id
  6: optional string preview
  7: optional list<string> workspace_roots
  8: optional string source
  9: optional string status
  10: optional string current_task
  11: optional string last_message_at
  12: optional i32 message_count
  13: optional TokenUsage token_usage
  14: optional string model
  15: optional string run_id
  16: optional string run_status
}

struct TokenUsage {
  1: optional i64 input_tokens
  2: optional i64 output_tokens
  3: optional i64 total_tokens
  4: optional i64 cache_read_tokens
  5: optional i64 cache_write_tokens
  6: optional i64 cache_creation_tokens
  7: optional i64 reasoning_tokens
  8: optional double estimated_cost_usd
  9: optional double actual_cost_usd
  10: optional double cost_usd
}

struct FileChange {
  1: optional string path
  2: optional string tool
  3: optional string old_content
  4: optional string new_content
}

struct User {
  1: optional string user_id
  2: optional string email
  3: optional string name
  4: optional string role
  5: optional bool is_admin
  6: optional string created_at
  7: optional string last_seen_at
}

struct Node {
  1: optional string node_id
  2: optional string owner_user_id
  3: optional string name
  4: optional string hostname
  5: optional string machine_type
  6: optional string os
  7: optional string arch
  8: optional string paxd_version
  9: optional string api_endpoint
  10: optional string status
  11: optional bool online
  12: optional string last_heartbeat
  13: optional string registered_at
  14: optional JSON metadata
}

struct Agent {
  1: optional string agent_id
  2: optional string node_id
  3: optional string owner_user_id
  4: optional string name
  5: optional string agent_type
  6: optional string status
  7: optional bool online
  8: optional string last_heartbeat
  9: optional string registered_at
  10: optional JSON capabilities
  11: optional JSON metadata
}

struct AgentSession {
  1: optional i64 id
  2: optional string node_id
  3: optional string agent_id
  4: optional string session_id
  5: optional string name
  6: optional string agent_type
  7: optional string native_id
  8: optional string project_id
  9: optional string preview
  10: optional list<string> workspace_roots
  11: optional string source
  12: optional string status
  13: optional string current_task
  14: optional string last_message_at
  15: optional i32 message_count
  16: optional TokenUsage token_usage
  19: optional string model
  20: optional string run_id
  21: optional string run_status
  22: optional string created_at
  23: optional string updated_at
  24: optional JSON metadata
}

struct MailboxMessage {
  1: optional i64 id
  2: optional string message_id
  3: optional string user_id
  4: optional string owner_user_id
  5: optional string node_id
  6: optional string agent_id
  7: optional string session_id
  8: optional string message
  9: optional string message_type
  10: optional JSON payload
  11: optional string status
  12: optional string delivered_at
  13: optional string completed_at
  14: optional string result
  15: optional string error
  16: optional string created_at
  17: optional string expires_at
  18: optional string direction
  19: optional string parent_message_id
  20: optional string turn_id
  21: optional string response_id
  22: optional JSON events
  23: optional list<FileChange> file_changes
  24: optional TokenUsage token_usage
}

struct NodeListData {
  1: optional list<Node> nodes
}

struct AgentListData {
  1: optional list<Agent> agents
}

struct SessionListData {
  1: optional list<AgentSession> sessions
}

struct CurrentUserData {
  1: optional User user
}

struct MailboxPullData {
  1: optional list<MailboxMessage> messages
  2: optional i64 max_offset
  3: optional bool has_more
}

struct MailboxListData {
  1: optional list<MailboxMessage> messages
}

struct CreateNodeAgentData {
  1: optional Agent agent
  2: optional MailboxMessage bootstrap_message
}

struct CreateNodeRegistrationTokenData {
  1: optional string token
  2: optional string owner_user_id
  3: optional string expires_at
}

struct CreateUserAPIKeyData {
  1: optional UserAPIKey api_key
  2: optional string key
}

struct PullNodeMailboxRequest {
  1: optional i64 offset (api.query = "offset")
  2: optional i32 limit (api.query = "limit")
}

struct PullNodeAgentMailboxRequest {
  1: optional string agent_id (api.path = "agent_id")
  2: optional i64 offset (api.query = "offset")
  3: optional i32 limit (api.query = "limit")
}

struct PullNodeAgentSessionMailboxRequest {
  1: optional string agent_id (api.path = "agent_id")
  2: optional string session_id (api.path = "session_id")
  3: optional i64 offset (api.query = "offset")
  4: optional i32 limit (api.query = "limit")
}

struct ReportNodeMessageResultRequest {
  1: optional string message_id (api.path = "message_id")
  2: optional string status
  3: optional string result
  4: optional string error
  5: optional string completed_at
  6: optional string result_message_id
  7: optional string content
  8: optional JSON payload
  9: optional JSON events
  10: optional list<FileChange> file_changes
  11: optional TokenUsage token_usage
}

struct MarkNodeMessageDeliveredRequest {
  1: optional string message_id (api.path = "message_id")
  2: optional string delivered_at
}

struct CreateNodeOutboundMessageRequest {
  1: optional string agent_id
  2: optional string session_id
  3: optional string message_type
  4: optional string content
  5: optional string parent_message_id
  6: optional string turn_id
  7: optional string response_id
  8: optional string status
  9: optional JSON payload
  10: optional JSON events
  11: optional list<FileChange> file_changes
  12: optional TokenUsage token_usage
  13: optional string created_at
}

struct UpdateNodeMailboxOffsetRequest {
  1: optional i64 offset
}

struct GetNodeRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
}

struct GetCurrentUserRequest {
  1: optional string user_id (api.path = "user_id")
}

struct ListNodesRequest {
  1: optional string user_id (api.path = "user_id")
}

struct ListNodeAgentsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
}

struct CreateNodeAgentRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string name
  4: optional string agent_type
  5: optional JSON capabilities
  6: optional JSON metadata
}

struct GetNodeAgentRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
}

struct ListNodeAgentMessagesRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string session_id (api.query = "session_id")
  5: optional string status (api.query = "status")
  6: optional i32 limit (api.query = "limit")
}

struct CreateNodeAgentMessageRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string session_id
  5: optional string message
  6: optional string message_type
  7: optional JSON payload
}

struct ListNodeAgentSessionsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
}

struct CreateNodeAgentSessionRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string session_id
  5: optional string name
  6: optional string agent_type
  7: optional string native_id
  8: optional string project_id
  9: optional list<string> workspace_roots
  10: optional string source
  11: optional JSON metadata
}

struct GetNodeAgentSessionRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string session_id (api.path = "session_id")
}

struct ListNodeAgentSessionMessagesRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string session_id (api.path = "session_id")
}

struct CreateNodeAgentSessionMessageRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string session_id (api.path = "session_id")
  5: optional string message
  6: optional string message_type
  7: optional JSON payload
}

struct CreateNodeRegistrationTokenRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string owner_user_id
  3: optional string owner_email
  4: optional i64 expires_in_seconds
}

struct CreateUserAPIKeyRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string name
}

struct ListUserAPIKeysRequest {
  1: optional string user_id (api.path = "user_id")
}

struct RevokeUserAPIKeyRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string key_id (api.path = "key_id")
}

struct UserAPIKey {
  1: optional string key_id
  2: optional string owner_user_id
  3: optional string name
  4: optional string prefix
  5: optional string created_at
  6: optional string last_used_at
  7: optional string revoked_at
}

struct HealthResponse {
  1: optional HealthData data
  2: optional i32 code
  3: optional string message
}

struct OKResponse {
  1: optional OKData data
  2: optional i32 code
  3: optional string message
}

struct ErrorResponse {
  1: optional ErrorData data
  2: optional i32 code
  3: optional string message
}

struct RegisterNodeResponse {
  1: optional RegisterNodeData data
  2: optional i32 code
  3: optional string message
}

struct RegisterNodeAgentResponse {
  1: optional RegisterNodeAgentData data
  2: optional i32 code
  3: optional string message
}

struct PullMailboxResponse {
  1: optional MailboxPullData data
  2: optional i32 code
  3: optional string message
}

struct NodeListResponse {
  1: optional NodeListData data
  2: optional i32 code
  3: optional string message
}

struct CurrentUserResponse {
  1: optional CurrentUserData data
  2: optional i32 code
  3: optional string message
}

struct NodeResponse {
  1: optional Node data
  2: optional i32 code
  3: optional string message
}

struct AgentListResponse {
  1: optional AgentListData data
  2: optional i32 code
  3: optional string message
}

struct AgentResponse {
  1: optional Agent data
  2: optional i32 code
  3: optional string message
}

struct CreateNodeAgentResponse {
  1: optional CreateNodeAgentData data
  2: optional i32 code
  3: optional string message
}

struct SessionListResponse {
  1: optional SessionListData data
  2: optional i32 code
  3: optional string message
}

struct AgentSessionResponse {
  1: optional AgentSession data
  2: optional i32 code
  3: optional string message
}

struct MailboxMessageResponse {
  1: optional MailboxMessage data
  2: optional i32 code
  3: optional string message
}

struct MailboxListResponse {
  1: optional MailboxListData data
  2: optional i32 code
  3: optional string message
}

struct CreateNodeRegistrationTokenResponse {
  1: optional CreateNodeRegistrationTokenData data
  2: optional i32 code
  3: optional string message
}

struct CreateUserAPIKeyResponse {
  1: optional CreateUserAPIKeyData data
  2: optional i32 code
  3: optional string message
}

struct UserAPIKeyListData {
  1: optional list<UserAPIKey> api_keys
}

struct UserAPIKeyListResponse {
  1: optional UserAPIKeyListData data
  2: optional i32 code
  3: optional string message
}

service PaxManagerAPI {
  HealthResponse Health(1: optional EmptyRequest request) (
    api.get = "/api/v1/health",
    openapi.tag = "system",
    openapi.summary = "Health check"
  )

  RegisterNodeResponse RegisterNode(1: optional RegisterNodeRequest request) (
    api.post = "/api/v1/node/register",
    openapi.tag = "node",
    openapi.summary = "Register node",
    openapi.description = "Registers a paxd node with an owner-bound registration token.",
    openapi.status = "200",
    openapi.header.XRegistrationToken = "One-time registration token minted by a user."
  )

  RegisterNodeAgentResponse RegisterNodeAgent(
    1: optional RegisterNodeAgentRequest request
  ) (
    api.post = "/api/v1/node/agents/register",
    openapi.tag = "node",
    openapi.summary = "Register node agent",
    openapi.description = "Creates a node agent using either X-Pax-Key for an existing node or X-Registration-Token for first bootstrap.",
    openapi.status = "200",
    openapi.header.XPaxKey = "Existing node API key.",
    openapi.header.XRegistrationToken = "One-time registration token minted by a user."
  )

  OKResponse ReportNodeStatus(1: optional NodeStatusReportRequest request) (
    api.post = "/api/v1/node/status",
    openapi.tag = "node",
    openapi.summary = "Report node status",
    openapi.description = "Upserts node, hosted agents, and agent session status.",
    openapi.security = "nodeBearer"
  )

  PullMailboxResponse PullNodeMailbox(
    1: optional PullNodeMailboxRequest request
  ) (
    api.get = "/api/v1/node/mailbox",
    openapi.tag = "node",
    openapi.summary = "Pull node mailbox",
    openapi.description = "Returns pending node-level bootstrap or control messages.",
    openapi.security = "nodeBearer",
    openapi.query.offset = "Last processed mailbox offset.",
    openapi.query.limit = "Maximum number of messages to return."
  )

  PullMailboxResponse PullNodeAgentMailbox(
    1: optional PullNodeAgentMailboxRequest request
  ) (
    api.get = "/api/v1/node/agents/:agent_id/mailbox",
    openapi.tag = "node",
    openapi.summary = "Pull node agent mailbox",
    openapi.description = "Returns pending mailbox messages for an agent hosted by the authenticated node.",
    openapi.security = "nodeBearer",
    openapi.path.agent_id = "Agent identifier.",
    openapi.query.offset = "Last processed mailbox offset.",
    openapi.query.limit = "Maximum number of messages to return."
  )

  PullMailboxResponse PullNodeAgentSessionMailbox(
    1: optional PullNodeAgentSessionMailboxRequest request
  ) (
    api.get = "/api/v1/node/agents/:agent_id/sessions/:session_id/mailbox",
    openapi.tag = "node",
    openapi.summary = "Pull node agent session mailbox",
    openapi.description = "Returns pending mailbox messages for a specific session under an agent hosted by the authenticated node.",
    openapi.security = "nodeBearer",
    openapi.path.agent_id = "Agent identifier.",
    openapi.path.session_id = "Session identifier.",
    openapi.query.offset = "Last processed mailbox offset.",
    openapi.query.limit = "Maximum number of messages to return."
  )

  OKResponse UpdateNodeMailboxOffset(
    1: optional UpdateNodeMailboxOffsetRequest request
  ) (
    api.post = "/api/v1/node/messages/offset",
    openapi.tag = "node",
    openapi.summary = "Update node mailbox offset",
    openapi.description = "Stores the highest mailbox offset processed by the node.",
    openapi.security = "nodeBearer"
  )

  OKResponse ReportNodeMessageResult(
    1: optional ReportNodeMessageResultRequest request
  ) (
    api.post = "/api/v1/node/messages/:message_id/result",
    openapi.tag = "node",
    openapi.summary = "Report node message result",
    openapi.description = "Marks a mailbox message completed or failed.",
    openapi.security = "nodeBearer",
    openapi.path.message_id = "Mailbox message identifier."
  )

  OKResponse MarkNodeMessageDelivered(
    1: optional MarkNodeMessageDeliveredRequest request
  ) (
    api.post = "/api/v1/node/messages/:message_id/delivered",
    openapi.tag = "node",
    openapi.summary = "Mark node message delivered",
    openapi.description = "Acknowledges that the authenticated node received a mailbox message.",
    openapi.security = "nodeBearer",
    openapi.path.message_id = "Mailbox message identifier."
  )

  MailboxMessageResponse CreateNodeOutboundMessage(
    1: optional CreateNodeOutboundMessageRequest request
  ) (
    api.post = "/api/v1/node/messages/outbound",
    openapi.tag = "node",
    openapi.summary = "Create node outbound message",
    openapi.description = "Stores a structured node-to-user response message for a processed mailbox item.",
    openapi.status = "200",
    openapi.security = "nodeBearer"
  )

  CurrentUserResponse GetCurrentUser(
    1: optional GetCurrentUserRequest request
  ) (
    api.get = "/api/v1/user/:user_id/me",
    openapi.tag = "user",
    openapi.summary = "Get current user",
    openapi.description = "Returns the authenticated user profile and authorization flags.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  NodeListResponse ListNodes(1: optional ListNodesRequest request) (
    api.get = "/api/v1/user/:user_id/nodes",
    openapi.tag = "user",
    openapi.summary = "List nodes",
    openapi.description = "Lists nodes visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  NodeResponse GetNode(1: optional GetNodeRequest request) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id",
    openapi.tag = "user",
    openapi.summary = "Get node",
    openapi.description = "Returns a node visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  AgentListResponse ListNodeAgents(
    1: optional ListNodeAgentsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/agents",
    openapi.tag = "user",
    openapi.summary = "List node agents",
    openapi.description = "Lists agents hosted by a node visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  CreateNodeAgentResponse CreateNodeAgent(
    1: optional CreateNodeAgentRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/agents",
    openapi.tag = "user",
    openapi.summary = "Bootstrap node agent",
    openapi.description = "Queues an agent bootstrap request for a node visible to the current user.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  AgentResponse GetNodeAgent(1: optional GetNodeAgentRequest request) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id",
    openapi.tag = "user",
    openapi.summary = "Get node agent",
    openapi.description = "Returns an agent under a node visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier."
  )

  MailboxListResponse ListNodeAgentMessages(
    1: optional ListNodeAgentMessagesRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/messages",
    openapi.tag = "user",
    openapi.summary = "List node agent messages",
    openapi.description = "Lists mailbox messages for an agent under a node visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier.",
    openapi.query.session_id = "Filter by session ID.",
    openapi.query.status = "Filter by mailbox status.",
    openapi.query.limit = "Maximum number of messages to return."
  )

  MailboxMessageResponse CreateNodeAgentMessage(
    1: optional CreateNodeAgentMessageRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/messages",
    openapi.tag = "user",
    openapi.summary = "Create node agent message",
    openapi.description = "Queues a bootstrap or agent-level mailbox message.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier."
  )

  SessionListResponse ListNodeAgentSessions(
    1: optional ListNodeAgentSessionsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/sessions",
    openapi.tag = "user",
    openapi.summary = "List node agent sessions",
    openapi.description = "Lists sessions for an agent under a node visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier."
  )

  AgentSessionResponse CreateNodeAgentSession(
    1: optional CreateNodeAgentSessionRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/sessions",
    openapi.tag = "user",
    openapi.summary = "Create node agent session",
    openapi.description = "Creates or reserves a session before the first user message is queued.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier."
  )

  AgentSessionResponse GetNodeAgentSession(
    1: optional GetNodeAgentSessionRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/sessions/:session_id",
    openapi.tag = "user",
    openapi.summary = "Get node agent session",
    openapi.description = "Returns a session for an agent under a node visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier.",
    openapi.path.session_id = "Session identifier."
  )

  MailboxListResponse ListNodeAgentSessionMessages(
    1: optional ListNodeAgentSessionMessagesRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/sessions/:session_id/messages",
    openapi.tag = "user",
    openapi.summary = "List node agent session messages",
    openapi.description = "Lists mailbox messages for a specific session under an agent and node visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier.",
    openapi.path.session_id = "Session identifier."
  )

  MailboxMessageResponse CreateNodeAgentSessionMessage(
    1: optional CreateNodeAgentSessionMessageRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/sessions/:session_id/messages",
    openapi.tag = "user",
    openapi.summary = "Create node agent session message",
    openapi.description = "Queues a mailbox message for a specific session under an agent and node visible to the current user.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier.",
    openapi.path.session_id = "Session identifier."
  )

  UserAPIKeyListResponse ListUserAPIKeys(
    1: optional ListUserAPIKeysRequest request
  ) (
    api.get = "/api/v1/user/:user_id/api-keys",
    openapi.tag = "user",
    openapi.summary = "List platform API keys",
    openapi.description = "Lists API keys owned by the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  CreateUserAPIKeyResponse CreateUserAPIKey(
    1: optional CreateUserAPIKeyRequest request
  ) (
    api.post = "/api/v1/user/:user_id/api-keys",
    openapi.tag = "user",
    openapi.summary = "Create platform API key",
    openapi.description = "Creates a user platform API key. The plain key is returned once.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  OKResponse RevokeUserAPIKey(
    1: optional RevokeUserAPIKeyRequest request
  ) (
    api.delete = "/api/v1/user/:user_id/api-keys/:key_id",
    openapi.tag = "user",
    openapi.summary = "Revoke platform API key",
    openapi.description = "Revokes a user platform API key.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.key_id = "API key identifier."
  )

  CreateNodeRegistrationTokenResponse CreateNodeRegistrationToken(
    1: optional CreateNodeRegistrationTokenRequest request
  ) (
    api.post = "/api/v1/user/:user_id/node-registration-tokens",
    openapi.tag = "user",
    openapi.summary = "Create node registration token",
    openapi.description = "Mints an owner-bound one-time node registration token.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )
}
