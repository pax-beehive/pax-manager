package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	SessionTransportManager = "manager"
	SessionTransportE2EE    = "e2ee"
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
	Description   string          `json:"description,omitempty"`
	Card          json.RawMessage `json:"card,omitempty"`
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
	UserMetadata  json.RawMessage `json:"user_metadata,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

// OwnerAgentFilter narrows an owner-scoped agent inventory. All fields are
// optional; the zero value returns every agent the owner owns. Filter
// semantics beyond owner scoping are applied by Store.ListOwnerAgents.
type OwnerAgentFilter struct {
	// Query is a free-text term matched against name, alias, and description.
	Query string
	// Status filters by effective reachability: "online" or "offline". Empty
	// (or "any") does not filter by status; the "online" product default is
	// applied at the service layer, not the store.
	Status string
	// OrderBy is "relevance", "last_active", or "name". Empty resolves to
	// "relevance" when Query is set and "last_active" otherwise.
	OrderBy string
	// Limit caps the number of returned agents. Zero means no cap at the store
	// layer; the service applies the product default and hard cap.
	Limit int
}

type Node struct {
	NodeID        string          `json:"node_id"`
	OwnerUserID   string          `json:"owner_user_id"`
	Kind          string          `json:"kind,omitempty"`
	Name          string          `json:"name,omitempty"`
	Description   string          `json:"description,omitempty"`
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
	UserMetadata  json.RawMessage `json:"user_metadata,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type AgentSession struct {
	ID                    int64                `json:"id"`
	NodeID                string               `json:"node_id,omitempty"`
	AgentID               string               `json:"agent_id"`
	SessionID             string               `json:"session_id"`
	ConversationID        string               `json:"conversation_id,omitempty"`
	ProfileID             string               `json:"profile_id,omitempty"`
	RepresentativeAgentID string               `json:"representative_agent_id,omitempty"`
	CreatedByUserID       string               `json:"created_by_user_id,omitempty"`
	SessionName           string               `json:"name,omitempty"`
	ReportedSessionName   string               `json:"reported_name,omitempty"`
	CustomSessionName     string               `json:"-"`
	NameIsCustom          bool                 `json:"name_is_custom"`
	AgentType             string               `json:"agent_type,omitempty"`
	NativeID              string               `json:"-"`
	PrimaryProjectID      string               `json:"primary_project_id,omitempty"`
	Preview               string               `json:"preview,omitempty"`
	WorkspaceRoots        []string             `json:"workspace_roots,omitempty"`
	Source                string               `json:"source,omitempty"`
	Transport             string               `json:"transport"`
	Status                string               `json:"status"`
	CurrentTask           string               `json:"current_task,omitempty"`
	LastMessageAt         *time.Time           `json:"last_message_at,omitempty"`
	LastUserMessageAt     *time.Time           `json:"last_user_message_at,omitempty"`
	MessageCount          int                  `json:"message_count"`
	TokenInput            int64                `json:"token_input"`
	TokenOutput           int64                `json:"token_output"`
	TokenTotal            int64                `json:"token_total"`
	TokenUsage            TokenUsage           `json:"token_usage"`
	Model                 string               `json:"model,omitempty"`
	RunID                 string               `json:"run_id,omitempty"`
	RunStatus             string               `json:"run_status,omitempty"`
	RuntimeStatus         string               `json:"runtime_status"`
	RuntimeTurnInstanceID string               `json:"runtime_turn_instance_id,omitempty"`
	RuntimeAuthority      string               `json:"-"`
	CreatedAt             time.Time            `json:"created_at"`
	UpdatedAt             time.Time            `json:"updated_at"`
	ArchivedAt            *time.Time           `json:"archived_at,omitempty"`
	Metadata              json.RawMessage      `json:"metadata,omitempty"`
	RuntimeState          *SessionRuntimeState `json:"runtime_state,omitempty"`
	PaxConfig             SessionPaxConfig     `json:"pax_config,omitempty"`
	ACPConfig             SessionACPConfig     `json:"-"`
}

type ListSessionsFilter struct {
	OwnerUserID      string
	NodeIDs          []string
	AgentIDs         []string
	PrimaryProjectID string
	IncludeArchived  bool
	PageSize         int
	PageNum          int
}

type ListSessionsResult struct {
	Sessions   []AgentSession `json:"sessions"`
	Pagination Pagination     `json:"pagination"`
}

type Pagination struct {
	PageNum    int   `json:"page_num"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

const (
	SessionApprovalModeManual         = "manual"
	SessionApprovalModeAutoApproveAll = "auto_approve_all"
)

func NormalizeSessionApprovalMode(mode string) string {
	if mode == "" {
		return SessionApprovalModeManual
	}
	return mode
}

func IsSessionApprovalMode(mode string) bool {
	switch mode {
	case SessionApprovalModeManual, SessionApprovalModeAutoApproveAll:
		return true
	default:
		return false
	}
}

type SessionPaxConfig struct {
	CWD                string `json:"cwd,omitempty"`
	ApprovalMode       string `json:"approval_mode,omitempty"`
	PermissionChoiceID string `json:"permission_choice_id,omitempty"`
}

const (
	ConversationTypeDirect           = "direct"
	ConversationTypeGroup            = "group"
	ConversationTypeAgentThread      = "agent_thread"
	ConversationTypeEscalationThread = "escalation_thread"

	ConversationStatusActive   = "active"
	ConversationStatusArchived = "archived"

	ConversationHistoryFullHistory   = "full_history"
	ConversationHistoryFromJoin      = "from_join"
	ConversationHistoryNone          = "none"
	ConversationHistoryAdminApproved = "admin_approved"

	ConversationMemberRoleOwner  = "owner"
	ConversationMemberRoleAdmin  = "admin"
	ConversationMemberRoleMember = "member"

	ConversationAgentRelationshipAssistant      = "assistant"
	ConversationAgentRelationshipRepresentative = "representative"
	ConversationAgentRelationshipReviewer       = "reviewer"
	ConversationAgentRelationshipParticipant    = "participant"

	ConversationAgentAccessThreadHistory       = "thread_history"
	ConversationAgentAccessFromBinding         = "from_binding"
	ConversationAgentAccessSelectedMessages    = "selected_messages"
	ConversationAgentAccessCurrentTurn         = "current_turn"
	ConversationAgentAccessSinceInvited        = "since_invited"
	ConversationAgentAccessFullHistoryApproved = "full_history_approved"

	ConversationAgentBindingStatusActive   = "active"
	ConversationAgentBindingStatusArchived = "archived"

	ConversationAgentInvocationStatusActive    = "active"
	ConversationAgentInvocationStatusCompleted = "completed"
	ConversationAgentInvocationStatusRevoked   = "revoked"
	ConversationAgentInvocationStatusCancelled = "cancelled"
	ConversationAgentInvocationStatusExpired   = "expired"
)

type AgentProfile struct {
	ProfileID       string          `json:"profile_id"`
	OwnerType       string          `json:"owner_type"`
	OwnerID         string          `json:"owner_id"`
	DisplayName     string          `json:"display_name"`
	Description     string          `json:"description,omitempty"`
	Card            json.RawMessage `json:"card,omitempty"`
	InstructionsMD  string          `json:"instructions_md,omitempty"`
	DefaultModel    string          `json:"default_model,omitempty"`
	ToolPolicy      json.RawMessage `json:"tool_policy,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	Status          string          `json:"status"`
	CreatedByUserID string          `json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	ArchivedAt      *time.Time      `json:"archived_at,omitempty"`
}

type RepresentativeAgent struct {
	RepresentativeAgentID string     `json:"representative_agent_id"`
	ProfileID             string     `json:"profile_id"`
	RuntimeAgentID        string     `json:"runtime_agent_id"`
	RepresentsType        string     `json:"represents_type"`
	RepresentsID          string     `json:"represents_id"`
	ApprovalPolicyID      string     `json:"approval_policy_id,omitempty"`
	Status                string     `json:"status"`
	CreatedByUserID       string     `json:"created_by_user_id,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	ArchivedAt            *time.Time `json:"archived_at,omitempty"`
}

