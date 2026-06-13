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

struct RegisterAgentRequest {
  1: optional string name
  2: optional string agent_type
  3: optional string hostname
  4: optional string machine_type
  5: optional string os
  6: optional string hermes_version
  7: optional string api_endpoint
  8: optional JSON metadata
}

struct RegisterAgentData {
  1: optional string agent_id
  2: optional string api_key
}

struct AgentStatusReportRequest {
  1: optional string agent_id
  2: optional string hostname
  3: optional string timestamp
  4: optional list<SessionStatusInput> sessions
  5: optional JSON system
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
}

struct PullMailboxRequest {
  1: optional i64 offset (api.query = "offset")
  2: optional i32 limit (api.query = "limit")
}

struct PullSessionMailboxRequest {
  1: optional string session_id (api.path = "sessionId")
  2: optional i64 offset (api.query = "offset")
  3: optional i32 limit (api.query = "limit")
}

struct Agent {
  1: optional string agent_id
  2: optional string owner_user_id
  3: optional string name
  4: optional string hostname
  5: optional string agent_type
  6: optional string machine_type
  7: optional string os
  8: optional string hermes_version
  9: optional string api_endpoint
  10: optional string status
  11: optional bool online
  12: optional string last_heartbeat
  13: optional string registered_at
  14: optional JSON metadata
}

struct AgentListData {
  1: optional list<Agent> agents
}

struct GetAgentRequest {
  1: optional string agent_id (api.path = "agentId")
}

struct AgentSession {
  1: optional i64 id
  2: optional string agent_id
  3: optional string session_id
  4: optional string name
  5: optional string agent_type
  6: optional string native_id
  7: optional string project_id
  8: optional string preview
  9: optional list<string> workspace_roots
  10: optional string source
  11: optional string status
  12: optional string current_task
  13: optional string last_message_at
  14: optional i32 message_count
  15: optional i64 token_input
  16: optional i64 token_output
  17: optional i64 token_usage
  18: optional string model
  19: optional string run_id
  20: optional string run_status
  21: optional string created_at
  22: optional string updated_at
}

struct ListAgentSessionsRequest {
  1: optional string agent_id (api.path = "agentId")
}

struct GetAgentSessionRequest {
  1: optional string agent_id (api.path = "agentId")
  2: optional string session_id (api.path = "sessionId")
}

struct SessionListData {
  1: optional list<AgentSession> sessions
}

struct CreateAgentMessageRequest {
  1: optional string agent_id (api.path = "agentId")
  2: optional string session_id
  3: optional string message
  4: optional string message_type
  5: optional JSON payload
}

struct CreateSessionMessageRequest {
  1: optional string agent_id (api.path = "agentId")
  2: optional string session_id (api.path = "sessionId")
  3: optional string message
  4: optional string message_type
  5: optional JSON payload
}

struct MailboxMessage {
  1: optional i64 id
  2: optional string message_id
  3: optional string user_id
  4: optional string owner_user_id
  5: optional string agent_id
  6: optional string session_id
  7: optional string message
  8: optional string message_type
  9: optional JSON payload
  10: optional string status
  11: optional string delivered_at
  12: optional string completed_at
  13: optional string result
  14: optional string error
  15: optional string created_at
  16: optional string expires_at
}

struct MailboxPullData {
  1: optional list<MailboxMessage> messages
  2: optional i64 max_offset
  3: optional bool has_more
}

struct MailboxListData {
  1: optional list<MailboxMessage> messages
}

struct ReportMessageResultRequest {
  1: optional string message_id (api.path = "messageId")
  2: optional string status
  3: optional string result
  4: optional string error
  5: optional string completed_at
}

struct UpdateMailboxOffsetRequest {
  1: optional i64 offset
}

struct ListAgentMessagesRequest {
  1: optional string agent_id (api.path = "agentId")
  2: optional string session_id (api.query = "session_id")
  3: optional string status (api.query = "status")
  4: optional i32 limit (api.query = "limit")
}

struct ListAgentSessionMessagesRequest {
  1: optional string agent_id (api.path = "agentId")
  2: optional string session_id (api.path = "sessionId")
}

struct CreateRegistrationTokenRequest {
  1: optional string owner_user_id
  2: optional string owner_email
  3: optional i64 expires_in_seconds
}

