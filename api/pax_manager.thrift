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
  9: optional string description
  10: optional JSON user_metadata
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
  10: optional string description
  11: optional JSON card
}

struct SessionStatusInput {
  1: optional string session_id
  2: optional string agent_type
  3: optional string native_id
  4: optional string name
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
  17: optional string last_user_message_at
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

struct AgentStatusReportRequest {
  1: optional string agent_id
  2: optional string hostname
  3: optional string timestamp
  4: optional list<SessionStatusInput> sessions
  5: optional JSON system
}

struct PullMailboxRequest {
  1: optional i64 offset
  2: optional i32 limit
}

struct PullSessionMailboxRequest {
  1: optional string session_id
  2: optional i64 offset
  3: optional i32 limit
}

struct UpdateMailboxOffsetRequest {
  1: optional i64 offset
}

struct ReportMessageResultRequest {
  1: optional string message_id
  2: optional string status
  3: optional string result
  4: optional string error
  5: optional string completed_at
}

struct GetAgentRequest {
  1: optional string agent_id
}

struct ListAgentSessionsRequest {
  1: optional string agent_id
}

struct GetAgentSessionRequest {
  1: optional string agent_id
  2: optional string session_id
}

struct ListAgentMessagesRequest {
  1: optional string agent_id
  2: optional string session_id
  3: optional string status
  4: optional i32 limit
}

struct CreateAgentMessageRequest {
  1: optional string agent_id
  2: optional string session_id
  3: optional string message
  4: optional string message_type
  5: optional JSON payload
}

struct ListAgentSessionMessagesRequest {
  1: optional string agent_id
  2: optional string session_id
}

struct CreateSessionMessageRequest {
  1: optional string agent_id
  2: optional string session_id
  3: optional string message
  4: optional string message_type
  5: optional JSON payload
}

struct CreateRegistrationTokenRequest {
  1: optional string owner_user_id
  2: optional string owner_email
  3: optional i64 expires_in_seconds
}

struct NodeAgentSessionReportRequest {
  1: optional list<SessionStatusInput> sessions
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
  15: optional string description
  16: optional JSON user_metadata
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
  12: optional string description
  13: optional JSON card
  14: optional JSON user_metadata
}

struct AgentSession {
  1: optional i64 id
  2: optional string node_id
  3: optional string agent_id
  4: optional string session_id
  5: optional string name
  6: optional string agent_type
  8: optional string primary_project_id
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
  25: optional SessionPaxConfig pax_config
  26: optional string last_user_message_at
  27: optional string reported_name
  28: optional bool name_is_custom
  29: optional string runtime_status
  30: optional string runtime_turn_instance_id
  31: optional string archived_at
}

struct Project {
  1: optional string project_id
  2: optional string owner_user_id
  3: optional string display_name
  4: optional string parent_project_id
  5: optional string archived_at
  6: optional string created_at
  7: optional string updated_at
}

struct ProjectTarget {
  1: optional string target_id
  2: optional string project_id
  3: optional string agent_id
  4: optional string display_name
  5: optional string cwd
  6: optional bool is_default
  7: optional bool enabled
  8: optional string created_at
  9: optional string updated_at
}

struct SessionPaxConfig {
  1: optional string cwd
  2: optional string approval_mode
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

struct Pagination {
  1: optional i32 page_num
  2: optional i32 page_size
  3: optional i64 total
  4: optional i32 total_pages
}

struct SessionListData {
  1: optional list<AgentSession> sessions
  2: optional Pagination pagination
}

struct ProjectListData {
  1: optional list<Project> projects
}

struct ProjectData {
  1: optional Project project
}

struct ProjectTargetData {
  1: optional ProjectTarget target
}

struct ProjectTargetListData {
  1: optional list<ProjectTarget> targets
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

struct Secret {
  1: optional string secret_id
  2: optional string owner_user_id
  3: optional string name
  4: optional string kind
  5: optional string description
  6: optional JSON metadata
  7: optional string current_version_id
  8: optional i64 current_version
  9: optional string created_at
  10: optional string updated_at
  11: optional string deleted_at
}

struct SecretVersion {
  1: optional string version_id
  2: optional string secret_id
  3: optional i64 version_number
  4: optional string key_id
  5: optional string state
  6: optional string created_at
  7: optional string created_by_user_id
  8: optional string created_by_node_id
  9: optional string created_by_agent_id
  10: optional string idempotency_key
}

struct CreateSecretData {
  1: optional Secret secret
  2: optional SecretVersion version
}

struct SecretListData {
  1: optional list<Secret> secrets
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
  7: optional string description
  8: optional JSON card
  9: optional JSON user_metadata
}

struct UpdateNodeRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string name
  4: optional string description
  5: optional JSON user_metadata
}

struct DeleteNodeRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
}

struct GetNodeAgentRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
}