type AgentOwnerInfoRequest struct {
	AgentID               string
	RepresentativeAgentID string
}

type AgentOwnerInfo struct {
	Agent               Agent                `json:"agent"`
	RepresentativeAgent *RepresentativeAgent `json:"representative_agent,omitempty"`
	Profile             *AgentProfile        `json:"profile,omitempty"`
	Owner               AgentOwnerSubject    `json:"owner"`
}

type AgentOwnerSubject struct {
	Kind string       `json:"kind"`
	User *User        `json:"user,omitempty"`
	Team *TeamSummary `json:"team,omitempty"`
}

type UpsertRepresentativeAgentRequest struct {
	RuntimeAgentID   string          `json:"runtime_agent_id"`
	ProfileID        string          `json:"profile_id,omitempty"`
	DisplayName      string          `json:"display_name,omitempty"`
	Description      string          `json:"description,omitempty"`
	RepresentsType   string          `json:"represents_type,omitempty"`
	RepresentsID     string          `json:"represents_id,omitempty"`
	ApprovalPolicyID string          `json:"approval_policy_id,omitempty"`
	Card             json.RawMessage `json:"card,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type Conversation struct {
	ConversationID   string     `json:"conversation_id"`
	ConversationType string     `json:"conversation_type"`
	BoundaryType     string     `json:"boundary_type"`
	BoundaryID       string     `json:"boundary_id,omitempty"`
	HistoryPolicy    string     `json:"history_policy"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
}

type ConversationMember struct {
	ConversationID string     `json:"conversation_id"`
	UserID         string     `json:"user_id"`
	Role           string     `json:"role"`
	JoinedAt       time.Time  `json:"joined_at"`
	LeftAt         *time.Time `json:"left_at,omitempty"`
}

type ConversationAgentBinding struct {
	BindingID             string     `json:"binding_id"`
	ConversationID        string     `json:"conversation_id"`
	RepresentativeAgentID string     `json:"representative_agent_id"`
	RelationshipType      string     `json:"relationship_type"`
	AddedByUserID         string     `json:"added_by_user_id"`
	AccessMode            string     `json:"access_mode"`
	Status                string     `json:"status"`
	CreatedAt             time.Time  `json:"created_at"`
	ArchivedAt            *time.Time `json:"archived_at,omitempty"`
}

type ConversationAgentInvocation struct {
	InvocationID                string          `json:"invocation_id"`
	ConversationID              string          `json:"conversation_id"`
	ParentInvocationID          string          `json:"parent_invocation_id,omitempty"`
	SourceRepresentativeAgentID string          `json:"source_representative_agent_id"`
	SourceRuntimeAgentID        string          `json:"source_runtime_agent_id"`
	SourceSessionID             string          `json:"source_session_id"`
	TargetRepresentativeAgentID string          `json:"target_representative_agent_id"`
	TargetRuntimeAgentID        string          `json:"target_runtime_agent_id"`
	TargetSessionID             string          `json:"target_session_id"`
	ReceiptTokenHash            string          `json:"-"`
	RequestedByUserID           string          `json:"requested_by_user_id"`
	AccessMode                  string          `json:"access_mode"`
	SelectedMessageIDsJSON      json.RawMessage `json:"selected_message_ids,omitempty"`
	MaxTurns                    int             `json:"max_turns,omitempty"`
	RemainingTurns              int             `json:"remaining_turns,omitempty"`
	Status                      string          `json:"status"`
	CreatedAt                   time.Time       `json:"created_at"`
	ExpiresAt                   *time.Time      `json:"expires_at,omitempty"`
	RevokedAt                   *time.Time      `json:"revoked_at,omitempty"`
}

type StartAgentConversationRequest struct {
	FromRuntimeAgentID        string `json:"-"`
	FromRepresentativeAgentID string `json:"from_representative_agent_id,omitempty"`
	ToRepresentativeAgentID   string `json:"to_representative_agent_id"`
	ConversationID            string `json:"conversation_id,omitempty"`
	Input                     string `json:"input"`
	MaxTurns                  int    `json:"max_turns,omitempty"`
}

type AgentConversationStart struct {
	Conversation         Conversation                `json:"conversation"`
	SourceBinding        ConversationAgentBinding    `json:"source_binding"`
	TargetBinding        ConversationAgentBinding    `json:"target_binding"`
	Invocation           ConversationAgentInvocation `json:"invocation"`
	SourceRepresentative RepresentativeAgent         `json:"source_representative"`
	TargetRepresentative RepresentativeAgent         `json:"target_representative"`
	SourceRuntimeAgent   Agent                       `json:"source_runtime_agent"`
	TargetRuntimeAgent   Agent                       `json:"target_runtime_agent"`
	SourceSession        AgentSession                `json:"source_session"`
	TargetSession        AgentSession                `json:"target_session"`
	PromptMessage        MessageWithParts            `json:"prompt_message"`
}

const (
	ConversationDeliveryTargetRepresentative   = "representative"
	ConversationDeliveryTargetActiveInvocation = "active_invocation"
	// ConversationDeliveryTargetAgent addresses a target runtime agent directly
	// by agent_id. The server maps the agent to its canonical representative and
	// reuses the representative delivery core, so the caller does not need to
	// know about representative agents.
	ConversationDeliveryTargetAgent = "agent"
)

