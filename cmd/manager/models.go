package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	UserID      string     `json:"userId"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastSeenAt  *time.Time `json:"lastSeenAt,omitempty"`
}

type UserPrincipal struct {
	User    User
	IsAdmin bool
}

type Agent struct {
	AgentID       string          `json:"agentId"`
	OwnerUserID   string          `json:"ownerUserId"`
	Name          string          `json:"name,omitempty"`
	Hostname      string          `json:"hostname"`
	AgentType     string          `json:"agentType"`
	MachineType   string          `json:"machineType,omitempty"`
	OS            string          `json:"os"`
	HermesVersion string          `json:"hermesVersion,omitempty"`
	APIEndpoint   string          `json:"apiEndpoint"`
	Status        string          `json:"status"`
	Online        bool            `json:"online"`
	LastHeartbeat *time.Time      `json:"lastHeartbeat,omitempty"`
	RegisteredAt  time.Time       `json:"registeredAt"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type AgentSession struct {
	ID             int64      `json:"id"`
	AgentID        string     `json:"agentId"`
	SessionID      string     `json:"sessionId"`
	SessionName    string     `json:"name,omitempty"`
	AgentType      string     `json:"agentType,omitempty"`
	NativeID       string     `json:"nativeId,omitempty"`
	ProjectID      string     `json:"projectId,omitempty"`
	Preview        string     `json:"preview,omitempty"`
	WorkspaceRoots []string   `json:"workspaceRoots,omitempty"`
	Source         string     `json:"source,omitempty"`
	Status         string     `json:"status"`
	CurrentTask    string     `json:"currentTask,omitempty"`
	LastMessageAt  *time.Time `json:"lastMessageAt,omitempty"`
	MessageCount   int        `json:"messageCount"`
	TokenInput     int64      `json:"tokenInput"`
	TokenOutput    int64      `json:"tokenOutput"`
	TokenTotal     int64      `json:"tokenUsage"`
	Model          string     `json:"model,omitempty"`
	RunID          string     `json:"runId,omitempty"`
	RunStatus      string     `json:"runStatus,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type MailboxMessage struct {
	ID          int64           `json:"id"`
	MessageID   string          `json:"messageId"`
	UserID      string          `json:"userId"`
	OwnerUserID string          `json:"ownerUserId"`
	AgentID     string          `json:"agentId"`
	SessionID   string          `json:"sessionId,omitempty"`
	Message     string          `json:"message"`
	MessageType string          `json:"messageType"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Status      string          `json:"status"`
	DeliveredAt *time.Time      `json:"deliveredAt,omitempty"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
	Result      string          `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	ExpiresAt   *time.Time      `json:"expiresAt,omitempty"`
}

type RegisterAgentRequest struct {
	Name          string          `json:"name"`
	AgentType     string          `json:"agentType"`
	Hostname      string          `json:"hostname"`
	MachineType   string          `json:"machineType"`
	OS            string          `json:"os"`
	HermesVersion string          `json:"hermesVersion"`
	APIEndpoint   string          `json:"apiEndpoint"`
	Projects      []Project       `json:"projects,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
}

type RegisterAgentResponse struct {
	AgentID string `json:"agentId"`
	APIKey  string `json:"apiKey"`
}

type CreateRegistrationTokenRequest struct {
	OwnerUserID      string `json:"ownerUserId"`
	OwnerEmail       string `json:"ownerEmail"`
	ExpiresInSeconds int64  `json:"expiresInSeconds"`
}

type CreateRegistrationTokenResponse struct {
	Token       string     `json:"token"`
	OwnerUserID string     `json:"ownerUserId"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

type UserAPIKey struct {
	KeyID       string     `json:"keyId"`
	OwnerUserID string     `json:"ownerUserId"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

type CreateUserAPIKeyRequest struct {
	Name string `json:"name"`
}

type CreateUserAPIKeyResponse struct {
	APIKey UserAPIKey `json:"apiKey"`
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
	SessionID      string     `json:"sessionId"`
	AgentType      string     `json:"agentType"`
	NativeID       string     `json:"nativeId"`
	SessionName    string     `json:"name"`
	ProjectID      string     `json:"projectId"`
	Preview        string     `json:"preview"`
	WorkspaceRoots []string   `json:"workspaceRoots"`
	Source         string     `json:"source"`
	Status         string     `json:"status"`
	CurrentTask    string     `json:"currentTask"`
	LastMessageAt  *time.Time `json:"lastMessageAt"`
	MessageCount   int        `json:"messageCount"`
	TokenUsage     TokenUsage `json:"tokenUsage"`
	Model          string     `json:"model"`
	RunID          string     `json:"runId"`
	RunStatus      string     `json:"runStatus"`
}

type TokenUsage struct {
	Input  int64 `json:"inputTokens"`
	Output int64 `json:"outputTokens"`
	Total  int64 `json:"totalTokens"`
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
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
		TotalTokens  int64 `json:"totalTokens"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	u.Input = firstNonZero(object.InputTokens, object.Input)
	u.Output = firstNonZero(object.OutputTokens, object.Output)
	u.Total = firstNonZero(object.TotalTokens, object.Total)
	if u.Total == 0 {
		u.Total = u.Input + u.Output
	}
	return nil
}

type CreateMailboxRequest struct {
	AgentID     string          `json:"agentId"`
	SessionID   string          `json:"sessionId"`
	Message     string          `json:"message"`
	MessageType string          `json:"messageType"`
	Payload     json.RawMessage `json:"payload"`
}

type MessageResultRequest struct {
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
	RootPath string `json:"rootPath"`
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

func newSecret(prefix string) (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func defaultMessageType(v string) string {
	switch v {
	case "chat", "steer", "command":
		return v
	case "":
		return "chat"
	default:
		return ""
	}
}

func expiresAt(now time.Time, messageType string) *time.Time {
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

func mailboxPayload(req CreateMailboxRequest) (json.RawMessage, error) {
	if len(req.Payload) > 0 {
		if !json.Valid(req.Payload) {
			return nil, errors.New("payload must be valid JSON")
		}
		return req.Payload, nil
	}

	var payload any
	switch defaultMessageType(req.MessageType) {
	case "chat":
		payload = map[string]any{
			"entity_type": "turn",
			"event_type":  "start",
			"sessionId":   req.SessionID,
			"prompt":      req.Message,
		}
	case "steer":
		payload = map[string]any{
			"entity_type": "turn",
			"event_type":  "cancel",
			"sessionId":   req.SessionID,
			"mode":        "steer",
			"steerText":   req.Message,
		}
	case "command":
		payload = map[string]any{
			"entity_type": "command",
			"event_type":  "execute",
			"sessionId":   req.SessionID,
			"command":     req.Message,
		}
	default:
		return nil, errors.New("unsupported message type")
	}
	return json.Marshal(payload)
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func canAccessOwner(principal UserPrincipal, ownerUserID string) bool {
	return principal.IsAdmin || principal.User.UserID == ownerUserID
}