struct UpdateNodeAgentRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string name
  5: optional string description
  6: optional JSON card
  7: optional JSON user_metadata
}

struct DeleteNodeAgentRequest {
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

struct ListUserSessionsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.query = "node_id")
  3: optional string agent_id (api.query = "agent_id")
  4: optional i32 page_size (api.query = "page_size")
  5: optional i32 page_num (api.query = "page_num")
  6: optional string primary_project_id (api.query = "primary_project_id")
  7: optional bool include_archived (api.query = "include_archived")
}

struct CreateProjectRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string display_name
  3: optional string parent_project_id
}

struct ListProjectsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional bool include_archived (api.query = "include_archived")
}

struct GetProjectRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string project_id (api.path = "project_id")
}

struct UpdateProjectRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string project_id (api.path = "project_id")
  3: optional string display_name
  4: optional string parent_project_id
}

struct ArchiveProjectRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string project_id (api.path = "project_id")
}

struct CreateProjectTargetRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string project_id (api.path = "project_id")
  3: optional string agent_id
  4: optional string display_name
  5: optional string cwd
  6: optional bool is_default
  7: optional bool enabled
}

struct ListProjectTargetsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string project_id (api.path = "project_id")
}

struct GetProjectTargetRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string project_id (api.path = "project_id")
  3: optional string target_id (api.path = "target_id")
}

struct UpdateProjectTargetRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string project_id (api.path = "project_id")
  3: optional string target_id (api.path = "target_id")
  4: optional string agent_id
  5: optional string display_name
  6: optional string cwd
  7: optional bool is_default
  8: optional bool enabled
}

struct CreateNodeAgentSessionRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string session_id
  5: optional string name
  6: optional string agent_type
  7: optional string native_id
  8: optional string primary_project_id
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

struct UpdateNodeAgentSessionRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string agent_id (api.path = "agent_id")
  4: optional string session_id (api.path = "session_id")
  5: optional SessionPaxConfig pax_config
  6: optional string name
  7: optional bool use_reported_name
  8: optional bool archived
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

struct CreateUserSecretRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string name
  3: optional string kind
  4: optional string description
  5: optional JSON metadata
  6: optional string value
}

struct ListUserSecretsRequest {
  1: optional string user_id (api.path = "user_id")
}

struct GetUserSecretRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string secret_id (api.path = "secret_id")
}

struct ResolveNodeSecretRequest {
  1: optional string secret_id
  2: optional string version
  3: optional string agent_id
  4: optional string session_id
}