type DeliverConversationRequest struct {
	Source      ConversationDeliverySource         `json:"source,omitempty"`
	Target      ConversationDeliveryTarget         `json:"target"`
	Context     ConversationDeliveryContextRequest `json:"context,omitempty"`
	Instruction string                             `json:"instruction,omitempty"`
	Reason      string                             `json:"reason,omitempty"`
}

type ConversationDelivery struct {
	Conversation     Conversation                 `json:"conversation"`
	Invocation       ConversationAgentInvocation  `json:"invocation"`
	ParentInvocation *ConversationAgentInvocation `json:"parent_invocation,omitempty"`
	SourceSession    AgentSession                 `json:"source_session"`
	TargetSession    AgentSession                 `json:"target_session"`
	PromptMessage    MessageWithParts             `json:"prompt_message"`
	Context          ConversationDeliveryContext  `json:"context"`
	Instruction      string                       `json:"instruction,omitempty"`
	Reason           string                       `json:"reason,omitempty"`
	DeliveryStatus   string                       `json:"delivery_status"`
	ReceiptToken     string                       `json:"receipt_token,omitempty"`
}

type ConversationDeliverySource struct {
	AgentID               string `json:"agent_id,omitempty"`
	RepresentativeAgentID string `json:"representative_agent_id,omitempty"`
	SessionID             string `json:"session_id,omitempty"`
}

type ConversationDeliveryTarget struct {
	Kind                  string `json:"kind"`
	RepresentativeAgentID string `json:"representative_agent_id,omitempty"`
	AgentID               string `json:"agent_id,omitempty"`
	SessionID             string `json:"session_id,omitempty"`
	InvocationID          string `json:"invocation_id,omitempty"`
}

type ConversationDeliveryContextRequest struct {
	LatestResponse   *bool `json:"latest_response,omitempty"`
	ToolCalls        *bool `json:"tool_calls,omitempty"`
	ReasoningSummary *bool `json:"reasoning_summary,omitempty"`
	Artifacts        *bool `json:"artifacts,omitempty"`
}

type ConversationDeliveryContext struct {
	LatestResponse   bool `json:"latest_response"`
	ToolCalls        bool `json:"tool_calls"`
	ReasoningSummary bool `json:"reasoning_summary"`
	Artifacts        bool `json:"artifacts"`
}

func (r ConversationDeliveryContextRequest) Effective() ConversationDeliveryContext {
	ctx := ConversationDeliveryContext{
		LatestResponse: true,
	}
	if r.LatestResponse != nil {
		ctx.LatestResponse = *r.LatestResponse
	}
	if r.ToolCalls != nil {
		ctx.ToolCalls = *r.ToolCalls
	}
	if r.ReasoningSummary != nil {
		ctx.ReasoningSummary = *r.ReasoningSummary
	}
	if r.Artifacts != nil {
		ctx.Artifacts = *r.Artifacts
	}
	return ctx
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

	MessageDirectionUserToAgent  = "user_to_agent"
	MessageDirectionAgentToUser  = "agent_to_user"
	MessageDirectionAgentToAgent = "agent_to_agent"

	MessageTypePaxInvocation        = "pax:invocation"
	MessageTypePaxInvocationPending = "pax:invocation_pending"
	MessageTypePaxArtifact          = "pax:artifact"
	MessageTypePaxUser              = "pax:user_message"
	MessageTypeUser                 = "user_message"

	MessagePartText     = "text"
	MessagePartRawJSON  = "raw_json"
	MessagePartArtifact = "artifact"
)

// Message is durable business history. Unlike TransportFrame, it is intended
// for user-visible replay and can aggregate many transport frames into one row.
type Message struct {
	ID              int64           `json:"id"`
	MessageID       string          `json:"message_id"`
	ConversationID  string          `json:"conversation_id,omitempty"`
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
	// SessionSeq is a session-scoped monotonic ordering key for the transcript,
	// assigned once at row creation and immutable thereafter. It is the single
	// ordering/cursor key shared by history, conversation and live-event views,
	// and is decoupled from the reliablemq transport offset. ConversationSeq is
	// the analogous key scoped to the message's conversation (0 when none).
	SessionSeq      int64     `json:"session_seq,omitempty"`
	ConversationSeq int64     `json:"conversation_seq,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
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

type MessageHistoryPage struct {
	Messages     []Message
	NextBeforeID int64
	HasMore      bool
	// seq-based cursor fields (transcript ordering refactor). HeadSeq is the
	// scope's current max seq so a client can tell whether it is behind.
	HeadSeq       int64
	HasOlder      bool
	HasNewer      bool
	NextBeforeSeq int64
	NextAfterSeq  int64
}

type MessageHistoryPagination struct {
	NextBeforeID  int64 `json:"next_before_id,omitempty"`
	HasMore       bool  `json:"has_more"`
	HeadSeq       int64 `json:"head_seq"`
	HasOlder      bool  `json:"has_older"`
	HasNewer      bool  `json:"has_newer"`
	NextBeforeSeq int64 `json:"next_before_seq,omitempty"`
	NextAfterSeq  int64 `json:"next_after_seq,omitempty"`
}

const (
	KnowledgeCapsuleStatusActive   = "active"
	KnowledgeCapsuleStatusArchived = "archived"

	KnowledgeInjectionStatusPending   = "pending"
	KnowledgeInjectionStatusDelivered = "delivered"
	KnowledgeInjectionStatusFailed    = "failed"
	KnowledgeInjectionStatusRevoked   = "revoked"

	KnowledgeInjectionDeliveryMailboxSteer = "mailbox_steer"
	MessageTypeSystemHandoff               = "system_handoff"
)

type KnowledgeCapsule struct {
	CapsuleID              string          `json:"capsule_id"`
	OwnerUserID            string          `json:"owner_user_id"`
	SourceSessionID        string          `json:"source_session_id"`
	SourceAgentID          string          `json:"source_agent_id"`
	SourceNodeID           string          `json:"source_node_id,omitempty"`
	CreatedByUserID        string          `json:"created_by_user_id"`
	Keyword                string          `json:"keyword"`
	Title                  string          `json:"title"`
	Summary                string          `json:"summary"`
	Content                string          `json:"content"`
	SuggestedSkills        json.RawMessage `json:"suggested_skills,omitempty"`
	References             json.RawMessage `json:"references,omitempty"`
	OpenQuestions          json.RawMessage `json:"open_questions,omitempty"`
	Risks                  json.RawMessage `json:"risks,omitempty"`
	Redactions             json.RawMessage `json:"redactions,omitempty"`
	Status                 string          `json:"status"`
	Truncated              bool            `json:"truncated"`
	OriginalEstimatedChars int64           `json:"original_estimated_chars"`
	CreatedAt              time.Time       `json:"created_at"`
	ArchivedAt             *time.Time      `json:"archived_at,omitempty"`
}

type SessionKnowledgeInjection struct {
	InjectionID         string     `json:"injection_id"`
	OwnerUserID         string     `json:"owner_user_id"`
	CapsuleID           string     `json:"capsule_id"`
	TargetSessionID     string     `json:"target_session_id"`
	TargetAgentID       string     `json:"target_agent_id"`
	TargetNodeID        string     `json:"target_node_id,omitempty"`
	CreatedByUserID     string     `json:"created_by_user_id"`
	DeliveredAsUserID   string     `json:"delivered_as_user_id,omitempty"`
	DeliveryMethod      string     `json:"delivery_method"`
	DeliveryMessageID   string     `json:"delivery_message_id,omitempty"`
	DeliveryMessageType string     `json:"delivery_message_type"`
	Status              string     `json:"status"`
	CreatedAt           time.Time  `json:"created_at"`
	DeliveredAt         *time.Time `json:"delivered_at,omitempty"`
	FailedAt            *time.Time `json:"failed_at,omitempty"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
	Error               string     `json:"error,omitempty"`
}

