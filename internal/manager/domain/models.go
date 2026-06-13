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

type AgentSession struct {
	ID             int64      `json:"id"`
	AgentID        string     `json:"agent_id"`
	SessionID      string     `json:"session_id"`
	SessionName    string     `json:"name,omitempty"`
	AgentType      string     `json:"agent_type,omitempty"`
	NativeID       string     `json:"native_id,omitempty"`
	ProjectID      string     `json:"project_id,omitempty"`
	Preview        string     `json:"preview,omitempty"`
	WorkspaceRoots []string   `json:"workspace_roots,omitempty"`
	Source         string     `json:"source,omitempty"`
	Status         string     `json:"status"`
	CurrentTask    string     `json:"current_task,omitempty"`
	LastMessageAt  *time.Time `json:"last_message_at,omitempty"`
	MessageCount   int        `json:"message_count"`
	TokenInput     int64      `json:"token_input"`
	TokenOutput    int64      `json:"token_output"`
	TokenTotal     int64      `json:"token_usage"`
	Model          string     `json:"model,omitempty"`
	RunID          string     `json:"run_id,omitempty"`
	RunStatus      string     `json:"run_status,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type MailboxMessage struct {
	ID          int64           `json:"id"`
	MessageID   string          `json:"message_id"`
	UserID      string          `json:"user_id"`
	OwnerUserID string          `json:"owner_user_id"`
	AgentID     string          `json:"agent_id"`
	SessionID   string          `json:"session_id,omitempty"`
	Message     string          `json:"message"`
	MessageType string          `json:"message_type"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Status      string          `json:"status"`
	DeliveredAt *time.Time      `json:"delivered_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Result      string          `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
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

type CreateUserAPIKeyResponse struct {
	APIKey UserAPIKey `json:"api_key"`
	Key    string     `json:"key"`
}

type AgentStatusReport struct {
	AgentID   string               `json:"agent_id"`
	Hostname  string               `json:"hostname"`
	Timestamp time.Time            `json:"timestamp"`
	Sessions  []SessionStatusInput `json:"sessions"`
	System    json.RawMessage      `json:"system"`
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
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
	Total  int64 `json:"total_tokens"`
}

func (u *TokenUsage) UnmarshalJSON(data []byte) error {
	var total int64
	if err := json.Unmarshal(data, &total); err == nil {
		u.Total = total
		return nil
	}

	var object struct {
		Input        int64 `json:"input"`
		Output       int64 `json:"output"`
		Total        int64 `json:"total"`
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	u.Input = FirstNonZero(object.InputTokens, object.Input)
	u.Output = FirstNonZero(object.OutputTokens, object.Output)
	u.Total = FirstNonZero(object.TotalTokens, object.Total)
	if u.Total == 0 {
		u.Total = u.Input + u.Output
	}
	return nil
}

type CreateMailboxRequest struct {
	AgentID     string          `json:"agent_id"`
	SessionID   string          `json:"session_id"`
	Message     string          `json:"message"`
	MessageType string          `json:"message_type"`
	Payload     json.RawMessage `json:"payload"`
}

type MessageResultRequest struct {
	MessageID   string     `json:"message_id"`
	Status      string     `json:"status"`
	Result      string     `json:"result"`
	Error       string     `json:"error"`
	CompletedAt *time.Time `json:"completed_at"`
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
	AgentID   string
	SessionID string
	Status    string
	Limit     int
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
	return principal.IsAdmin || principal.User.UserID == ownerUserID
}
