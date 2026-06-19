package storage

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var (
	ErrNotFound     = domain.ErrNotFound
	ErrUnauthorized = domain.ErrUnauthorized
	ErrConflict     = domain.ErrConflict
)

type User = domain.User
type UserPrincipal = domain.UserPrincipal
type Node = domain.Node
type Agent = domain.Agent
type AgentSession = domain.AgentSession
type SessionRuntimeState = domain.SessionRuntimeState
type MailboxMessage = domain.MailboxMessage
type ApprovalOption = domain.ApprovalOption
type AgentApproval = domain.AgentApproval
type RegisterNodeRequest = domain.RegisterNodeRequest
type NodeRegistrationSession = domain.NodeRegistrationSession
type NodeRegistrationPreviewResponse = domain.NodeRegistrationPreviewResponse
type RegisterAgentRequest = domain.RegisterAgentRequest
type UserAPIKey = domain.UserAPIKey
type Secret = domain.Secret
type SecretVersion = domain.SecretVersion
type CreateSecretRequest = domain.CreateSecretRequest
type WriteSecretVersionRequest = domain.WriteSecretVersionRequest
type SecretAccessEvent = domain.SecretAccessEvent
type PaxdArtifact = domain.PaxdArtifact
type CreatePaxdArtifactRequest = domain.CreatePaxdArtifactRequest
type FindPaxdArtifactRequest = domain.FindPaxdArtifactRequest
type NodeStatusReport = domain.NodeStatusReport
type AgentStatusInput = domain.AgentStatusInput
type AgentStatusReport = domain.AgentStatusReport
type SessionStatusInput = domain.SessionStatusInput
type FileChange = domain.FileChange
type CreateMailboxRequest = domain.CreateMailboxRequest
type MessageResultRequest = domain.MessageResultRequest
type MarkDeliveredRequest = domain.MarkDeliveredRequest
type CreateOutboundMessageRequest = domain.CreateOutboundMessageRequest
type CreateApprovalRequest = domain.CreateApprovalRequest
type ApprovalFilter = domain.ApprovalFilter
type ApprovalGrantFilter = domain.ApprovalGrantFilter
type ApprovalDecisionRequest = domain.ApprovalDecisionRequest
type ApprovalGrantLookup = domain.ApprovalGrantLookup
type RevokeApprovalGrantRequest = domain.RevokeApprovalGrantRequest
type CreateAgentRequest = domain.CreateAgentRequest
type CreateSessionRequest = domain.CreateSessionRequest
type MailboxPull = domain.MailboxPull
type MailboxFilter = domain.MailboxFilter
type Message = domain.Message
type MessagePart = domain.MessagePart
type TransportFrame = domain.TransportFrame

func newSecret(prefix string) (string, error) {
	return auth.NewSecret(prefix)
}

func defaultMessageType(v string) string {
	return domain.DefaultMessageType(v)
}

func expiresAt(now time.Time, messageType string) *time.Time {
	return domain.ExpiresAt(now, messageType)
}

func mailboxPayload(req CreateMailboxRequest) (json.RawMessage, error) {
	return domain.MailboxPayload(req)
}

func normalizeEmail(email string) string {
	return domain.NormalizeEmail(email)
}

func canAccessOwner(principal UserPrincipal, ownerUserID string) bool {
	return domain.CanAccessOwner(principal, ownerUserID)
}

func isManagerSessionID(sessionID string) bool {
	return strings.HasPrefix(sessionID, "sess_") || strings.HasPrefix(sessionID, "sess-")
}

func reportedNativeSessionID(input SessionStatusInput) string {
	if input.NativeID != "" && isManagerSessionID(input.SessionID) {
		return input.NativeID
	}
	return input.SessionID
}

func replacePayloadSessionID(raw json.RawMessage, sessionID string) json.RawMessage {
	if sessionID == "" || len(raw) == 0 || !json.Valid(raw) {
		return raw
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return raw
	}
	changed := false
	for _, key := range []string{"session_id", "sessionId"} {
		if _, ok := object[key]; ok {
			object[key] = sessionID
			changed = true
		}
	}
	if !changed {
		return raw
	}
	data, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return data
}

func runtimeMetadata(raw json.RawMessage, state SessionRuntimeState) json.RawMessage {
	object := map[string]any{}
	if len(raw) > 0 && json.Valid(raw) {
		_ = json.Unmarshal(raw, &object)
	}
	object["runtime_state"] = state
	data, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return data
}