struct WriteNodeSecretVersionRequest {
  1: optional string secret_id (api.path = "secret_id")
  2: optional string agent_id
  3: optional string session_id
  4: optional string value
  5: optional bool make_current
  6: optional string expected_current_version_id
  7: optional string idempotency_key
  8: optional string reason
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

struct ApprovalOption {
  1: optional string option_id
  2: optional string label
  3: optional string decision
  4: optional string scope
}

struct AgentApproval {
  1: optional string approval_id
  2: optional string owner_user_id
  3: optional string request_node_id
  4: optional string request_agent_id
  5: optional string request_session_id
  6: optional string source_message_id
  34: optional string native_id
  7: optional string grant_node_id
  8: optional string grant_agent_id
  9: optional string grant_session_id
  10: optional string domain
  11: optional string operation
  12: optional string resource_type
  13: optional string resource_ref
  14: optional string title
  15: optional string description
  16: optional string risk_level
  17: optional string action_fingerprint
  18: optional JSON request_body
  19: optional JSON requested_effects
  20: optional list<ApprovalOption> options
  21: optional string status
  22: optional string decision
  23: optional string decision_option
  24: optional string decision_scope
  25: optional JSON grant_body
  26: optional string decided_by_user_id
  27: optional string grant_revoked_at
  28: optional string grant_revoked_by_user_id
  29: optional string grant_revocation_reason
  30: optional string created_at
  31: optional string expires_at
  32: optional string decided_at
  33: optional JSON raw_payload
}

struct ApprovalData {
  1: optional AgentApproval approval
}

struct ApprovalListData {
  1: optional list<AgentApproval> approvals
}

struct ApprovalGrantListData {
  1: optional list<AgentApproval> grants
}

struct AgentAuditEvent {
  1: optional string event_id
  2: optional string owner_user_id
  3: optional string node_id
  4: optional string agent_id
  5: optional string session_id
  6: optional string turn_id
  7: optional string message_id
  8: optional string approval_id
  9: optional string event_type
  10: optional string source_type
  11: optional string source_id
  12: optional string event_key
  13: optional string title
  14: optional string summary
  15: optional string tool_name
  16: optional JSON tool_input
  17: optional string reason
  18: optional string risk_level
  19: optional string approval_status
  20: optional string decision
  21: optional string decision_scope
  22: optional string decided_by_user_id
  23: optional string decided_at
  24: optional string occurred_at
  25: optional JSON raw
}

struct AuditEventListData {
  1: optional list<AgentAuditEvent> events
}

struct ListUserAuditEventsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string q (api.query = "q")
  3: optional string event_type (api.query = "event_type")
  4: optional string agent_id (api.query = "agent_id")
  5: optional string session_id (api.query = "session_id")
  6: optional string approval_id (api.query = "approval_id")
  7: optional string decision (api.query = "decision")
  8: optional i32 limit (api.query = "limit")
}

struct ResolveSecretData {
  1: optional string status
  2: optional string approval_id
  3: optional string secret_id
  4: optional string version_id
  5: optional i64 version_number
  6: optional string value
  7: optional AgentApproval approval
}

struct WriteSecretVersionData {
  1: optional string status
  2: optional string approval_id
  3: optional string secret_id
  4: optional string version_id
  5: optional i64 version_number
  6: optional bool current
  7: optional AgentApproval approval
}

struct CreateNodeAgentApprovalRequest {
  1: optional string agent_id (api.path = "agent_id")
  2: optional string session_id
  3: optional string source_message_id
  17: optional string native_id
  4: optional string domain
  5: optional string operation
  6: optional string resource_type
  7: optional string resource_ref
  8: optional string title
  9: optional string description
  10: optional string risk_level
  11: optional string action_fingerprint
  12: optional JSON request_body
  13: optional JSON requested_effects
  14: optional list<ApprovalOption> options
  15: optional string expires_at
  16: optional JSON raw_payload
}

struct GetNodeAgentApprovalRequest {
  1: optional string agent_id (api.path = "agent_id")
  2: optional string approval_id (api.path = "approval_id")
}

struct ListUserApprovalsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string status (api.query = "status")
  3: optional string decision (api.query = "decision")
  4: optional string domain (api.query = "domain")
  5: optional string operation (api.query = "operation")
  6: optional string resource_type (api.query = "resource_type")
  7: optional string resource_ref (api.query = "resource_ref")
  8: optional string request_node_id (api.query = "request_node_id")
  9: optional string request_agent_id (api.query = "request_agent_id")
  10: optional string request_session_id (api.query = "request_session_id")
  11: optional string decision_scope (api.query = "decision_scope")
  12: optional bool include_revoked (api.query = "include_revoked")
  13: optional i32 limit (api.query = "limit")
}

struct GetUserApprovalRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string approval_id (api.path = "approval_id")
}

struct DecideUserApprovalRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string approval_id (api.path = "approval_id")
  3: optional string decision_option
  4: optional string reason
  5: optional string grant_node_id
  6: optional string grant_agent_id
  7: optional string grant_session_id
  8: optional JSON grant_body
}

struct ListUserApprovalGrantsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string domain (api.query = "domain")
  3: optional string operation (api.query = "operation")
  4: optional string resource_type (api.query = "resource_type")
  5: optional string resource_ref (api.query = "resource_ref")
  6: optional string decision_scope (api.query = "decision_scope")
  7: optional string grant_node_id (api.query = "grant_node_id")
  8: optional string grant_agent_id (api.query = "grant_agent_id")
  9: optional string grant_session_id (api.query = "grant_session_id")
  10: optional bool active_only (api.query = "active_only")
  11: optional i32 limit (api.query = "limit")
}

struct RevokeUserApprovalGrantRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string grant_id (api.path = "grant_id")
  3: optional string reason
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

struct ProjectResponse {
  1: optional ProjectData data
  2: optional i32 code
  3: optional string message
}

struct ProjectListResponse {
  1: optional ProjectListData data
  2: optional i32 code
  3: optional string message
}

struct ProjectTargetResponse {
  1: optional ProjectTargetData data
  2: optional i32 code
  3: optional string message
}

struct ProjectTargetListResponse {
  1: optional ProjectTargetListData data
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

struct CreateSecretResponse {
  1: optional CreateSecretData data
  2: optional i32 code
  3: optional string message
}

struct SecretListResponse {
  1: optional SecretListData data
  2: optional i32 code
  3: optional string message
}

struct SecretResponse {
  1: optional Secret data
  2: optional i32 code
  3: optional string message
}

struct ResolveSecretResponse {
  1: optional ResolveSecretData data
  2: optional i32 code
  3: optional string message
}

struct WriteSecretVersionResponse {
  1: optional WriteSecretVersionData data
  2: optional i32 code
  3: optional string message
}

struct ApprovalResponse {
  1: optional ApprovalData data
  2: optional i32 code
  3: optional string message
}

struct ApprovalListResponse {
  1: optional ApprovalListData data
  2: optional i32 code
  3: optional string message
}

struct ApprovalGrantListResponse {
  1: optional ApprovalGrantListData data
  2: optional i32 code
  3: optional string message
}

struct AuditEventListResponse {
  1: optional AuditEventListData data
  2: optional i32 code
  3: optional string message
}

struct GetNodeDaemonStatusRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
}

struct ListNodeDaemonHarnessesRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional bool include_missing (api.query = "include_missing")
}

struct DiscoverNodeDaemonHarnessesRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional bool probe
  4: optional list<string> names
}

struct ListNodeDaemonAgentConnectionsRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional bool include_disabled (api.query = "include_disabled")
}

struct NodeDaemonQueryResponse {
  1: optional JSON data
  2: optional i32 code
  3: optional string message
}

struct CreateNodeDaemonAgentConnectionRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string command_id
  4: optional string name
  5: optional string agent_type
  6: optional string harness
  7: optional string instance_id
  8: optional list<string> command
  9: optional string working_dir
  10: optional i32 desired_slots
}

struct CreateNodeDaemonAgentConnectionData {
  1: optional Agent agent
  2: optional string agent_id
  3: optional MailboxMessage bootstrap_message
  4: optional string remote_id
  5: optional string connection_id
  6: optional string command_id
  7: optional string command_status
  8: optional i64 desired_generation
  9: optional string dispatch_status
  10: optional string dispatch_error
  11: optional JSON command_ack
}

struct CreateNodeDaemonAgentConnectionResponse {
  1: optional CreateNodeDaemonAgentConnectionData data
  2: optional i32 code
  3: optional string message
}

struct UpdateNodeDaemonAgentConnectionRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string connection_id (api.path = "connection_id")
  4: optional string command_id
  5: optional string name
  6: optional string harness
  7: optional list<string> command
  8: optional string working_dir
  9: optional i32 desired_slots
  10: optional string desired_state
}

struct NodeDaemonCommandData {
  1: optional string remote_id
  2: optional string connection_id
  3: optional string command_id
  4: optional string command_status
  5: optional i64 desired_generation
  6: optional string dispatch_status
  7: optional string dispatch_error
  8: optional JSON command_ack
}

struct NodeDaemonCommandResponse {
  1: optional NodeDaemonCommandData data
  2: optional i32 code
  3: optional string message
}

struct NodeDaemonAgentConnectionActionRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string connection_id (api.path = "connection_id")
  4: optional string command_id
}

struct GetNodeDaemonCommandRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string command_id (api.path = "command_id")
}

struct RestartNodeDaemonRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string command_id
  4: optional string mode
  5: optional i32 shutdown_grace_seconds
  6: optional string reason
  7: optional i32 idle_grace_seconds
  8: optional i32 drain_timeout_seconds
  9: optional bool force_at_deadline
}

struct UpgradeNodeDaemonRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string command_id
  4: optional string version
  5: optional string tag
  6: optional string mode
  7: optional i32 shutdown_grace_seconds
  8: optional i32 idle_grace_seconds
  9: optional i32 drain_timeout_seconds
  10: optional bool force_at_deadline
  11: optional string reason
}