struct CreateRegistrationTokenData {
  1: optional string token
  2: optional string owner_user_id
  3: optional string expires_at
}

struct CreateUserAPIKeyRequest {
  1: optional string name
}

struct RevokeUserAPIKeyRequest {
  1: optional string key_id (api.path = "keyId")
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

struct CreateUserAPIKeyData {
  1: optional UserAPIKey api_key
  2: optional string key
}

struct UserAPIKeyListData {
  1: optional list<UserAPIKey> api_keys
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

struct RegisterAgentResponse {
  1: optional RegisterAgentData data
  2: optional i32 code
  3: optional string message
}

struct PullMailboxResponse {
  1: optional MailboxPullData data
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

struct CreateRegistrationTokenResponse {
  1: optional CreateRegistrationTokenData data
  2: optional i32 code
  3: optional string message
}

struct CreateUserAPIKeyResponse {
  1: optional CreateUserAPIKeyData data
  2: optional i32 code
  3: optional string message
}

struct UserAPIKeyListResponse {
  1: optional UserAPIKeyListData data
  2: optional i32 code
  3: optional string message
}

service PaxManagerAPI {
  HealthResponse Health(1: EmptyRequest request)
    (api.get = "/health", openapi.tag = "system", openapi.summary = "Health check")

  RegisterAgentResponse RegisterAgent(1: RegisterAgentRequest request)
    (api.post = "/api/agent/register", openapi.tag = "agent", openapi.summary = "Register agent", openapi.description = "Registers a paxd agent with an owner-bound registration token.", openapi.status = "201", openapi.header.XRegistrationToken = "One-time registration token minted by a user.")

  OKResponse ReportAgentStatus(1: AgentStatusReportRequest request)
    (api.post = "/api/agent/status", openapi.tag = "agent", openapi.summary = "Report agent status", openapi.description = "Upserts agent and session status using the paxd session shape.", openapi.security = "agentBearer")

  PullMailboxResponse PullMailbox(1: PullMailboxRequest request)
    (api.get = "/api/agent/mailbox", openapi.tag = "agent", openapi.summary = "Pull agent mailbox", openapi.description = "Returns pending mailbox messages for the whole agent. Prefer the session-scoped mailbox when the paxd connection is bound to a session.", openapi.security = "agentBearer", openapi.query.offset = "Last processed mailbox offset.", openapi.query.limit = "Maximum number of messages to return.")

  PullMailboxResponse PullSessionMailbox(1: PullSessionMailboxRequest request)
    (api.get = "/api/agent/sessions/:sessionId/mailbox", openapi.tag = "agent", openapi.summary = "Pull session mailbox", openapi.description = "Returns pending mailbox messages for a specific session owned by the authenticated agent.", openapi.security = "agentBearer", openapi.path.sessionId = "Session identifier.", openapi.query.offset = "Last processed mailbox offset.", openapi.query.limit = "Maximum number of messages to return.")

  OKResponse UpdateMailboxOffset(1: UpdateMailboxOffsetRequest request)
    (api.post = "/api/agent/messages/offset", openapi.tag = "agent", openapi.summary = "Update mailbox offset", openapi.description = "Stores the highest mailbox offset processed by the agent.", openapi.security = "agentBearer")

  OKResponse ReportMessageResult(1: ReportMessageResultRequest request)
    (api.post = "/api/agent/messages/:messageId/result", openapi.tag = "agent", openapi.summary = "Report message result", openapi.description = "Marks a mailbox message completed or failed.", openapi.security = "agentBearer", openapi.path.messageId = "Mailbox message identifier.")

  AgentListResponse ListAgents(1: EmptyRequest request)
    (api.get = "/api/user/agents", openapi.tag = "user", openapi.summary = "List agents", openapi.description = "Lists agents visible to the current user.", openapi.security = "cloudflareAccess")

  AgentResponse GetAgent(1: GetAgentRequest request)
    (api.get = "/api/user/agents/:agentId", openapi.tag = "user", openapi.summary = "Get agent", openapi.description = "Returns an agent visible to the current user.", openapi.security = "cloudflareAccess", openapi.path.agentId = "Agent identifier.")

  SessionListResponse ListAgentSessions(1: ListAgentSessionsRequest request)
    (api.get = "/api/user/agents/:agentId/sessions", openapi.tag = "user", openapi.summary = "List agent sessions", openapi.description = "Lists session snapshots for an agent visible to the current user.", openapi.security = "cloudflareAccess", openapi.path.agentId = "Agent identifier.")

  AgentSessionResponse GetAgentSession(1: GetAgentSessionRequest request)
    (api.get = "/api/user/agents/:agentId/sessions/:sessionId", openapi.tag = "user", openapi.summary = "Get agent session", openapi.description = "Returns a session snapshot under an agent visible to the current user.", openapi.security = "cloudflareAccess", openapi.path.agentId = "Agent identifier.", openapi.path.sessionId = "Session identifier.")

  MailboxListResponse ListAgentMessages(1: ListAgentMessagesRequest request)
    (api.get = "/api/user/agents/:agentId/messages", openapi.tag = "user", openapi.summary = "List agent messages", openapi.description = "Lists mailbox messages for an agent visible to the current user. This is an aggregate view; use the session-scoped route for a specific conversation.", openapi.security = "cloudflareAccess", openapi.path.agentId = "Agent identifier.", openapi.query.session_id = "Filter by session ID.", openapi.query.status = "Filter by mailbox status.", openapi.query.limit = "Maximum number of messages to return.")

  MailboxMessageResponse CreateAgentMessage(1: CreateAgentMessageRequest request)
    (api.post = "/api/user/agents/:agentId/messages", openapi.tag = "user", openapi.summary = "Create agent-level message", openapi.description = "Queues a bootstrap or agent-level mailbox message. Once a session exists, prefer POST /api/user/agents/{agentId}/sessions/{sessionId}/messages.", openapi.status = "201", openapi.security = "cloudflareAccess", openapi.path.agentId = "Agent identifier.")

  MailboxListResponse ListAgentSessionMessages(1: ListAgentSessionMessagesRequest request)
    (api.get = "/api/user/agents/:agentId/sessions/:sessionId/messages", openapi.tag = "user", openapi.summary = "List agent session messages", openapi.description = "Lists mailbox messages for a specific session under an agent visible to the current user.", openapi.security = "cloudflareAccess", openapi.path.agentId = "Agent identifier.", openapi.path.sessionId = "Session identifier.")

  MailboxMessageResponse CreateSessionMessage(1: CreateSessionMessageRequest request)
    (api.post = "/api/user/agents/:agentId/sessions/:sessionId/messages", openapi.tag = "user", openapi.summary = "Create session message", openapi.description = "Queues a mailbox message for a specific session under an agent visible to the current user.", openapi.status = "201", openapi.security = "cloudflareAccess", openapi.path.agentId = "Agent identifier.", openapi.path.sessionId = "Session identifier.")

  UserAPIKeyListResponse ListUserAPIKeys(1: EmptyRequest request)
    (api.get = "/api/user/api-keys", openapi.tag = "user", openapi.summary = "List platform API keys", openapi.description = "Lists API keys owned by the current user.", openapi.security = "cloudflareAccess")

  CreateUserAPIKeyResponse CreateUserAPIKey(1: CreateUserAPIKeyRequest request)
    (api.post = "/api/user/api-keys", openapi.tag = "user", openapi.summary = "Create platform API key", openapi.description = "Creates a user platform API key. The plain key is returned once.", openapi.status = "201", openapi.security = "cloudflareAccess")

  OKResponse RevokeUserAPIKey(1: RevokeUserAPIKeyRequest request)
    (api.delete = "/api/user/api-keys/:keyId", openapi.tag = "user", openapi.summary = "Revoke platform API key", openapi.description = "Revokes a user platform API key.", openapi.security = "cloudflareAccess", openapi.path.keyId = "API key identifier.")

  CreateRegistrationTokenResponse CreateAgentRegistrationToken(1: CreateRegistrationTokenRequest request)
    (api.post = "/api/user/agent-registration-tokens", openapi.tag = "user", openapi.summary = "Create agent registration token", openapi.description = "Mints an owner-bound one-time agent registration token.", openapi.status = "201", openapi.security = "cloudflareAccess")
}