const (
	EnvelopePayloadKnowledgeCapsule = "knowledge_capsule"

	EnvelopeStatusPending  = "pending"
	EnvelopeStatusAccepted = "accepted"
	EnvelopeStatusArchived = "archived"
)

type Envelope struct {
	EnvelopeID      string          `json:"envelope_id"`
	SenderUserID    string          `json:"sender_user_id"`
	SenderEmail     string          `json:"sender_email"`
	RecipientUserID string          `json:"recipient_user_id,omitempty"`
	RecipientEmail  string          `json:"recipient_email"`
	FromAgentID     string          `json:"from_agent_id,omitempty"`
	ToAgentID       string          `json:"to_agent_id,omitempty"`
	PayloadType     string          `json:"payload_type"`
	PayloadJSON     json.RawMessage `json:"payload_json"`
	Message         string          `json:"message,omitempty"`
	Status          string          `json:"status"`
	CreatedAt       time.Time       `json:"created_at"`
	AcceptedAt      *time.Time      `json:"accepted_at,omitempty"`
	ArchivedAt      *time.Time      `json:"archived_at,omitempty"`
}

type CreateEnvelopeRequest struct {
	RecipientEmail string          `json:"recipient_email"`
	FromAgentID    string          `json:"from_agent_id,omitempty"`
	ToAgentID      string          `json:"to_agent_id,omitempty"`
	PayloadType    string          `json:"payload_type"`
	PayloadJSON    json.RawMessage `json:"payload_json"`
	Message        string          `json:"message,omitempty"`
}

type ListEnvelopesFilter struct {
	Principal UserPrincipal
	Status    string
	Direction string
	Limit     int
	Cursor    string
}

const (
	FriendStatusPending  = "pending"
	FriendStatusAccepted = "accepted"
	FriendStatusRemoved  = "removed"
	FriendStatusBlocked  = "blocked"

	FriendDirectionSent     = "sent"
	FriendDirectionReceived = "received"
)

const (
	EnvelopeDirectionSent     = "sent"
	EnvelopeDirectionReceived = "received"
)

type Friend struct {
	FriendID        string     `json:"friend_id"`
	RequesterUserID string     `json:"requester_user_id"`
	RequesterEmail  string     `json:"requester_email"`
	RequesterAlias  string     `json:"requester_alias,omitempty"`
	RecipientUserID string     `json:"recipient_user_id,omitempty"`
	RecipientEmail  string     `json:"recipient_email"`
	RecipientAlias  string     `json:"recipient_alias,omitempty"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	RemovedAt       *time.Time `json:"removed_at,omitempty"`
	BlockedAt       *time.Time `json:"blocked_at,omitempty"`
}

type CreateFriendRequest struct {
	Email string `json:"email"`
	Alias string `json:"alias,omitempty"`
}

type AcceptFriendRequest struct {
	Alias string `json:"alias,omitempty"`
}

type UpdateFriendAliasRequest struct {
	Alias string `json:"alias"`
}

type ListFriendsFilter struct {
	Principal UserPrincipal
	Status    string
	Direction string
	Alias     string
	Limit     int
	Cursor    string
}

type CreateKnowledgeCapsuleRequest struct {
	Keyword string `json:"keyword"`
}

type ListKnowledgeCapsulesFilter struct {
	Principal       UserPrincipal
	Status          string
	Keyword         string
	SourceSessionID string
	Limit           int
	Cursor          string
}

type InjectKnowledgeCapsuleRequest struct {
	CapsuleID string `json:"capsule_id"`
}

type ListKnowledgeInjectionsFilter struct {
	Principal       UserPrincipal
	TargetSessionID string
	Limit           int
	Cursor          string
}

const (
	TransportStreamManagerToPaxd = "manager_to_paxd"
	TransportStreamPaxdToManager = "paxd_to_manager"
	TransportStreamACP           = "acp"

	TransportDirectionInbound  = "inbound"
	TransportDirectionOutbound = "outbound"

	TransportStatusPending  = "pending"
	TransportStatusSent     = "sent"
	TransportStatusAcked    = "acked"
	TransportStatusReceived = "received"
	TransportStatusApplied  = "applied"
	TransportStatusFailed   = "failed"
	TransportStatusRejected = "rejected"
)

// TransportFrame is one durable frame in the manager<->paxd reliable transport
// journal. PayloadJSON is the raw ACP JSON-RPC payload, not the tunnel envelope.
type TransportFrame struct {
	ID             int64             `json:"id"`
	QueueID        string            `json:"queue_id"`
	AgentID        string            `json:"agent_id"`
	Stream         string            `json:"stream"`
	Seq            int64             `json:"seq"`
	Direction      string            `json:"direction"`
	LocalDirection string            `json:"local_direction"`
	Kind           string            `json:"kind"`
	PayloadJSON    json.RawMessage   `json:"payload_json"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Status         string            `json:"status"`
	ErrorMessage   string            `json:"error_message,omitempty"`
	Error          string            `json:"error,omitempty"`
	RetryCount     int               `json:"retry_count"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	SentAt         *time.Time        `json:"sent_at,omitempty"`
	ReceivedAt     *time.Time        `json:"received_at,omitempty"`
	AckedAt        *time.Time        `json:"acked_at,omitempty"`
	AppliedAt      *time.Time        `json:"applied_at,omitempty"`
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
	NativeID              string           `json:"native_id,omitempty"`
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
	RespondedAt           *time.Time       `json:"responded_at,omitempty"`
	ResponseBody          json.RawMessage  `json:"response_body,omitempty"`
	ResponseError         string           `json:"response_error,omitempty"`
}

const (
	AuditEventToolCallRequested = "tool_call_requested"
	AuditEventToolCallCompleted = "tool_call_completed"
	AuditEventApprovalRequested = "approval_requested"
	AuditEventApprovalDecided   = "approval_decided"
	AuditEventApprovalRevoked   = "approval_grant_revoked"
	AuditEventFileChanged       = "file_changed"
	AuditEventMessageCompleted  = "message_completed"
	AuditSourceApproval         = "approval"
	AuditSourceMailbox          = "mailbox"
)

type AgentAuditEvent struct {
	EventID         string          `json:"event_id"`
	OwnerUserID     string          `json:"owner_user_id"`
	NodeID          string          `json:"node_id,omitempty"`
	AgentID         string          `json:"agent_id,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	TurnID          string          `json:"turn_id,omitempty"`
	MessageID       string          `json:"message_id,omitempty"`
	ApprovalID      string          `json:"approval_id,omitempty"`
	EventType       string          `json:"event_type"`
	SourceType      string          `json:"source_type"`
	SourceID        string          `json:"source_id"`
	EventKey        string          `json:"event_key"`
	Title           string          `json:"title,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	ToolName        string          `json:"tool_name,omitempty"`
	ToolInput       json.RawMessage `json:"tool_input,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	RiskLevel       string          `json:"risk_level,omitempty"`
	ApprovalStatus  string          `json:"approval_status,omitempty"`
	Decision        string          `json:"decision,omitempty"`
	DecisionScope   string          `json:"decision_scope,omitempty"`
	DecidedByUserID string          `json:"decided_by_user_id,omitempty"`
	DecidedAt       *time.Time      `json:"decided_at,omitempty"`
	OccurredAt      time.Time       `json:"occurred_at"`
	Raw             json.RawMessage `json:"raw,omitempty"`
}

