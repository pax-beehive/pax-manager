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
type MessageWithParts = domain.MessageWithParts
type KnowledgeCapsule = domain.KnowledgeCapsule
type SessionKnowledgeInjection = domain.SessionKnowledgeInjection
type Envelope = domain.Envelope
type Friend = domain.Friend
type Team = domain.Team
type TeamSummary = domain.TeamSummary
type TeamMember = domain.TeamMember
type TeamInvite = domain.TeamInvite
type TeamAgent = domain.TeamAgent
type TeamMemexDocument = domain.TeamMemexDocument
type TeamMemexRun = domain.TeamMemexRun
type CreateKnowledgeCapsuleRequest = domain.CreateKnowledgeCapsuleRequest
type ListKnowledgeCapsulesFilter = domain.ListKnowledgeCapsulesFilter
type InjectKnowledgeCapsuleRequest = domain.InjectKnowledgeCapsuleRequest
type ListKnowledgeInjectionsFilter = domain.ListKnowledgeInjectionsFilter
type CreateEnvelopeRequest = domain.CreateEnvelopeRequest
type ListEnvelopesFilter = domain.ListEnvelopesFilter
type CreateFriendRequest = domain.CreateFriendRequest
type AcceptFriendRequest = domain.AcceptFriendRequest
type UpdateFriendAliasRequest = domain.UpdateFriendAliasRequest
type ListFriendsFilter = domain.ListFriendsFilter
type CreateTeamRequest = domain.CreateTeamRequest
type CreateTeamInviteRequest = domain.CreateTeamInviteRequest
type AddTeamAgentRequest = domain.AddTeamAgentRequest
type UpdateTeamMemberRoleRequest = domain.UpdateTeamMemberRoleRequest
type ApprovalOption = domain.ApprovalOption
type AgentApproval = domain.AgentApproval
type RegisterNodeRequest = domain.RegisterNodeRequest
type RegisterNodeResponse = domain.RegisterNodeResponse
type NodeRegistrationSession = domain.NodeRegistrationSession
type StartNodeRegistrationRequest = domain.StartNodeRegistrationRequest
type StartNodeRegistrationResponse = domain.StartNodeRegistrationResponse
type PollNodeRegistrationRequest = domain.PollNodeRegistrationRequest
type PollNodeRegistrationResponse = domain.PollNodeRegistrationResponse
type ApproveNodeRegistrationResponse = domain.ApproveNodeRegistrationResponse
type NodeRegistrationPreviewResponse = domain.NodeRegistrationPreviewResponse
type NodeRegistrationNetworkPreview = domain.NodeRegistrationNetworkPreview
type PaxlDeviceLoginSession = domain.PaxlDeviceLoginSession
type StartPaxlDeviceLoginRequest = domain.StartPaxlDeviceLoginRequest
type StartPaxlDeviceLoginResponse = domain.StartPaxlDeviceLoginResponse
type PollPaxlDeviceLoginRequest = domain.PollPaxlDeviceLoginRequest
type PollPaxlDeviceLoginResponse = domain.PollPaxlDeviceLoginResponse
type ApprovePaxlDeviceLoginResponse = domain.ApprovePaxlDeviceLoginResponse
type RegisterNodeAgentRequest = domain.RegisterNodeAgentRequest
type RegisterNodeAgentResponse = domain.RegisterNodeAgentResponse
type RegisterAgentRequest = domain.RegisterAgentRequest
type RegisterAgentResponse = domain.RegisterAgentResponse
type CreateRegistrationTokenRequest = domain.CreateRegistrationTokenRequest
type CreateRegistrationTokenResponse = domain.CreateRegistrationTokenResponse
type UserAPIKey = domain.UserAPIKey
type CreateUserAPIKeyRequest = domain.CreateUserAPIKeyRequest
type CreateUserAPIKeyResponse = domain.CreateUserAPIKeyResponse
type Secret = domain.Secret
type SecretVersion = domain.SecretVersion
type CreateSecretRequest = domain.CreateSecretRequest
type UpdateSecretValueRequest = domain.UpdateSecretValueRequest
type ResolveSecretRequest = domain.ResolveSecretRequest
type ResolveSecretResponse = domain.ResolveSecretResponse
type WriteSecretVersionRequest = domain.WriteSecretVersionRequest
type WriteSecretVersionResponse = domain.WriteSecretVersionResponse
type SecretAccessEvent = domain.SecretAccessEvent
type PaxdArtifact = domain.PaxdArtifact
type CreatePaxdArtifactRequest = domain.CreatePaxdArtifactRequest
type FindPaxdArtifactRequest = domain.FindPaxdArtifactRequest
type PaxdArtifactDownloadResponse = domain.PaxdArtifactDownloadResponse
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
type UpdateNodeRequest = domain.UpdateNodeRequest
type UpdateAgentProfileRequest = domain.UpdateAgentProfileRequest
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
