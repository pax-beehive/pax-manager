package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrConflict     = errors.New("conflict")
)

type User struct {
	UserID      string     `json:"user_id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

type UserPrincipal struct {
	User    User
	IsAdmin bool
}

type Agent struct {
	AgentID       string          `json:"agent_id"`
	NodeID        string          `json:"node_id,omitempty"`
	OwnerUserID   string          `json:"owner_user_id"`
	Name          string          `json:"name,omitempty"`
	Hostname      string          `json:"hostname"`
	AgentType     string          `json:"agent_type"`
	MachineType   string          `json:"machine_type,omitempty"`
	OS            string          `json:"os"`
	HermesVersion string          `json:"hermes_version,omitempty"`
	APIEndpoint   string          `json:"api_endpoint"`
	Status        string          `json:"status"`
	Online        bool            `json:"online"`
	LastHeartbeat *time.Time      `json:"last_heartbeat,omitempty"`
	RegisteredAt  time.Time       `json:"registered_at"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type Node struct {
	NodeID        string          `json:"node_id"`
	OwnerUserID   string          `json:"owner_user_id"`
	Name          string          `json:"name,omitempty"`
	Hostname      string          `json:"hostname"`
	MachineType   string          `json:"machine_type,omitempty"`
	OS            string          `json:"os"`
	Arch          string          `json:"arch,omitempty"`
	PaxdVersion   string          `json:"paxd_version,omitempty"`
	APIEndpoint   string          `json:"api_endpoint"`
	Status        string          `json:"status"`
	Online        bool            `json:"online"`
	LastHeartbeat *time.Time      `json:"last_heartbeat,omitempty"`
	RegisteredAt  time.Time       `json:"registered_at"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type AgentSession struct {
	ID             int64                `json:"id"`
	NodeID         string               `json:"node_id,omitempty"`
	AgentID        string               `json:"agent_id"`
	SessionID      string               `json:"session_id"`
	SessionName    string               `json:"name,omitempty"`
	AgentType      string               `json:"agent_type,omitempty"`
	NativeID       string               `json:"-"`
	ProjectID      string               `json:"project_id,omitempty"`
	Preview        string               `json:"preview,omitempty"`
	WorkspaceRoots []string             `json:"workspace_roots,omitempty"`
	Source         string               `json:"source,omitempty"`
	Status         string               `json:"status"`
	CurrentTask    string               `json:"current_task,omitempty"`
	LastMessageAt  *time.Time           `json:"last_message_at,omitempty"`
	MessageCount   int                  `json:"message_count"`
	TokenInput     int64                `json:"token_input"`
	TokenOutput    int64                `json:"token_output"`
	TokenTotal     int64                `json:"token_total"`
	TokenUsage     TokenUsage           `json:"token_usage"`
	Model          string               `json:"model,omitempty"`
	RunID          string               `json:"run_id,omitempty"`
	RunStatus      string               `json:"run_status,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
	Metadata       json.RawMessage      `json:"metadata,omitempty"`
	RuntimeState   *SessionRuntimeState `json:"runtime_state,omitempty"`
}

type MailboxMessage struct {
	ID              int64           `json:"id"`
	MessageID       string          `json:"message_id"`
	UserID          string          `json:"user_id"`
	OwnerUserID     string          `json:"owner_user_id"`
	NodeID          string          `json:"node_id,omitempty"`
	AgentID         string          `json:"agent_id"`
	SessionID       string          `json:"session_id,omitempty"`
	Message         string          `json:"message"`
	MessageType     string          `json:"message_type"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Status          string          `json:"status"`
	DeliveredAt     *time.Time      `json:"delivered_at,omitempty"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	Result          string          `json:"result,omitempty"`
	Error           string          `json:"error,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	Direction       string          `json:"direction,omitempty"`
	ParentMessageID string          `json:"parent_message_id,omitempty"`
	TurnID          string          `json:"turn_id,omitempty"`
	ResponseID      string          `json:"response_id,omitempty"`
	Events          json.RawMessage `json:"events,omitempty"`
	FileChanges     []FileChange    `json:"file_changes,omitempty"`
	TokenUsage      TokenUsage      `json:"token_usage,omitempty"`
}

const (
	MessageSourceMailbox   = "mailbox"
	MessageSourceACPTunnel = "acp_tunnel"

	MessageDirectionUserToAgent = "user_to_agent"
	MessageDirectionAgentToUser = "agent_to_user"

	MessagePartText     = "text"
	MessagePartRawJSON  = "raw_json"
	MessagePartArtifact = "artifact"
)

// Message is durable business history. Unlike TransportFrame, it is intended
// for user-visible replay and can aggregate many transport frames into one row.
type Message struct {
	ID              int64           `json:"id"`
	MessageID       string          `json:"message_id"`
	OwnerUserID     string          `json:"owner_user_id,omitempty"`
	NodeID          string          `json:"node_id,omitempty"`
	AgentID         string          `json:"agent_id"`
	SessionID       string          `json:"session_id,omitempty"`
	Source          string          `json:"source"`
	Direction       string          `json:"direction"`
	Role            string          `json:"role,omitempty"`
	Status          string          `json:"status,omitempty"`
	MessageType     string          `json:"message_type,omitempty"`
	ParentMessageID string          `json:"parent_message_id,omitempty"`
	TurnID          string          `json:"turn_id,omitempty"`
	ResponseID      string          `json:"response_id,omitempty"`
	LogicalKey      string          `json:"logical_key,omitempty"`
	RawJSON         json.RawMessage `json:"raw_json,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// MessagePart stores message content or artifacts. Streaming text deltas append
// to an existing text part instead of creating one row per token.
type MessagePart struct {
	ID          int64           `json:"id"`
	MessageID   string          `json:"message_id"`
	PartIndex   int             `json:"part_index"`
	PartType    string          `json:"part_type"`
	Text        string          `json:"text,omitempty"`
	PayloadJSON json.RawMessage `json:"payload_json,omitempty"`
	ArtifactURI string          `json:"artifact_uri,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type MessageWithParts struct {
	Message
	Parts []MessagePart `json:"parts"`
}

const (
	TransportStreamManagerToPaxd = "manager_to_paxd"
	TransportStreamPaxdToManager = "paxd_to_manager"

	TransportDirectionInbound  = "inbound"
	TransportDirectionOutbound = "outbound"

	TransportStatusPending  = "pending"
	TransportStatusSent     = "sent"
	TransportStatusAcked    = "acked"
	TransportStatusReceived = "received"
	TransportStatusApplied  = "applied"
	TransportStatusFailed   = "failed"
)

// TransportFrame is one durable frame in the manager<->paxd reliable transport
// journal. PayloadJSON is the raw ACP JSON-RPC payload, not the tunnel envelope.
type TransportFrame struct {
	ID             int64           `json:"id"`
	AgentID        string          `json:"agent_id"`
	Stream         string          `json:"stream"`
	Seq            int64           `json:"seq"`
	LocalDirection string          `json:"local_direction"`
	PayloadJSON    json.RawMessage `json:"payload_json"`
	Status         string          `json:"status"`
	Error          string          `json:"error,omitempty"`
	RetryCount     int             `json:"retry_count"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	SentAt         *time.Time      `json:"sent_at,omitempty"`
	ReceivedAt     *time.Time      `json:"received_at,omitempty"`
	AckedAt        *time.Time      `json:"acked_at,omitempty"`
	AppliedAt      *time.Time      `json:"applied_at,omitempty"`
}

type ApprovalOption struct {
	OptionID string `json:"option_id"`
	Label    string `json:"label"`
	Decision string `json:"decision"`
	Scope    string `json:"scope"`
}

type AgentApproval struct {
	ApprovalID            string           `json:"approval_id"`
	OwnerUserID           string           `json:"owner_user_id"`
	RequestNodeID         string           `json:"request_node_id,omitempty"`
	RequestAgentID        string           `json:"request_agent_id,omitempty"`
	RequestSessionID      string           `json:"request_session_id,omitempty"`
	SourceMessageID       string           `json:"source_message_id,omitempty"`
	GrantNodeID           string           `json:"grant_node_id,omitempty"`
	GrantAgentID          string           `json:"grant_agent_id,omitempty"`
	GrantSessionID        string           `json:"grant_session_id,omitempty"`
	Domain                string           `json:"domain"`
	Operation             string           `json:"operation"`
	ResourceType          string           `json:"resource_type"`
	ResourceRef           string           `json:"resource_ref,omitempty"`
	Title                 string           `json:"title,omitempty"`
	Description           string           `json:"description,omitempty"`
	RiskLevel             string           `json:"risk_level,omitempty"`
	ActionFingerprint     string           `json:"action_fingerprint"`
	RequestBody           json.RawMessage  `json:"request_body,omitempty"`
	RequestedEffects      json.RawMessage  `json:"requested_effects,omitempty"`
	Options               []ApprovalOption `json:"options,omitempty"`
	Status                string           `json:"status"`
	Decision              string           `json:"decision,omitempty"`
	DecisionOption        string           `json:"decision_option,omitempty"`
	DecisionScope         string           `json:"decision_scope,omitempty"`
	GrantBody             json.RawMessage  `json:"grant_body,omitempty"`
	DecidedByUserID       string           `json:"decided_by_user_id,omitempty"`
	GrantRevokedAt        *time.Time       `json:"grant_revoked_at,omitempty"`
	GrantRevokedByUserID  string           `json:"grant_revoked_by_user_id,omitempty"`
	GrantRevocationReason string           `json:"grant_revocation_reason,omitempty"`
	CreatedAt             time.Time        `json:"created_at"`
	ExpiresAt             *time.Time       `json:"expires_at,omitempty"`
	DecidedAt             *time.Time       `json:"decided_at,omitempty"`
	RawPayload            json.RawMessage  `json:"raw_payload,omitempty"`
}

type FileChange struct {
	Path       string `json:"path"`
	Tool       string `json:"tool"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

type RegisterNodeRequest struct {
	Name        string          `json:"name"`
	Hostname    string          `json:"hostname"`
	MachineType string          `json:"machine_type"`
	OS          string          `json:"os"`
	Arch        string          `json:"arch"`
	PaxdVersion string          `json:"paxd_version"`
	APIEndpoint string          `json:"api_endpoint"`
	Metadata    json.RawMessage `json:"metadata"`
}

type RegisterNodeResponse struct {
	NodeID string `json:"node_id"`
	APIKey string `json:"api_key"`
}

type RegisterNodeAgentRequest struct {
	Node  RegisterNodeRequest `json:"node"`
	Agent CreateAgentRequest  `json:"agent"`
}

type RegisterNodeAgentResponse struct {
	NodeID  string `json:"node_id"`
	APIKey  string `json:"api_key,omitempty"`
	AgentID string `json:"agent_id"`
	Agent   Agent  `json:"agent"`
}

type RegisterAgentRequest struct {
	Name          string          `json:"name"`
	AgentType     string          `json:"agent_type"`
	Hostname      string          `json:"hostname"`
	MachineType   string          `json:"machine_type"`
	OS            string          `json:"os"`
	HermesVersion string          `json:"hermes_version"`
	APIEndpoint   string          `json:"api_endpoint"`
	Projects      []Project       `json:"projects,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
}

type RegisterAgentResponse struct {
	AgentID string `json:"agent_id"`
	APIKey  string `json:"api_key"`
}

type CreateRegistrationTokenRequest struct {
	OwnerUserID      string `json:"owner_user_id"`
	OwnerEmail       string `json:"owner_email"`
	ExpiresInSeconds int64  `json:"expires_in_seconds"`
}

type CreateRegistrationTokenResponse struct {
	Token       string     `json:"token"`
	OwnerUserID string     `json:"owner_user_id"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

type UserAPIKey struct {
	KeyID       string     `json:"key_id"`
	OwnerUserID string     `json:"owner_user_id"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type CreateUserAPIKeyRequest struct {
	Name string `json:"name"`
}

type Secret struct {
	SecretID         string          `json:"secret_id"`
	OwnerUserID      string          `json:"owner_user_id"`
	Name             string          `json:"name"`
	Kind             string          `json:"kind,omitempty"`
	Description      string          `json:"description,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CurrentVersionID string          `json:"current_version_id,omitempty"`
	CurrentVersion   int64           `json:"current_version,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
}

type SecretVersion struct {
	VersionID        string    `json:"version_id"`
	SecretID         string    `json:"secret_id"`
	VersionNumber    int64     `json:"version_number"`
	Ciphertext       []byte    `json:"-"`
	Nonce            []byte    `json:"-"`
	KeyID            string    `json:"key_id"`
	State            string    `json:"state"`
	CreatedAt        time.Time `json:"created_at"`
	CreatedByUserID  string    `json:"created_by_user_id,omitempty"`
	CreatedByNodeID  string    `json:"created_by_node_id,omitempty"`
	CreatedByAgentID string    `json:"created_by_agent_id,omitempty"`
	IdempotencyKey   string    `json:"idempotency_key,omitempty"`
}

type CreateSecretRequest struct {
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata"`
	Value       string          `json:"value"`
}

type UpdateSecretValueRequest struct {
	Value                    string `json:"value"`
	MakeCurrent              bool   `json:"make_current"`
	ExpectedCurrentVersionID string `json:"expected_current_version_id"`
	IdempotencyKey           string `json:"idempotency_key"`
	Reason                   string `json:"reason"`
}

type ResolveSecretRequest struct {
	SecretID  string `json:"secret_id"`
	Version   string `json:"version"`
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
}

type ResolveSecretResponse struct {
	Status        string        `json:"status"`
	ApprovalID    string        `json:"approval_id,omitempty"`
	SecretID      string        `json:"secret_id,omitempty"`
	VersionID     string        `json:"version_id,omitempty"`
	VersionNumber int64         `json:"version_number,omitempty"`
	Value         string        `json:"value,omitempty"`
	Approval      AgentApproval `json:"approval,omitempty"`
}

type WriteSecretVersionRequest struct {
	SecretID                 string `json:"secret_id"`
	AgentID                  string `json:"agent_id"`
	SessionID                string `json:"session_id"`
	Value                    string `json:"value"`
	MakeCurrent              bool   `json:"make_current"`
	ExpectedCurrentVersionID string `json:"expected_current_version_id"`
	IdempotencyKey           string `json:"idempotency_key"`
	Reason                   string `json:"reason"`
}

type WriteSecretVersionResponse struct {
	Status        string        `json:"status"`
	ApprovalID    string        `json:"approval_id,omitempty"`
	SecretID      string        `json:"secret_id,omitempty"`
	VersionID     string        `json:"version_id,omitempty"`
	VersionNumber int64         `json:"version_number,omitempty"`
	Current       bool          `json:"current,omitempty"`
	Approval      AgentApproval `json:"approval,omitempty"`
}

type SecretAccessEvent struct {
	SecretID  string
	VersionID string
	NodeID    string
	AgentID   string
	SessionID string
	Action    string
	Result    string
}

type PaxdArtifact struct {
	ArtifactID  string     `json:"artifact_id"`
	Platform    string     `json:"platform"`
	Tags        []string   `json:"tags"`
	Version     string     `json:"version"`
	BuildID     string     `json:"build_id,omitempty"`
	Bucket      string     `json:"bucket"`
	Object      string     `json:"object"`
	Generation  int64      `json:"generation"`
	SHA256      string     `json:"sha256"`
	SizeBytes   int64      `json:"size_bytes"`
	ContentType string     `json:"content_type,omitempty"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

type CreatePaxdArtifactRequest struct {
	Platform    string   `json:"platform"`
	Tags        []string `json:"tags"`
	Version     string   `json:"version"`
	BuildID     string   `json:"build_id"`
	Bucket      string   `json:"bucket"`
	Object      string   `json:"object"`
	Generation  int64    `json:"generation"`
	SHA256      string   `json:"sha256"`
	SizeBytes   int64    `json:"size_bytes"`
	ContentType string   `json:"content_type"`
}

type FindPaxdArtifactRequest struct {
	Platform string
	Tags     []string
}

type PaxdArtifactDownloadResponse struct {
	URL        string       `json:"url"`
	ExpiresAt  time.Time    `json:"expires_at"`
	Artifact   PaxdArtifact `json:"artifact"`
	SHA256     string       `json:"sha256"`
	SizeBytes  int64        `json:"size_bytes"`
	Version    string       `json:"version"`
	Platform   string       `json:"platform"`
	Tags       []string     `json:"tags"`
	Generation int64        `json:"generation"`
}

type CreateUserAPIKeyResponse struct {
	APIKey UserAPIKey `json:"api_key"`
	Key    string     `json:"key"`
}

type CreateApprovalRequest struct {
	AgentID           string           `json:"agent_id"`
	SessionID         string           `json:"session_id"`
	SourceMessageID   string           `json:"source_message_id"`
	Domain            string           `json:"domain"`
	Operation         string           `json:"operation"`
	ResourceType      string           `json:"resource_type"`
	ResourceRef       string           `json:"resource_ref"`
	Title             string           `json:"title"`
	Description       string           `json:"description"`
	RiskLevel         string           `json:"risk_level"`
	ActionFingerprint string           `json:"action_fingerprint"`
	RequestBody       json.RawMessage  `json:"request_body"`
	RequestedEffects  json.RawMessage  `json:"requested_effects"`
	Options           []ApprovalOption `json:"options"`
	ExpiresAt         *time.Time       `json:"expires_at"`
	RawPayload        json.RawMessage  `json:"raw_payload"`
}

type ApprovalFilter struct {
	Principal        UserPrincipal
	Status           string
	Decision         string
	Domain           string
	Operation        string
	ResourceType     string
	ResourceRef      string
	RequestNodeID    string
	RequestAgentID   string
	RequestSessionID string
	DecisionScope    string
	IncludeRevoked   bool
	Limit            int
}

type ApprovalGrantFilter struct {
	Principal      UserPrincipal
	Domain         string
	Operation      string
	ResourceType   string
	ResourceRef    string
	DecisionScope  string
	GrantNodeID    string
	GrantAgentID   string
	GrantSessionID string
	ActiveOnly     bool
	Limit          int
}

type ApprovalDecisionRequest struct {
	DecisionOption string          `json:"decision_option"`
	Reason         string          `json:"reason"`
	GrantNodeID    string          `json:"grant_node_id"`
	GrantAgentID   string          `json:"grant_agent_id"`
	GrantSessionID string          `json:"grant_session_id"`
	GrantBody      json.RawMessage `json:"grant_body"`
}

type ApprovalGrantLookup struct {
	OwnerUserID       string
	RequestNodeID     string
	RequestAgentID    string
	RequestSessionID  string
	Domain            string
	Operation         string
	ActionFingerprint string
}

type RevokeApprovalGrantRequest struct {
	Reason string `json:"reason"`
}

type AgentStatusReport struct {
	AgentID   string               `json:"agent_id"`
	Hostname  string               `json:"hostname"`
	Timestamp time.Time            `json:"timestamp"`
	Sessions  []SessionStatusInput `json:"sessions"`
	System    json.RawMessage      `json:"system"`
}

type NodeStatusReport struct {
	NodeID    string             `json:"node_id"`
	Hostname  string             `json:"hostname"`
	Timestamp time.Time          `json:"timestamp"`
	Agents    []AgentStatusInput `json:"agents"`
	System    json.RawMessage    `json:"system"`
	Metadata  json.RawMessage    `json:"metadata"`
}

type AgentStatusInput struct {
	AgentID       string               `json:"agent_id"`
	Name          string               `json:"name"`
	AgentType     string               `json:"agent_type"`
	Status        string               `json:"status"`
	Online        bool                 `json:"online"`
	LastHeartbeat *time.Time           `json:"last_heartbeat"`
	Capabilities  json.RawMessage      `json:"capabilities"`
	Metadata      json.RawMessage      `json:"metadata"`
	Sessions      []SessionStatusInput `json:"sessions"`
}

type SessionStatusInput struct {
	SessionID      string     `json:"session_id"`
	AgentType      string     `json:"agent_type"`
	NativeID       string     `json:"native_id"`
	SessionName    string     `json:"name"`
	ProjectID      string     `json:"project_id"`
	Preview        string     `json:"preview"`
	WorkspaceRoots []string   `json:"workspace_roots"`
	Source         string     `json:"source"`
	Status         string     `json:"status"`
	CurrentTask    string     `json:"current_task"`
	LastMessageAt  *time.Time `json:"last_message_at"`
	MessageCount   int        `json:"message_count"`
	TokenUsage     TokenUsage `json:"token_usage"`
	Model          string     `json:"model"`
	RunID          string     `json:"run_id"`
	RunStatus      string     `json:"run_status"`
}

type TokenUsage struct {
	Input            int64   `json:"input_tokens"`
	Output           int64   `json:"output_tokens"`
	Total            int64   `json:"total_tokens"`
	CacheRead        int64   `json:"cache_read_tokens,omitempty"`
	CacheWrite       int64   `json:"cache_write_tokens,omitempty"`
	CacheCreation    int64   `json:"cache_creation_tokens,omitempty"`
	Reasoning        int64   `json:"reasoning_tokens,omitempty"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
	ActualCostUSD    float64 `json:"actual_cost_usd,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
}

func (u *TokenUsage) UnmarshalJSON(data []byte) error {
	var total int64
	if err := json.Unmarshal(data, &total); err == nil {
		u.Total = total
		return nil
	}

	var object struct {
		Input              int64   `json:"input"`
		Output             int64   `json:"output"`
		Total              int64   `json:"total"`
		InputTokens        int64   `json:"input_tokens"`
		OutputTokens       int64   `json:"output_tokens"`
		TotalTokens        int64   `json:"total_tokens"`
		InputTokensCamel   int64   `json:"inputTokens"`
		OutputTokensCamel  int64   `json:"outputTokens"`
		TotalTokensCamel   int64   `json:"totalTokens"`
		CacheRead          int64   `json:"cache_read_tokens"`
		CacheReadCamel     int64   `json:"cacheReadTokens"`
		CacheWrite         int64   `json:"cache_write_tokens"`
		CacheCreation      int64   `json:"cache_creation_tokens"`
		CacheCreationCamel int64   `json:"cacheCreationTokens"`
		Reasoning          int64   `json:"reasoning_tokens"`
		ReasoningCamel     int64   `json:"reasoningTokens"`
		EstimatedCostUSD   float64 `json:"estimated_cost_usd"`
		ActualCostUSD      float64 `json:"actual_cost_usd"`
		CostUSD            float64 `json:"cost_usd"`
		CostUSDCamel       float64 `json:"costUsd"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	u.Input = FirstNonZero(object.InputTokens, object.InputTokensCamel, object.Input)
	u.Output = FirstNonZero(object.OutputTokens, object.OutputTokensCamel, object.Output)
	u.Total = FirstNonZero(object.TotalTokens, object.TotalTokensCamel, object.Total)
	u.CacheRead = FirstNonZero(object.CacheRead, object.CacheReadCamel)
	u.CacheWrite = object.CacheWrite
	u.CacheCreation = FirstNonZero(object.CacheCreation, object.CacheCreationCamel)
	u.Reasoning = FirstNonZero(object.Reasoning, object.ReasoningCamel)
	u.EstimatedCostUSD = object.EstimatedCostUSD
	u.ActualCostUSD = object.ActualCostUSD
	u.CostUSD = firstNonZeroFloat(object.CostUSD, object.CostUSDCamel)
	if u.Total == 0 {
		u.Total = u.Input + u.Output + u.CacheRead + u.CacheWrite + u.CacheCreation + u.Reasoning
	}
	return nil
}

type CreateMailboxRequest struct {
	NodeID      string          `json:"node_id"`
	AgentID     string          `json:"agent_id"`
	SessionID   string          `json:"session_id"`
	Message     string          `json:"message"`
	MessageType string          `json:"message_type"`
	Payload     json.RawMessage `json:"payload"`
}

type MessageResultRequest struct {
	MessageID       string          `json:"message_id"`
	Status          string          `json:"status"`
	Result          string          `json:"result"`
	Error           string          `json:"error"`
	CompletedAt     *time.Time      `json:"completed_at"`
	ResultMessageID string          `json:"result_message_id"`
	Content         string          `json:"content"`
	Payload         json.RawMessage `json:"payload"`
	Events          json.RawMessage `json:"events"`
	FileChanges     []FileChange    `json:"file_changes"`
	TokenUsage      TokenUsage      `json:"token_usage"`
}

type MarkDeliveredRequest struct {
	MessageID   string     `json:"message_id"`
	DeliveredAt *time.Time `json:"delivered_at"`
}

type CreateOutboundMessageRequest struct {
	AgentID         string          `json:"agent_id"`
	SessionID       string          `json:"session_id"`
	MessageType     string          `json:"message_type"`
	Content         string          `json:"content"`
	ParentMessageID string          `json:"parent_message_id"`
	TurnID          string          `json:"turn_id"`
	ResponseID      string          `json:"response_id"`
	Status          string          `json:"status"`
	Payload         json.RawMessage `json:"payload"`
	Events          json.RawMessage `json:"events"`
	FileChanges     []FileChange    `json:"file_changes"`
	TokenUsage      TokenUsage      `json:"token_usage"`
	CreatedAt       *time.Time      `json:"created_at"`
}

type CreateAgentRequest struct {
	UserID       string          `json:"user_id"`
	NodeID       string          `json:"node_id"`
	Name         string          `json:"name"`
	AgentType    string          `json:"agent_type"`
	Capabilities json.RawMessage `json:"capabilities"`
	Metadata     json.RawMessage `json:"metadata"`
}

type CreateSessionRequest struct {
	UserID         string          `json:"user_id"`
	NodeID         string          `json:"node_id"`
	AgentID        string          `json:"agent_id"`
	SessionID      string          `json:"session_id"`
	SessionName    string          `json:"name"`
	AgentType      string          `json:"agent_type"`
	NativeID       string          `json:"native_id"`
	ProjectID      string          `json:"project_id"`
	WorkspaceRoots []string        `json:"workspace_roots"`
	Source         string          `json:"source"`
	Metadata       json.RawMessage `json:"metadata"`
}

type OffsetRequest struct {
	Offset int64 `json:"offset"`
}

type Project struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RootPath string `json:"root_path"`
}

type MailboxPull struct {
	Messages  []MailboxMessage `json:"messages"`
	MaxOffset int64            `json:"max_offset"`
	HasMore   bool             `json:"has_more"`
}

type MailboxFilter struct {
	Principal UserPrincipal
	NodeID    string
	AgentID   string
	SessionID string
	Status    string
	Limit     int
}

func firstNonZeroFloat(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func DefaultMessageType(v string) string {
	switch v {
	case "chat", "steer", "command":
		return v
	case "":
		return "chat"
	default:
		return ""
	}
}

func ExpiresAt(now time.Time, messageType string) *time.Time {
	var t time.Time
	switch messageType {
	case "steer":
		t = now.Add(30 * time.Second)
	case "chat":
		t = now.Add(5 * time.Minute)
	case "command":
		t = now.Add(5 * time.Minute)
	default:
		return nil
	}
	return &t
}

func MailboxPayload(req CreateMailboxRequest) (json.RawMessage, error) {
	if len(req.Payload) > 0 {
		if !json.Valid(req.Payload) {
			return nil, errors.New("payload must be valid JSON")
		}
		return req.Payload, nil
	}

	var payload any
	switch DefaultMessageType(req.MessageType) {
	case "chat":
		payload = map[string]any{
			"entity_type": "turn",
			"event_type":  "start",
			"session_id":  req.SessionID,
			"prompt":      req.Message,
		}
	case "steer":
		payload = map[string]any{
			"entity_type": "turn",
			"event_type":  "cancel",
			"session_id":  req.SessionID,
			"mode":        "steer",
			"steer_text":  req.Message,
		}
	case "command":
		payload = map[string]any{
			"entity_type": "command",
			"event_type":  "execute",
			"session_id":  req.SessionID,
			"command":     req.Message,
		}
	default:
		return nil, errors.New("unsupported message type")
	}
	return json.Marshal(payload)
}

func FirstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func CanAccessOwner(principal UserPrincipal, ownerUserID string) bool {
	return principal.User.UserID == ownerUserID
}
