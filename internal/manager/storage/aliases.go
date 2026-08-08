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
type OwnerAgentFilter = domain.OwnerAgentFilter
type AgentSession = domain.AgentSession
type ListSessionsFilter = domain.ListSessionsFilter
type SessionPaxConfig = domain.SessionPaxConfig
type SessionRuntimeState = domain.SessionRuntimeState
type MailboxMessage = domain.MailboxMessage
type ApprovalOption = domain.ApprovalOption
type AgentApproval = domain.AgentApproval
type AgentAuditEvent = domain.AgentAuditEvent
type AuditEventFilter = domain.AuditEventFilter
type RegisterNodeRequest = domain.RegisterNodeRequest
type NodeRegistrationSession = domain.NodeRegistrationSession
type NodeRegistrationPreviewResponse = domain.NodeRegistrationPreviewResponse
type PaxlDeviceLoginSession = domain.PaxlDeviceLoginSession
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
type UserAttachment = domain.UserAttachment
type CreateUserAttachmentRequest = domain.CreateUserAttachmentRequest
type ArtifactPublication = domain.ArtifactPublication
type PrepareArtifactPublicationRequest = domain.PrepareArtifactPublicationRequest
type ArtifactUpload = domain.ArtifactUpload
type CreateArtifactUploadRequest = domain.CreateArtifactUploadRequest
type CompleteArtifactUploadRequest = domain.CompleteArtifactUploadRequest
type SessionArtifact = domain.SessionArtifact
type ArtifactContent = domain.ArtifactContent
type CreateSessionArtifactRequest = domain.CreateSessionArtifactRequest
type ListSessionArtifactsFilter = domain.ListSessionArtifactsFilter
type AttachArtifactRequest = domain.AttachArtifactRequest
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
type UpdateNodeRequest = domain.UpdateNodeRequest
type DeleteNodeRequest = domain.DeleteNodeRequest
type UpdateAgentProfileRequest = domain.UpdateAgentProfileRequest
type DeleteAgentRequest = domain.DeleteAgentRequest
type CreateSessionRequest = domain.CreateSessionRequest
type UpdateSessionRequest = domain.UpdateSessionRequest
type Project = domain.Project
type CreateProjectRequest = domain.CreateProjectRequest
type UpdateProjectRequest = domain.UpdateProjectRequest
type ListProjectsFilter = domain.ListProjectsFilter
type ProjectTarget = domain.ProjectTarget
type CreateProjectTargetRequest = domain.CreateProjectTargetRequest
type UpdateProjectTargetRequest = domain.UpdateProjectTargetRequest
type MailboxPull = domain.MailboxPull
type MailboxFilter = domain.MailboxFilter
type Message = domain.Message
type MessagePart = domain.MessagePart
type KnowledgeCapsule = domain.KnowledgeCapsule
type SessionKnowledgeInjection = domain.SessionKnowledgeInjection
type ListKnowledgeCapsulesFilter = domain.ListKnowledgeCapsulesFilter
type ListKnowledgeInjectionsFilter = domain.ListKnowledgeInjectionsFilter
type Envelope = domain.Envelope
type ListEnvelopesFilter = domain.ListEnvelopesFilter
type Friend = domain.Friend
type ListFriendsFilter = domain.ListFriendsFilter
type Team = domain.Team
type TeamSummary = domain.TeamSummary
type TeamMember = domain.TeamMember
type TeamInvite = domain.TeamInvite
type TeamAgent = domain.TeamAgent
type TeamAuditEvent = domain.TeamAuditEvent
type TeamMemexDocument = domain.TeamMemexDocument
type TeamMemexDocumentOperation = domain.TeamMemexDocumentOperation
type TeamMemexManifest = domain.TeamMemexManifest
type TeamMemexManifestOperation = domain.TeamMemexManifestOperation
type TeamMemexRun = domain.TeamMemexRun
type TeamMemexRunAttempt = domain.TeamMemexRunAttempt
type AddTeamAgentRequest = domain.AddTeamAgentRequest
type TransportFrame = domain.TransportFrame
type E2EERecord = domain.E2EERecord
type AgentCommand = domain.AgentCommand
type AgentEvent = domain.AgentEvent

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

func normalizeSessionApprovalMode(mode string) string {
	return domain.NormalizeSessionApprovalMode(mode)
}

func canAccessOwner(principal UserPrincipal, ownerUserID string) bool {
	return domain.CanAccessOwner(principal, ownerUserID)
}

func totalPages(total int64, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 0
	}
	return int((total + int64(pageSize) - 1) / int64(pageSize))
}

func normalizeSessionPage(pageSize int, pageNum int) (int, int) {
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageNum <= 0 {
		pageNum = 1
	}
	return pageSize, pageNum
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func teamAgentAccessSQL(agentIDExpr string, userIDParam string) string {
	return `EXISTS (
		SELECT 1
		FROM team_agents ta
		JOIN team_members tm ON tm.team_id = ta.team_id
		JOIN teams t ON t.team_id = ta.team_id
		WHERE ta.agent_id = ` + agentIDExpr + `
			AND ta.removed_at IS NULL
			AND tm.user_id = ` + userIDParam + `
			AND tm.status = '` + domain.TeamMemberStatusActive + `'
			AND t.status = '` + domain.TeamStatusActive + `'
	)`
}

func isManagerSessionID(sessionID string) bool {
	return strings.HasPrefix(sessionID, "sess_") || strings.HasPrefix(sessionID, "sess-")
}

func reportedNativeSessionID(input SessionStatusInput) string {
	if input.NativeID != "" {
		return stripAgentTypeSessionPrefix(input.AgentType, input.NativeID)
	}
	return stripAgentTypeSessionPrefix(input.AgentType, input.SessionID)
}

func stripAgentTypeSessionPrefix(agentType string, sessionID string) string {
	prefix := strings.TrimSpace(agentType)
	if prefix == "" {
		return sessionID
	}
	return strings.TrimPrefix(sessionID, prefix+":")
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

func paxConfigMetadata(raw json.RawMessage, config SessionPaxConfig) json.RawMessage {
	object := map[string]any{}
	if len(raw) > 0 && json.Valid(raw) {
		_ = json.Unmarshal(raw, &object)
	}
	config.ApprovalMode = normalizeSessionApprovalMode(config.ApprovalMode)
	object["pax_config"] = config
	data, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return data
}