struct CancelNodeDaemonMaintenanceRequest {
  1: optional string user_id (api.path = "user_id")
  2: optional string node_id (api.path = "node_id")
  3: optional string maintenance_command_id (api.path = "maintenance_command_id")
  4: optional string command_id
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

  OKResponse ReportNodeAgentSessions(
    1: optional NodeAgentSessionReportRequest request
  ) (
    api.post = "/api/v1/node/agents/:agent_id/sessions",
    openapi.tag = "node",
    openapi.summary = "Report node agent sessions",
    openapi.description = "Upserts session status for one agent owned by the authenticated node without changing node or agent liveness fields.",
    openapi.security = "nodeBearer",
    openapi.path.agent_id = "Agent identifier."
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

  ResolveSecretResponse ResolveNodeSecret(
    1: optional ResolveNodeSecretRequest request
  ) (
    api.post = "/api/v1/node/secrets/resolve",
    openapi.tag = "node",
    openapi.summary = "Resolve secret value",
    openapi.description = "Returns a secret value to an authenticated node only after an approval grant exists.",
    openapi.status = "200",
    openapi.security = "nodeBearer"
  )

  WriteSecretVersionResponse WriteNodeSecretVersion(
    1: optional WriteNodeSecretVersionRequest request
  ) (
    api.post = "/api/v1/node/secrets/:secret_id/versions",
    openapi.tag = "node",
    openapi.summary = "Write secret version",
    openapi.description = "Creates a new encrypted secret version and optionally promotes it with an optimistic lock.",
    openapi.status = "200",
    openapi.security = "nodeBearer",
    openapi.path.secret_id = "Secret identifier."
  )

  ApprovalResponse CreateNodeAgentApproval(
    1: optional CreateNodeAgentApprovalRequest request
  ) (
    api.post = "/api/v1/node/agents/:agent_id/approvals",
    openapi.tag = "node",
    openapi.summary = "Create node agent approval",
    openapi.description = "Creates a normalized approval request for an agent action.",
    openapi.status = "200",
    openapi.security = "nodeBearer",
    openapi.path.agent_id = "Agent identifier."
  )

  ApprovalResponse GetNodeAgentApproval(
    1: optional GetNodeAgentApprovalRequest request
  ) (
    api.get = "/api/v1/node/agents/:agent_id/approvals/:approval_id",
    openapi.tag = "node",
    openapi.summary = "Get node agent approval",
    openapi.description = "Returns an approval request created by an agent on the authenticated node.",
    openapi.security = "nodeBearer",
    openapi.path.agent_id = "Agent identifier.",
    openapi.path.approval_id = "Approval identifier."
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

  ProjectResponse CreateProject(1: optional CreateProjectRequest request) (
    api.post = "/api/v1/user/:user_id/projects",
    openapi.tag = "project",
    openapi.summary = "Create project",
    openapi.description = "Creates a user-owned logical project, optionally nested under another active project.",
    openapi.status = "201",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  ProjectListResponse ListProjects(1: optional ListProjectsRequest request) (
    api.get = "/api/v1/user/:user_id/projects",
    openapi.tag = "project",
    openapi.summary = "List projects",
    openapi.description = "Lists the current user's logical projects. Archived projects are excluded by default.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.query.include_archived = "Include archived projects."
  )

  ProjectResponse GetProject(1: optional GetProjectRequest request) (
    api.get = "/api/v1/user/:user_id/projects/:project_id",
    openapi.tag = "project",
    openapi.summary = "Get project",
    openapi.description = "Returns one logical project owned by the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.project_id = "Project identifier."
  )

  ProjectResponse UpdateProject(1: optional UpdateProjectRequest request) (
    api.patch = "/api/v1/user/:user_id/projects/:project_id",
    openapi.tag = "project",
    openapi.summary = "Update project",
    openapi.description = "Renames or moves an active logical project. An empty parent_project_id moves it to the root.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.project_id = "Project identifier."
  )

  ProjectResponse ArchiveProject(1: optional ArchiveProjectRequest request) (
    api.post = "/api/v1/user/:user_id/projects/:project_id/archive",
    openapi.tag = "project",
    openapi.summary = "Archive project",
    openapi.description = "Archives a logical project without cascading to its children.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.project_id = "Project identifier."
  )

  ProjectTargetResponse CreateProjectTarget(
    1: optional CreateProjectTargetRequest request
  ) (
    api.post = "/api/v1/user/:user_id/projects/:project_id/targets",
    openapi.tag = "project",
    openapi.summary = "Create project target",
    openapi.description = "Adds an explicit Agent and configured cwd template to a logical project.",
    openapi.status = "201",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.project_id = "Project identifier."
  )

  ProjectTargetListResponse ListProjectTargets(
    1: optional ListProjectTargetsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/projects/:project_id/targets",
    openapi.tag = "project",
    openapi.summary = "List project targets",
    openapi.description = "Lists every explicitly configured target for a project, including disabled targets.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.project_id = "Project identifier."
  )

  ProjectTargetResponse GetProjectTarget(
    1: optional GetProjectTargetRequest request
  ) (
    api.get = "/api/v1/user/:user_id/projects/:project_id/targets/:target_id",
    openapi.tag = "project",
    openapi.summary = "Get project target",
    openapi.description = "Returns one explicitly configured project target.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.project_id = "Project identifier.",
    openapi.path.target_id = "Project target identifier."
  )

  ProjectTargetResponse UpdateProjectTarget(
    1: optional UpdateProjectTargetRequest request
  ) (
    api.patch = "/api/v1/user/:user_id/projects/:project_id/targets/:target_id",
    openapi.tag = "project",
    openapi.summary = "Update project target",
    openapi.description = "Edits, enables, disables, or makes a project target the sole enabled default.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.project_id = "Project identifier.",
    openapi.path.target_id = "Project target identifier."
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

  NodeDaemonQueryResponse GetNodeDaemonStatus(
    1: optional GetNodeDaemonStatusRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/daemon/status",
    openapi.tag = "user",
    openapi.summary = "Get node daemon status",
    openapi.description = "Forwards a status.get query to the connected paxd control tunnel.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  NodeDaemonQueryResponse ListNodeDaemonHarnesses(
    1: optional ListNodeDaemonHarnessesRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/daemon/harnesses",
    openapi.tag = "user",
    openapi.summary = "List node daemon harnesses",
    openapi.description = "Forwards a harnesses.list query to the connected paxd control tunnel.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  NodeDaemonQueryResponse DiscoverNodeDaemonHarnesses(
    1: optional DiscoverNodeDaemonHarnessesRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/daemon/harnesses/discover",
    openapi.tag = "user",
    openapi.summary = "Discover node daemon harnesses",
    openapi.description = "Forwards a harnesses.discover query to the connected paxd control tunnel.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  NodeDaemonQueryResponse ListNodeDaemonAgentConnections(
    1: optional ListNodeDaemonAgentConnectionsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/daemon/agent-connections",
    openapi.tag = "user",
    openapi.summary = "List node daemon agent connections",
    openapi.description = "Forwards an agent_connections.list query to the connected paxd control tunnel.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  NodeDaemonCommandResponse RestartNodeDaemon(
    1: optional RestartNodeDaemonRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/daemon/restart",
    openapi.tag = "user",
    openapi.summary = "Restart node daemon",
    openapi.description = "Forwards paxd.restart to the connected paxd control tunnel.",
    openapi.status = "202",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  NodeDaemonCommandResponse UpgradeNodeDaemon(
    1: optional UpgradeNodeDaemonRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/daemon/upgrade",
    openapi.tag = "user",
    openapi.summary = "Upgrade node daemon",
    openapi.description = "Forwards paxd.upgrade to the connected paxd control tunnel.",
    openapi.status = "202",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  NodeDaemonCommandResponse CancelNodeDaemonMaintenance(
    1: optional CancelNodeDaemonMaintenanceRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/daemon/maintenance/:maintenance_command_id/cancel",
    openapi.tag = "user",
    openapi.summary = "Cancel node daemon maintenance",
    openapi.description = "Cancels a restart or upgrade before binary activation or shutdown is committed.",
    openapi.status = "202",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.maintenance_command_id = "Maintenance command identifier."
  )

  CreateNodeDaemonAgentConnectionResponse CreateNodeDaemonAgentConnection(
    1: optional CreateNodeDaemonAgentConnectionRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/daemon/agent-connections",
    openapi.tag = "user",
    openapi.summary = "Create node daemon agent connection",
    openapi.description = "Creates a cloud Agent and forwards agent_connection.create to the connected paxd control tunnel.",
    openapi.status = "202",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  NodeDaemonCommandResponse UpdateNodeDaemonAgentConnection(
    1: optional UpdateNodeDaemonAgentConnectionRequest request
  ) (
    api.patch = "/api/v1/user/:user_id/nodes/:node_id/daemon/agent-connections/:connection_id",
    openapi.tag = "user",
    openapi.summary = "Update node daemon agent connection",
    openapi.description = "Forwards agent_connection.update to the connected paxd control tunnel.",
    openapi.status = "202",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.connection_id = "Agent connection identifier."
  )

  NodeDaemonCommandResponse StopNodeDaemonAgentConnection(
    1: optional NodeDaemonAgentConnectionActionRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/daemon/agent-connections/:connection_id/stop",
    openapi.tag = "user",
    openapi.summary = "Stop node daemon agent connection",
    openapi.description = "Sets the paxd agent connection desired state to stopped.",
    openapi.status = "202",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.connection_id = "Agent connection identifier."
  )

  NodeDaemonCommandResponse RestartNodeDaemonAgentConnection(
    1: optional NodeDaemonAgentConnectionActionRequest request
  ) (
    api.post = "/api/v1/user/:user_id/nodes/:node_id/daemon/agent-connections/:connection_id/restart",
    openapi.tag = "user",
    openapi.summary = "Restart node daemon agent connection",
    openapi.description = "Forwards agent_connection.restart to the connected paxd control tunnel.",
    openapi.status = "202",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.connection_id = "Agent connection identifier."
  )

  NodeDaemonCommandResponse RemoveNodeDaemonAgentConnection(
    1: optional NodeDaemonAgentConnectionActionRequest request
  ) (
    api.delete = "/api/v1/user/:user_id/nodes/:node_id/daemon/agent-connections/:connection_id",
    openapi.tag = "user",
    openapi.summary = "Remove node daemon agent connection",
    openapi.description = "Forwards agent_connection.delete to the connected paxd control tunnel without deleting the cloud Agent.",
    openapi.status = "202",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.connection_id = "Agent connection identifier."
  )

  NodeDaemonQueryResponse GetNodeDaemonCommand(
    1: optional GetNodeDaemonCommandRequest request
  ) (
    api.get = "/api/v1/user/:user_id/nodes/:node_id/daemon/commands/:command_id",
    openapi.tag = "user",
    openapi.summary = "Get node daemon command",
    openapi.description = "Forwards command.get to the connected paxd control tunnel.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.command_id = "Command identifier."
  )

  NodeResponse UpdateNode(1: optional UpdateNodeRequest request) (
    api.patch = "/api/v1/user/:user_id/nodes/:node_id",
    openapi.tag = "user",
    openapi.summary = "Update node profile",
    openapi.description = "Updates user-maintained node name, description, and metadata.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier."
  )

  NodeResponse DeleteNode(1: optional DeleteNodeRequest request) (
    api.delete = "/api/v1/user/:user_id/nodes/:node_id",
    openapi.tag = "user",
    openapi.summary = "Delete node",
    openapi.description = "Soft-deletes a node owned by the current user, hides it from fleet lists, revokes its API key, and soft-deletes hosted agents.",
    openapi.status = "200",
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

  AgentResponse UpdateNodeAgent(1: optional UpdateNodeAgentRequest request) (
    api.patch = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id",
    openapi.tag = "user",
    openapi.summary = "Update node agent profile",
    openapi.description = "Updates user-maintained agent name, description, card, and metadata.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.node_id = "Node identifier.",
    openapi.path.agent_id = "Agent identifier."
  )

  AgentResponse DeleteNodeAgent(1: optional DeleteNodeAgentRequest request) (
    api.delete = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id",
    openapi.tag = "user",
    openapi.summary = "Delete node agent",
    openapi.description = "Soft-deletes an agent owned by the current user, hides it from fleet lists, and revokes its API key.",
    openapi.status = "200",
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

  SessionListResponse ListUserSessions(
    1: optional ListUserSessionsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/sessions",
    openapi.tag = "user",
    openapi.summary = "List user sessions",
    openapi.description = "Lists sessions across agents owned by the current user with pagination.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier or self.",
    openapi.query.node_id = "Optional node identifier filter. Use comma-separated IDs for multiple nodes.",
    openapi.query.agent_id = "Optional agent identifier filter. Use comma-separated IDs for multiple agents.",
    openapi.query.primary_project_id = "Optional primary logical project identifier filter.",
    openapi.query.include_archived = "Whether archived sessions should be included. Defaults to false.",
    openapi.query.page_size = "Maximum number of sessions per page.",
    openapi.query.page_num = "One-based page number."
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

  AgentSessionResponse UpdateNodeAgentSession(
    1: optional UpdateNodeAgentSessionRequest request
  ) (
    api.patch = "/api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/sessions/:session_id",
    openapi.tag = "user",
    openapi.summary = "Update node agent session",
    openapi.description = "Updates mutable session configuration such as the PAX approval mode. The session cwd is create-only and cannot be changed.",
    openapi.status = "200",
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

  SecretListResponse ListUserSecrets(
    1: optional ListUserSecretsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/secrets",
    openapi.tag = "user",
    openapi.summary = "List secrets",
    openapi.description = "Lists secret metadata visible to the current user without returning plaintext values.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  CreateSecretResponse CreateUserSecret(
    1: optional CreateUserSecretRequest request
  ) (
    api.post = "/api/v1/user/:user_id/secrets",
    openapi.tag = "user",
    openapi.summary = "Create secret",
    openapi.description = "Creates an encrypted secret with its first version. The plaintext value is not returned.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  SecretResponse GetUserSecret(
    1: optional GetUserSecretRequest request
  ) (
    api.get = "/api/v1/user/:user_id/secrets/:secret_id",
    openapi.tag = "user",
    openapi.summary = "Get secret",
    openapi.description = "Returns secret metadata without returning plaintext values.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.secret_id = "Secret identifier."
  )

  ApprovalListResponse ListUserApprovals(
    1: optional ListUserApprovalsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/approvals",
    openapi.tag = "user",
    openapi.summary = "List approvals",
    openapi.description = "Lists normalized approval requests visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  AuditEventListResponse ListUserAuditEvents(
    1: optional ListUserAuditEventsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/audit-events",
    openapi.tag = "user",
    openapi.summary = "List agent audit events",
    openapi.description = "Lists normalized agent and session audit events visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.query.q = "Keyword search over title, summary, tool name, reason, raw event, and tool input.",
    openapi.query.event_type = "Filter by audit event type.",
    openapi.query.agent_id = "Filter by agent identifier.",
    openapi.query.session_id = "Filter by session identifier.",
    openapi.query.approval_id = "Filter by approval identifier.",
    openapi.query.decision = "Filter by approval decision.",
    openapi.query.limit = "Maximum number of events to return."
  )

  ApprovalResponse GetUserApproval(
    1: optional GetUserApprovalRequest request
  ) (
    api.get = "/api/v1/user/:user_id/approvals/:approval_id",
    openapi.tag = "user",
    openapi.summary = "Get approval",
    openapi.description = "Returns a normalized approval request visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.approval_id = "Approval identifier."
  )

  ApprovalResponse DecideUserApproval(
    1: optional DecideUserApprovalRequest request
  ) (
    api.post = "/api/v1/user/:user_id/approvals/:approval_id/decision",
    openapi.tag = "user",
    openapi.summary = "Decide approval",
    openapi.description = "Records a user decision for a pending approval request.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.approval_id = "Approval identifier."
  )

  ApprovalGrantListResponse ListUserApprovalGrants(
    1: optional ListUserApprovalGrantsRequest request
  ) (
    api.get = "/api/v1/user/:user_id/approval-grants",
    openapi.tag = "user",
    openapi.summary = "List approval grants",
    openapi.description = "Lists reusable approval grants visible to the current user.",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier."
  )

  ApprovalResponse RevokeUserApprovalGrant(
    1: optional RevokeUserApprovalGrantRequest request
  ) (
    api.post = "/api/v1/user/:user_id/approval-grants/:grant_id/revoke",
    openapi.tag = "user",
    openapi.summary = "Revoke approval grant",
    openapi.description = "Revokes a reusable approval grant without deleting the original approval record.",
    openapi.status = "200",
    openapi.security = "cloudflareAccess",
    openapi.path.user_id = "User identifier.",
    openapi.path.grant_id = "Approval grant identifier."
  )
}