type AuditEventFilter struct {
	Principal  UserPrincipal
	Query      string
	EventType  string
	AgentID    string
	SessionID  string
	ApprovalID string
	Decision   string
	Limit      int
}

type AuditEventListData struct {
	Events []AgentAuditEvent `json:"events"`
}

type FileChange struct {
	Path       string `json:"path"`
	Tool       string `json:"tool"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

type RegisterNodeRequest struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Hostname     string          `json:"hostname"`
	MachineType  string          `json:"machine_type"`
	OS           string          `json:"os"`
	Arch         string          `json:"arch"`
	PaxdVersion  string          `json:"paxd_version"`
	APIEndpoint  string          `json:"api_endpoint"`
	UserMetadata json.RawMessage `json:"user_metadata"`
	Metadata     json.RawMessage `json:"metadata"`
}

type RegisterNodeResponse struct {
	NodeID string `json:"node_id"`
	APIKey string `json:"api_key"`
}

const (
	NodeRegistrationStatusPending  = "pending"
	NodeRegistrationStatusApproved = "approved"
	NodeRegistrationStatusDenied   = "denied"
	NodeRegistrationStatusExpired  = "expired"
	NodeRegistrationStatusConsumed = "consumed"
)

type NodeRegistrationSession struct {
	RegistrationID string              `json:"registration_id"`
	PairCode       string              `json:"pair_code"`
	PollTokenHash  string              `json:"-"`
	Status         string              `json:"status"`
	OwnerUserID    string              `json:"owner_user_id,omitempty"`
	NodeID         string              `json:"node_id,omitempty"`
	Request        RegisterNodeRequest `json:"request"`
	RequestIP      string              `json:"request_ip,omitempty"`
	RequestCity    string              `json:"request_city,omitempty"`
	RequestCountry string              `json:"request_country,omitempty"`
	ExpiresAt      time.Time           `json:"expires_at"`
	CreatedAt      time.Time           `json:"created_at"`
	ApprovedAt     *time.Time          `json:"approved_at,omitempty"`
	ConsumedAt     *time.Time          `json:"consumed_at,omitempty"`
}

type StartNodeRegistrationRequest struct {
	RegisterNodeRequest
}

type StartNodeRegistrationResponse struct {
	RegistrationID          string `json:"registration_id"`
	PairCode                string `json:"pair_code"`
	PollToken               string `json:"poll_token"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
	ExpiresAt               string `json:"expires_at"`
}

type PollNodeRegistrationRequest struct {
	RegistrationID string `json:"registration_id"`
	PollToken      string `json:"poll_token"`
}

type PollNodeRegistrationResponse struct {
	Status string `json:"status"`
	NodeID string `json:"node_id,omitempty"`
	APIKey string `json:"api_key,omitempty"`
}

type ApproveNodeRegistrationResponse struct {
	RegistrationID string `json:"registration_id"`
	PairCode       string `json:"pair_code"`
	Status         string `json:"status"`
	ExpiresAt      string `json:"expires_at"`
}

type NodeRegistrationNetworkPreview struct {
	IPAddress string `json:"ip_address,omitempty"`
	City      string `json:"city,omitempty"`
	Country   string `json:"country,omitempty"`
}

type NodeRegistrationPreviewResponse struct {
	RegistrationID string                         `json:"registration_id"`
	PairCode       string                         `json:"pair_code"`
	Status         string                         `json:"status"`
	Request        RegisterNodeRequest            `json:"request"`
	Network        NodeRegistrationNetworkPreview `json:"network,omitempty"`
	ExpiresAt      string                         `json:"expires_at"`
	CreatedAt      string                         `json:"created_at"`
}

const (
	PaxlDeviceLoginStatusPending  = "pending"
	PaxlDeviceLoginStatusApproved = "approved"
	PaxlDeviceLoginStatusExpired  = "expired"
	PaxlDeviceLoginStatusConsumed = "consumed"
)

type PaxlDeviceLoginSession struct {
	LoginID       string     `json:"login_id"`
	UserCode      string     `json:"user_code"`
	PollTokenHash string     `json:"-"`
	Status        string     `json:"status"`
	ClientName    string     `json:"client_name,omitempty"`
	OwnerUserID   string     `json:"owner_user_id,omitempty"`
	UserAPIKeyID  string     `json:"user_api_key_id,omitempty"`
	NodeID        string     `json:"node_id,omitempty"`
	APIKey        string     `json:"-"`
	ExpiresAt     time.Time  `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	ApprovedAt    *time.Time `json:"approved_at,omitempty"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
}

type StartPaxlDeviceLoginRequest struct {
	ClientName string `json:"client_name,omitempty"`
}

