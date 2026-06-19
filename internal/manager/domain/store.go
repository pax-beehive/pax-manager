package domain

import (
	"context"
	"time"
)

type Store interface {
	EnsureUser(ctx context.Context, email string, displayName string, role string) (User, error)
	GetUser(ctx context.Context, userID string) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	CreateRegistrationToken(
		ctx context.Context,
		ownerUserID string,
		tokenHash string,
		expiresAt *time.Time,
	) error
	ResolveRegistrationToken(ctx context.Context, tokenHash string) (User, error)
	CreateUserAPIKey(
		ctx context.Context,
		principal UserPrincipal,
		name string,
		keyHash string,
		prefix string,
	) (UserAPIKey, error)
	ListUserAPIKeys(ctx context.Context, principal UserPrincipal) ([]UserAPIKey, error)
	RevokeUserAPIKey(ctx context.Context, principal UserPrincipal, keyID string) error
	AuthenticateUserAPIKey(ctx context.Context, keyHash string) (User, error)
	CreateSecret(
		ctx context.Context,
		principal UserPrincipal,
		req CreateSecretRequest,
		encrypted SecretVersion,
	) (Secret, SecretVersion, error)
	ListSecrets(ctx context.Context, principal UserPrincipal) ([]Secret, error)
	GetSecret(ctx context.Context, principal UserPrincipal, secretID string) (Secret, error)
	GetSecretVersionForNode(
		ctx context.Context,
		node Node,
		agentID string,
		secretID string,
		versionSelector string,
	) (Secret, SecretVersion, error)
	CreateSecretVersion(
		ctx context.Context,
		node Node,
		agentID string,
		req WriteSecretVersionRequest,
		encrypted SecretVersion,
	) (SecretVersion, bool, error)
	RecordSecretAccess(ctx context.Context, event SecretAccessEvent) error
	CreatePaxdArtifact(
		ctx context.Context,
		req CreatePaxdArtifactRequest,
		createdBy string,
	) (PaxdArtifact, error)
	FindPaxdArtifact(ctx context.Context, req FindPaxdArtifactRequest) (PaxdArtifact, error)
	RegisterAgent(
		ctx context.Context,
		owner User,
		req RegisterAgentRequest,
		apiKeyHash string,
	) (Agent, error)
	AuthenticateAgent(ctx context.Context, apiKeyHash string) (Agent, error)
	RegisterNode(
		ctx context.Context,
		owner User,
		req RegisterNodeRequest,
		apiKeyHash string,
	) (Node, error)
	CreateNodeRegistrationSession(ctx context.Context, session NodeRegistrationSession) error
	DeleteStaleNodeRegistrationSessions(ctx context.Context, cutoff time.Time) error
	GetNodeRegistrationSession(
		ctx context.Context,
		pairCode string,
	) (NodeRegistrationSession, error)
	ApproveNodeRegistrationSession(
		ctx context.Context,
		principal UserPrincipal,
		pairCode string,
	) (NodeRegistrationSession, error)
	PollNodeRegistrationSession(
		ctx context.Context,
		registrationID string,
		pollTokenHash string,
	) (NodeRegistrationSession, error)
	ConsumeNodeRegistrationSession(
		ctx context.Context,
		registrationID string,
		pollTokenHash string,
		apiKeyHash string,
	) (Node, error)
	AuthenticateNode(ctx context.Context, apiKeyHash string) (Node, error)
	UpsertNodeStatus(ctx context.Context, node Node, report NodeStatusReport) error
	ListNodes(ctx context.Context, principal UserPrincipal) ([]Node, error)
	GetNode(ctx context.Context, principal UserPrincipal, nodeID string) (Node, error)
	GetNodeAgent(ctx context.Context, nodeID string, agentID string) (Agent, error)
	ListNodeAgents(ctx context.Context, principal UserPrincipal, nodeID string) ([]Agent, error)
	CreateNodeAgent(
		ctx context.Context,
		principal UserPrincipal,
		req CreateAgentRequest,
	) (Agent, MailboxMessage, error)
	CreateNodeAgentSession(
		ctx context.Context,
		principal UserPrincipal,
		req CreateSessionRequest,
	) (AgentSession, error)
	UpsertAgentStatus(ctx context.Context, report AgentStatusReport) error
	ListAgents(ctx context.Context, principal UserPrincipal) ([]Agent, error)
	GetAgent(ctx context.Context, principal UserPrincipal, agentID string) (Agent, error)
	ListAgentSessions(
		ctx context.Context,
		principal UserPrincipal,
		agentID string,
	) ([]AgentSession, error)
	GetSession(ctx context.Context, principal UserPrincipal, sessionID string) (AgentSession, error)
	UpdateSessionRuntimeState(ctx context.Context, state SessionRuntimeState) error
	ListSessionMessages(
		ctx context.Context,
		principal UserPrincipal,
		sessionID string,
	) ([]MailboxMessage, error)
	CreateMailboxMessage(
		ctx context.Context,
		principal UserPrincipal,
		req CreateMailboxRequest,
	) (MailboxMessage, error)
	CreateApproval(ctx context.Context, node Node, req CreateApprovalRequest) (AgentApproval, error)
	GetNodeApproval(
		ctx context.Context,
		node Node,
		agentID string,
		approvalID string,
	) (AgentApproval, error)
	GetApproval(
		ctx context.Context,
		principal UserPrincipal,
		approvalID string,
	) (AgentApproval, error)
	ListApprovals(ctx context.Context, filter ApprovalFilter) ([]AgentApproval, error)
	DecideApproval(
		ctx context.Context,
		principal UserPrincipal,
		approvalID string,
		req ApprovalDecisionRequest,
	) (AgentApproval, error)
	ListApprovalGrants(ctx context.Context, filter ApprovalGrantFilter) ([]AgentApproval, error)
	FindReusableApprovalGrant(
		ctx context.Context,
		lookup ApprovalGrantLookup,
	) (AgentApproval, error)
	RevokeApprovalGrant(
		ctx context.Context,
		principal UserPrincipal,
		grantID string,
		req RevokeApprovalGrantRequest,
	) (AgentApproval, error)
	ListMailbox(ctx context.Context, filter MailboxFilter) ([]MailboxMessage, error)
	PullMailbox(
		ctx context.Context,
		agentID string,
		sessionID string,
		offset int64,
		limit int,
	) (MailboxPull, error)
	PullNodeMailbox(
		ctx context.Context,
		nodeID string,
		agentID string,
		sessionID string,
		offset int64,
		limit int,
	) (MailboxPull, error)
	MarkMessageResult(
		ctx context.Context,
		agentID string,
		messageID string,
		req MessageResultRequest,
	) error
	MarkNodeMessageResult(
		ctx context.Context,
		nodeID string,
		messageID string,
		req MessageResultRequest,
	) error
	MarkNodeMessageDelivered(
		ctx context.Context,
		nodeID string,
		req MarkDeliveredRequest,
	) error
	CreateNodeOutboundMessage(
		ctx context.Context,
		node Node,
		req CreateOutboundMessageRequest,
	) (MailboxMessage, error)
	UpsertMessage(ctx context.Context, msg *Message) error
	UpsertMessagePart(ctx context.Context, part *MessagePart) error
	ListMessages(
		ctx context.Context,
		agentID string,
		sessionID string,
		limit int,
	) ([]Message, error)
	ListMessageParts(ctx context.Context, messageID string) ([]MessagePart, error)
	CreateKnowledgeCapsule(ctx context.Context, capsule KnowledgeCapsule) (KnowledgeCapsule, error)
	ListKnowledgeCapsules(
		ctx context.Context,
		filter ListKnowledgeCapsulesFilter,
	) ([]KnowledgeCapsule, error)
	GetKnowledgeCapsule(
		ctx context.Context,
		principal UserPrincipal,
		capsuleID string,
	) (KnowledgeCapsule, error)
	ArchiveKnowledgeCapsule(
		ctx context.Context,
		principal UserPrincipal,
		capsuleID string,
		archivedAt time.Time,
	) (KnowledgeCapsule, error)
	CreateKnowledgeInjection(
		ctx context.Context,
		injection SessionKnowledgeInjection,
	) (SessionKnowledgeInjection, error)
	ListKnowledgeInjections(
		ctx context.Context,
		filter ListKnowledgeInjectionsFilter,
	) ([]SessionKnowledgeInjection, error)
	AppendMessagePartText(
		ctx context.Context,
		messageID string,
		partIndex int,
		delta string,
		payloadJSON []byte,
	) error
	SaveTransportFrame(ctx context.Context, frame *TransportFrame) error
	SaveTransportFrameIfAbsent(ctx context.Context, frame *TransportFrame) (bool, error)
	NextTransportSeq(
		ctx context.Context,
		agentID string,
		stream string,
		direction string,
	) (int64, error)
	GetTransportFrame(
		ctx context.Context,
		agentID string,
		stream string,
		seq int64,
		direction string,
	) (*TransportFrame, error)
	ListTransportFrames(
		ctx context.Context,
		agentID string,
		stream string,
		direction string,
		statuses []string,
		limit int,
	) ([]TransportFrame, error)
	UpdateTransportFrameStatus(
		ctx context.Context,
		agentID string,
		stream string,
		seq int64,
		direction string,
		status string,
		errMsg string,
	) error
	AckOutboundTransportFrames(
		ctx context.Context,
		agentID string,
		stream string,
		throughSeq int64,
	) error
	DeleteCompletedTransportFrames(ctx context.Context, cutoff time.Time, limit int) (int64, error)
	UpdateOffset(ctx context.Context, agentID string, offset int64) error
	UpdateNodeOffset(ctx context.Context, nodeID string, offset int64) error
}
