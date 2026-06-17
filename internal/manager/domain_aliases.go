package manager

import (
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
type MailboxMessage = domain.MailboxMessage
type ApprovalOption = domain.ApprovalOption
type AgentApproval = domain.AgentApproval
type RegisterNodeRequest = domain.RegisterNodeRequest
type RegisterNodeResponse = domain.RegisterNodeResponse
type RegisterNodeAgentRequest = domain.RegisterNodeAgentRequest
type RegisterNodeAgentResponse = domain.RegisterNodeAgentResponse
type RegisterAgentRequest = domain.RegisterAgentRequest
type RegisterAgentResponse = domain.RegisterAgentResponse
type CreateRegistrationTokenRequest = domain.CreateRegistrationTokenRequest
type CreateRegistrationTokenResponse = domain.CreateRegistrationTokenResponse
type UserAPIKey = domain.UserAPIKey
type CreateUserAPIKeyRequest = domain.CreateUserAPIKeyRequest
type CreateUserAPIKeyResponse = domain.CreateUserAPIKeyResponse
type NodeStatusReport = domain.NodeStatusReport
type AgentStatusReport = domain.AgentStatusReport
type SessionStatusInput = domain.SessionStatusInput
type TokenUsage = domain.TokenUsage
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
type OffsetRequest = domain.OffsetRequest
type Project = domain.Project
type MailboxPull = domain.MailboxPull
type MailboxFilter = domain.MailboxFilter
type Store = domain.Store

func hashSecret(secret string) string {
	return auth.HashSecret(secret)
}

func normalizeEmail(email string) string {
	return domain.NormalizeEmail(email)
}