type StartPaxlDeviceLoginResponse struct {
	LoginID                 string `json:"login_id"`
	UserCode                string `json:"user_code"`
	PollToken               string `json:"poll_token"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
	ExpiresAt               string `json:"expires_at"`
}

type PollPaxlDeviceLoginRequest struct {
	LoginID   string `json:"login_id"`
	PollToken string `json:"poll_token"`
}

type PollPaxlDeviceLoginResponse struct {
	Status     string      `json:"status"`
	APIKey     string      `json:"api_key,omitempty"`
	NodeID     string      `json:"node_id,omitempty"`
	UserAPIKey *UserAPIKey `json:"api_key_meta,omitempty"`
	User       *User       `json:"user,omitempty"`
}

type ApprovePaxlDeviceLoginResponse struct {
	LoginID   string `json:"login_id"`
	UserCode  string `json:"user_code"`
	Status    string `json:"status"`
	NodeID    string `json:"node_id,omitempty"`
	ExpiresAt string `json:"expires_at"`
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
	Name          string                 `json:"name"`
	AgentType     string                 `json:"agent_type"`
	Hostname      string                 `json:"hostname"`
	MachineType   string                 `json:"machine_type"`
	OS            string                 `json:"os"`
	HermesVersion string                 `json:"hermes_version"`
	APIEndpoint   string                 `json:"api_endpoint"`
	Projects      []AgentReportedProject `json:"projects,omitempty"`
	Metadata      json.RawMessage        `json:"metadata"`
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
	Product     string     `json:"product"`
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
	Product     string   `json:"product"`
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
	Product  string
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
	Product    string       `json:"product"`
	Platform   string       `json:"platform"`
	Tags       []string     `json:"tags"`
	Generation int64        `json:"generation"`
}

const (
	ArtifactUploadStatusPending   = "pending"
	ArtifactUploadStatusCompleted = "completed"
)

const (
	ArtifactPublicationStatusQueued    = "queued"
	ArtifactPublicationStatusUploading = "uploading"
	ArtifactPublicationStatusAvailable = "available"
	ArtifactPublicationStatusFailed    = "failed"
)

type ArtifactPublication struct {
	PublicationID string    `json:"publication_id"`
	OwnerUserID   string    `json:"owner_user_id,omitempty"`
	NodeID        string    `json:"node_id"`
	AgentID       string    `json:"agent_id"`
	SessionID     string    `json:"session_id"`
	Filename      string    `json:"filename"`
	Title         string    `json:"title,omitempty"`
	Status        string    `json:"status"`
	ArtifactID    string    `json:"artifact_id,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ArtifactPublicationSource struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
}

type RegisterArtifactPublicationRequest struct {
	Source   ArtifactPublicationSource `json:"source"`
	Filename string                    `json:"filename"`
	Title    string                    `json:"title"`
}

type FailArtifactPublicationRequest struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

type ArtifactPublicationData struct {
	Publication ArtifactPublication `json:"publication"`
	Artifact    *SessionArtifact    `json:"artifact,omitempty"`
}

type ArtifactPublicationContentData struct {
	Status      string              `json:"status"`
	Retryable   bool                `json:"retryable"`
	Publication ArtifactPublication `json:"publication"`
	Artifact    *SessionArtifact    `json:"artifact,omitempty"`
	Content     *ArtifactContent    `json:"content,omitempty"`
	URL         string              `json:"url,omitempty"`
	ExpiresAt   *time.Time          `json:"expires_at,omitempty"`
	PreviewKind string              `json:"preview_kind,omitempty"`
	Disposition string              `json:"disposition,omitempty"`
}

const (
	UserAttachmentUploadPending   = "pending"
	UserAttachmentUploadCompleted = "completed"
	UserAttachmentUploadFailed    = "failed"
)

type UserAttachment struct {
	AttachmentID    string     `json:"attachment_id"`
	OwnerUserID     string     `json:"owner_user_id,omitempty"`
	ConversationID  string     `json:"conversation_id,omitempty"`
	Filename        string     `json:"filename"`
	ContentType     string     `json:"content_type,omitempty"`
	SizeBytes       int64      `json:"size_bytes,omitempty"`
	SHA256          string     `json:"sha256,omitempty"`
	Bucket          string     `json:"-"`
	Object          string     `json:"-"`
	Generation      int64      `json:"generation,omitempty"`
	UploadStatus    string     `json:"upload_status"`
	UploadExpiresAt time.Time  `json:"upload_expires_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type CreateUserAttachmentRequest struct {
	ConversationID string `json:"conversation_id"`
	Filename       string `json:"filename"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	SHA256         string `json:"sha256"`
}

type UserAttachmentUpload struct {
	Protocol       string            `json:"protocol"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers,omitempty"`
	ChunkAlignment int64             `json:"chunk_alignment,omitempty"`
	ExpiresAt      time.Time         `json:"expires_at"`
}

type UserAttachmentUploadTicket struct {
	Attachment  UserAttachment       `json:"attachment"`
	Upload      UserAttachmentUpload `json:"upload"`
	CompleteURL string               `json:"complete_url"`
}

type CompleteUserAttachmentData struct {
	Attachment UserAttachment `json:"attachment"`
}

const (
	SessionArtifactStatusAvailable = "available"
	SessionArtifactStatusProposed  = "proposed"
	SessionArtifactStatusFailed    = "failed"
)

type ArtifactUpload struct {
	UploadID    string     `json:"upload_id"`
	ArtifactID  string     `json:"artifact_id,omitempty"`
	OwnerUserID string     `json:"owner_user_id,omitempty"`
	NodeID      string     `json:"node_id,omitempty"`
	AgentID     string     `json:"agent_id,omitempty"`
	SessionID   string     `json:"session_id,omitempty"`
	Kind        string     `json:"kind,omitempty"`
	Title       string     `json:"title,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Filename    string     `json:"filename,omitempty"`
	ContentType string     `json:"content_type,omitempty"`
	SizeBytes   int64      `json:"size_bytes,omitempty"`
	SHA256      string     `json:"sha256,omitempty"`
	Bucket      string     `json:"bucket"`
	Object      string     `json:"object"`
	Generation  int64      `json:"generation,omitempty"`
	Status      string     `json:"status"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type PrepareArtifactPublicationRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type NodeArtifactUploadTicket struct {
	UploadID       string            `json:"upload_id"`
	Protocol       string            `json:"protocol"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers,omitempty"`
	ChunkAlignment int64             `json:"chunk_alignment"`
	ExpiresAt      time.Time         `json:"expires_at"`
}

type PrepareArtifactPublicationData struct {
	Status     string                    `json:"status"`
	ArtifactID string                    `json:"artifact_id"`
	Upload     *NodeArtifactUploadTicket `json:"upload,omitempty"`
}

type CompleteNodeArtifactUploadData struct {
	Status     string `json:"status"`
	ArtifactID string `json:"artifact_id"`
}

type CreateArtifactUploadRequest struct {
	SessionID   string `json:"session_id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type ArtifactUploadTicket struct {
	UploadID    string            `json:"upload_id"`
	Protocol    string            `json:"protocol"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Bucket      string            `json:"bucket"`
	Object      string            `json:"object"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Headers     map[string]string `json:"headers,omitempty"`
	Upload      ArtifactUpload    `json:"upload"`
	ContentRef  string            `json:"content_ref"`
	CompleteURL string            `json:"complete_url,omitempty"`
}

type CompleteArtifactUploadRequest struct {
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	Title         string          `json:"title"`
	Summary       string          `json:"summary"`
	Status        string          `json:"status"`
	SessionID     string          `json:"session_id"`
	MessageID     string          `json:"message_id"`
	NodeID        string          `json:"node_id"`
	AgentID       string          `json:"agent_id"`
	SourceJSON    json.RawMessage `json:"source_json"`
	PayloadJSON   json.RawMessage `json:"payload_json"`
}

type CompleteArtifactUploadData struct {
	Upload   ArtifactUpload  `json:"upload"`
	Artifact SessionArtifact `json:"artifact"`
}

type SessionArtifact struct {
	ArtifactID    string            `json:"artifact_id"`
	OwnerUserID   string            `json:"owner_user_id,omitempty"`
	Kind          string            `json:"kind"`
	SchemaVersion int               `json:"schema_version"`
	Title         string            `json:"title,omitempty"`
	Summary       string            `json:"summary,omitempty"`
	Status        string            `json:"status"`
	SessionID     string            `json:"session_id,omitempty"`
	MessageID     string            `json:"message_id,omitempty"`
	NodeID        string            `json:"node_id,omitempty"`
	AgentID       string            `json:"agent_id,omitempty"`
	SourceJSON    json.RawMessage   `json:"source_json,omitempty"`
	PayloadJSON   json.RawMessage   `json:"payload_json,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	DeletedAt     *time.Time        `json:"deleted_at,omitempty"`
	Contents      []ArtifactContent `json:"contents,omitempty"`
}

type ArtifactContent struct {
	ArtifactID  string    `json:"artifact_id,omitempty"`
	Ref         string    `json:"ref"`
	Filename    string    `json:"filename,omitempty"`
	ContentType string    `json:"content_type,omitempty"`
	SizeBytes   int64     `json:"size_bytes,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	Bucket      string    `json:"bucket,omitempty"`
	Object      string    `json:"object,omitempty"`
	Generation  int64     `json:"generation,omitempty"`
	StorageURI  string    `json:"storage_uri,omitempty"`
	Text        string    `json:"text,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

type CreateSessionArtifactRequest struct {
	ArtifactID    string            `json:"-"`
	Kind          string            `json:"kind"`
	SchemaVersion int               `json:"schema_version"`
	Title         string            `json:"title"`
	Summary       string            `json:"summary"`
	Status        string            `json:"status"`
	SessionID     string            `json:"session_id"`
	MessageID     string            `json:"message_id"`
	NodeID        string            `json:"node_id"`
	AgentID       string            `json:"agent_id"`
	SourceJSON    json.RawMessage   `json:"source_json"`
	PayloadJSON   json.RawMessage   `json:"payload_json"`
	Contents      []ArtifactContent `json:"contents"`
}

type ListSessionArtifactsFilter struct {
	Principal UserPrincipal
	SessionID string
	Kind      string
	Status    string
	Limit     int
	Cursor    string
}

type AttachArtifactRequest struct {
	ArtifactID string `json:"artifact_id"`
	MessageID  string `json:"message_id"`
	SessionID  string `json:"session_id"`
}

type ArtifactContentURLResponse struct {
	URL       string          `json:"url"`
	ExpiresAt time.Time       `json:"expires_at"`
	Artifact  SessionArtifact `json:"artifact"`
	Content   ArtifactContent `json:"content"`
}

type CreateUserAPIKeyResponse struct {
	APIKey UserAPIKey `json:"api_key"`
	Key    string     `json:"key"`
}

type CreateApprovalRequest struct {
	AgentID           string           `json:"agent_id"`
	SessionID         string           `json:"session_id"`
	NativeID          string           `json:"native_id"`
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
	NodeID       string             `json:"node_id"`
	RuntimeFence string             `json:"-"`
	Hostname     string             `json:"hostname"`
	MachineType  string             `json:"machine_type"`
	OS           string             `json:"os"`
	Arch         string             `json:"arch"`
	PaxdVersion  string             `json:"-"`
	Timestamp    time.Time          `json:"timestamp"`
	Agents       []AgentStatusInput `json:"agents"`
	System       json.RawMessage    `json:"system"`
	Metadata     json.RawMessage    `json:"metadata"`
}

type NodeAgentSessionReport struct {
	Sessions []SessionStatusInput `json:"sessions"`
}

type AgentStatusInput struct {
	AgentID         string                `json:"agent_id"`
	Name            string                `json:"name"`
	Description     string                `json:"description"`
	Card            json.RawMessage       `json:"card"`
	AgentType       string                `json:"agent_type"`
	Status          string                `json:"status"`
	Online          bool                  `json:"online"`
	LastHeartbeat   *time.Time            `json:"last_heartbeat"`
	Capabilities    json.RawMessage       `json:"capabilities"`
	Metadata        json.RawMessage       `json:"metadata"`
	Sessions        []SessionStatusInput  `json:"sessions"`
	RuntimeIdentity *AgentRuntimeIdentity `json:"-"`
}

type SessionStatusInput struct {
	SessionID         string     `json:"session_id"`
	AgentType         string     `json:"agent_type"`
	NativeID          string     `json:"native_id"`
	SessionName       string     `json:"name"`
	Preview           string     `json:"preview"`
	WorkspaceRoots    []string   `json:"workspace_roots"`
	Source            string     `json:"source"`
	Status            string     `json:"status"`
	CurrentTask       string     `json:"current_task"`
	LastMessageAt     *time.Time `json:"last_message_at"`
	LastUserMessageAt *time.Time `json:"last_user_message_at"`
	MessageCount      int        `json:"message_count"`
	TokenUsage        TokenUsage `json:"token_usage"`
	Model             string     `json:"model"`
	RunID             string     `json:"run_id"`
	RunStatus         string     `json:"run_status"`
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
	Description  string          `json:"description"`
	Card         json.RawMessage `json:"card"`
	AgentType    string          `json:"agent_type"`
	Capabilities json.RawMessage `json:"capabilities"`
	UserMetadata json.RawMessage `json:"user_metadata"`
	Metadata     json.RawMessage `json:"metadata"`
}

type CreateNodeDaemonAgentConnectionRequest struct {
	UserID              string   `json:"user_id"`
	NodeID              string   `json:"node_id"`
	CommandID           string   `json:"command_id"`
	Name                string   `json:"name"`
	AgentType           string   `json:"agent_type"`
	Harness             string   `json:"harness"`
	InstanceID          string   `json:"instance_id"`
	Command             []string `json:"command"`
	WorkingDir          string   `json:"working_dir"`
	DesiredSlots        *int     `json:"desired_slots"`
	ReportLocalSessions *bool    `json:"report_local_sessions"`
}

type DiscoverNodeDaemonHarnessesRequest struct {
	UserID string   `json:"user_id"`
	NodeID string   `json:"node_id"`
	Probe  bool     `json:"probe"`
	Names  []string `json:"names"`
}

type UpdateNodeDaemonAgentConnectionRequest struct {
	UserID              string    `json:"user_id"`
	NodeID              string    `json:"node_id"`
	ConnectionID        string    `json:"connection_id"`
	CommandID           string    `json:"command_id"`
	Name                *string   `json:"name"`
	Harness             *string   `json:"harness"`
	Command             *[]string `json:"command"`
	WorkingDir          *string   `json:"working_dir"`
	DesiredSlots        *int      `json:"desired_slots"`
	DesiredState        *string   `json:"desired_state"`
	ReportLocalSessions *bool     `json:"report_local_sessions"`
}

type RestartNodeDaemonRequest struct {
	UserID               string `json:"user_id"`
	NodeID               string `json:"node_id"`
	CommandID            string `json:"command_id"`
	Mode                 string `json:"mode"`
	ShutdownGraceSeconds *int   `json:"shutdown_grace_seconds"`
	IdleGraceSeconds     *int   `json:"idle_grace_seconds"`
	DrainTimeoutSeconds  *int   `json:"drain_timeout_seconds"`
	ForceAtDeadline      bool   `json:"force_at_deadline"`
	Reason               string `json:"reason"`
}
type NodeDaemonHeartbeat struct {
	BootID      string    `json:"boot_id"`
	PaxdVersion string    `json:"paxd_version"`
	DaemonPhase string    `json:"daemon_phase"`
	ObservedAt  time.Time `json:"observed_at"`
}

type NodeDaemonMaintenanceConfirmation struct {
	CommandID       string    `json:"command_id"`
	RequestedBootID string    `json:"requested_boot_id"`
	ObservedBootID  string    `json:"observed_boot_id,omitempty"`
	ExpectedVersion string    `json:"expected_version,omitempty"`
	ObservedVersion string    `json:"observed_version,omitempty"`
	Status          string    `json:"status"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type UpgradeNodeDaemonRequest struct {
	UserID               string `json:"user_id"`
	NodeID               string `json:"node_id"`
	CommandID            string `json:"command_id"`
	Version              string `json:"version"`
	Tag                  string `json:"tag"`
	Mode                 string `json:"mode"`
	ShutdownGraceSeconds *int   `json:"shutdown_grace_seconds"`
	IdleGraceSeconds     *int   `json:"idle_grace_seconds"`
	DrainTimeoutSeconds  *int   `json:"drain_timeout_seconds"`
	ForceAtDeadline      bool   `json:"force_at_deadline"`
	Reason               string `json:"reason"`
}

type CancelNodeDaemonMaintenanceRequest struct {
	UserID               string `json:"user_id"`
	NodeID               string `json:"node_id"`
	MaintenanceCommandID string `json:"maintenance_command_id"`
	CommandID            string `json:"command_id"`
}

type NodeDaemonAgentConnectionActionRequest struct {
	UserID       string `json:"user_id"`
	NodeID       string `json:"node_id"`
	ConnectionID string `json:"connection_id"`
	CommandID    string `json:"command_id"`
}

// PushNodeDaemonSecretChannelRequest carries an already-sealed secret: the
// browser encrypted it client-side against a public key obtained from
// OpenNodeDaemonSecretChannel, so pax-manager only ever forwards these
// base64 fields as opaque bytes to the paxd control tunnel. It never has a
// decryption key and never sees the plaintext.
type PushNodeDaemonSecretChannelRequest struct {
	UserID          string `json:"user_id"`
	NodeID          string `json:"node_id"`
	CommandID       string `json:"command_id"`
	ChannelID       string `json:"channel_id"`
	SenderPublicKey string `json:"sender_public_key"`
	Nonce           string `json:"nonce"`
	Ciphertext      string `json:"ciphertext"`
}

type UpdateNodeRequest struct {
	UserID       string          `json:"user_id"`
	NodeID       string          `json:"node_id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	UserMetadata json.RawMessage `json:"user_metadata"`
}

type DeleteNodeRequest struct {
	UserID string `json:"user_id"`
	NodeID string `json:"node_id"`
}

type UpdateAgentProfileRequest struct {
	UserID       string          `json:"user_id"`
	NodeID       string          `json:"node_id"`
	AgentID      string          `json:"agent_id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Card         json.RawMessage `json:"card"`
	UserMetadata json.RawMessage `json:"user_metadata"`
}

type DeleteAgentRequest struct {
	UserID  string `json:"user_id"`
	NodeID  string `json:"node_id"`
	AgentID string `json:"agent_id"`
}

type CreateSessionRequest struct {
	UserID                string           `json:"user_id"`
	NodeID                string           `json:"node_id"`
	AgentID               string           `json:"agent_id"`
	SessionID             string           `json:"session_id"`
	ConversationID        string           `json:"conversation_id"`
	ProfileID             string           `json:"profile_id"`
	RepresentativeAgentID string           `json:"representative_agent_id"`
	CreatedByUserID       string           `json:"created_by_user_id"`
	SessionName           string           `json:"name"`
	AgentType             string           `json:"agent_type"`
	NativeID              string           `json:"native_id"`
	PrimaryProjectID      string           `json:"primary_project_id"`
	WorkspaceRoots        []string         `json:"workspace_roots"`
	Source                string           `json:"source"`
	Metadata              json.RawMessage  `json:"metadata"`
	PaxConfig             SessionPaxConfig `json:"pax_config"`
}

type UpdateSessionRequest struct {
	UserID          string           `json:"user_id"`
	NodeID          string           `json:"node_id"`
	AgentID         string           `json:"agent_id"`
	SessionID       string           `json:"session_id"`
	SessionName     *string          `json:"name"`
	UseReportedName bool             `json:"use_reported_name"`
	Archived        *bool            `json:"archived"`
	PaxConfig       SessionPaxConfig `json:"pax_config"`
}

type OffsetRequest struct {
	Offset int64 `json:"offset"`
}

type AgentReportedProject struct {
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
	case "chat", "steer", "command", MessageTypeSystemHandoff:
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
	case "command", MessageTypeSystemHandoff:
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
	case MessageTypeSystemHandoff:
		payload = map[string]any{
			"entity_type":  "knowledge",
			"event_type":   MessageTypeSystemHandoff,
			"message_type": MessageTypeSystemHandoff,
			"session_id":   req.SessionID,
			"handoff":      req.Message,
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
