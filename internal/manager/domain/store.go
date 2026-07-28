package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pax-beehive/paxkit/reliablemq"
)

type ReliableTransportJournal interface {
	AppendOutboundData(
		ctx context.Context,
		queueID string,
		stream reliablemq.Stream,
		payload json.RawMessage,
		metadata reliablemq.Metadata,
	) (reliablemq.Frame, error)
	AppendOutboundTombstone(
		ctx context.Context,
		queueID string,
		stream reliablemq.Stream,
		errorMessage string,
		metadata reliablemq.Metadata,
	) (reliablemq.Frame, error)
	SaveInboundIfAbsent(ctx context.Context, frame reliablemq.Frame) (bool, reliablemq.Frame, error)
	ListOutboundReplay(
		ctx context.Context,
		queueID string,
		stream reliablemq.Stream,
		limit int,
	) ([]reliablemq.Frame, error)
	ListInboundReplay(
		ctx context.Context,
		queueID string,
		stream reliablemq.Stream,
		limit int,
	) ([]reliablemq.Frame, error)
	MarkSent(ctx context.Context, key reliablemq.FrameKey) error
	AckOutboundThrough(
		ctx context.Context,
		queueID string,
		stream reliablemq.Stream,
		throughSeq int64,
	) error
	MarkApplied(ctx context.Context, key reliablemq.FrameKey) error
	MarkRejected(ctx context.Context, key reliablemq.FrameKey, errorMessage string) error
	RecordSendFailure(ctx context.Context, key reliablemq.FrameKey, errorMessage string) error
	RecordDispatchFailure(ctx context.Context, key reliablemq.FrameKey, errorMessage string) error
	UpdateMetadata(ctx context.Context, key reliablemq.FrameKey, metadata reliablemq.Metadata) error
}

type Store interface {
	ReliableTransportJournal

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
	CreateUserAttachment(
		ctx context.Context,
		principal UserPrincipal,
		req CreateUserAttachmentRequest,
		bucket string,
		object string,
		expiresAt time.Time,
	) (UserAttachment, error)
	GetUserAttachment(
		ctx context.Context,
		principal UserPrincipal,
		attachmentID string,
	) (UserAttachment, error)
	CompleteUserAttachment(
		ctx context.Context,
		principal UserPrincipal,
		attachmentID string,
		attrs ArtifactContent,
	) (UserAttachment, error)
	PutArtifactPublication(
		ctx context.Context,
		publication ArtifactPublication,
	) (ArtifactPublication, error)
	GetArtifactPublication(
		ctx context.Context,
		principal UserPrincipal,
		publicationID string,
	) (ArtifactPublication, error)
	FailArtifactPublication(
		ctx context.Context,
		node Node,
		publicationID string,
		code string,
		message string,
	) (ArtifactPublication, error)
	PrepareArtifactPublication(
		ctx context.Context,
		node Node,
		publicationID string,
		req PrepareArtifactPublicationRequest,
		bucket string,
		object string,
		expiresAt time.Time,
	) (ArtifactPublication, ArtifactUpload, error)
	GetNodeArtifactUpload(
		ctx context.Context,
		node Node,
		uploadID string,
	) (ArtifactUpload, error)
	CompleteNodeArtifactUpload(
		ctx context.Context,
		node Node,
		uploadID string,
		attrs ArtifactContent,
	) (ArtifactUpload, SessionArtifact, error)
	CreateArtifactUpload(
		ctx context.Context,
		principal UserPrincipal,
		req CreateArtifactUploadRequest,
		bucket string,
		object string,
		expiresAt time.Time,
	) (ArtifactUpload, error)
	GetArtifactUpload(
		ctx context.Context,
		principal UserPrincipal,
		uploadID string,
	) (ArtifactUpload, error)
	CompleteArtifactUpload(
		ctx context.Context,
		principal UserPrincipal,
		uploadID string,
		attrs ArtifactContent,
		req CompleteArtifactUploadRequest,
	) (ArtifactUpload, SessionArtifact, error)
	CreateSessionArtifact(
		ctx context.Context,
		principal UserPrincipal,
		req CreateSessionArtifactRequest,
	) (SessionArtifact, error)
	GetSessionArtifact(
		ctx context.Context,
		principal UserPrincipal,
		artifactID string,
	) (SessionArtifact, error)
	ListSessionArtifacts(
		ctx context.Context,
		filter ListSessionArtifactsFilter,
	) ([]SessionArtifact, error)
	GetArtifactContent(
		ctx context.Context,
		principal UserPrincipal,
		artifactID string,
		ref string,
	) (SessionArtifact, ArtifactContent, error)
	AttachSessionArtifact(
		ctx context.Context,
		principal UserPrincipal,
		req AttachArtifactRequest,
	) (SessionArtifact, error)
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
	CreatePaxlDeviceLoginSession(ctx context.Context, session PaxlDeviceLoginSession) error
	DeleteStalePaxlDeviceLoginSessions(ctx context.Context, cutoff time.Time) error
	ApprovePaxlDeviceLoginSession(
		ctx context.Context,
		principal UserPrincipal,
		userCode string,
		userAPIKey UserAPIKey,
		apiKey string,
	) (PaxlDeviceLoginSession, error)
	PollPaxlDeviceLoginSession(
		ctx context.Context,
		loginID string,
		pollTokenHash string,
	) (PaxlDeviceLoginSession, error)
	ConsumePaxlDeviceLoginSession(
		ctx context.Context,
		loginID string,
		pollTokenHash string,
	) (PaxlDeviceLoginSession, error)
	AuthenticateNode(ctx context.Context, apiKeyHash string) (Node, error)
	UpsertNodeStatus(ctx context.Context, node Node, report NodeStatusReport) error
	UpsertAgentSessions(
		ctx context.Context,
		node Node,
		agentID string,
		sessions []SessionStatusInput,
	) error
	ListNodes(ctx context.Context, principal UserPrincipal) ([]Node, error)
	GetNode(ctx context.Context, principal UserPrincipal, nodeID string) (Node, error)
	UpdateNode(ctx context.Context, principal UserPrincipal, req UpdateNodeRequest) (Node, error)
	DeleteNode(ctx context.Context, principal UserPrincipal, req DeleteNodeRequest) (Node, error)
	GetNodeAgent(ctx context.Context, nodeID string, agentID string) (Agent, error)
	ListNodeAgents(ctx context.Context, principal UserPrincipal, nodeID string) ([]Agent, error)
	CreateNodeAgent(
		ctx context.Context,
		principal UserPrincipal,
		req CreateAgentRequest,
	) (Agent, MailboxMessage, error)
	UpdateNodeAgent(
		ctx context.Context,
		principal UserPrincipal,
		req UpdateAgentProfileRequest,
	) (Agent, error)
	DeleteNodeAgent(
		ctx context.Context,
		principal UserPrincipal,
		req DeleteAgentRequest,
	) (Agent, error)
	CreateNodeAgentSession(
		ctx context.Context,
		principal UserPrincipal,
		req CreateSessionRequest,
	) (AgentSession, error)
	UpdateNodeAgentSession(
		ctx context.Context,
		principal UserPrincipal,
		req UpdateSessionRequest,
	) (AgentSession, error)
	// LinkAgentSessionNativeID records the native ACP session id on a session
	// row when it is not already set. A2A delivery creates the row before the
	// ACP session/new call, so without this link session_id <-> native_id
	// translation fails and paxd status reports create a duplicate row instead
	// of updating this one.
	LinkAgentSessionNativeID(ctx context.Context, agentID, sessionID, nativeID string) error
	UpsertAgentStatus(ctx context.Context, report AgentStatusReport) error
	NextAgentACPRequestID(ctx context.Context, agentID string) (int64, error)
	ListAgents(ctx context.Context, principal UserPrincipal) ([]Agent, error)
	ListOwnerAgents(
		ctx context.Context,
		ownerUserID string,
		filter OwnerAgentFilter,
	) ([]Agent, error)
	GetAgent(ctx context.Context, principal UserPrincipal, agentID string) (Agent, error)
	DeleteAgent(ctx context.Context, principal UserPrincipal, req DeleteAgentRequest) (Agent, error)
	ListAgentSessions(
		ctx context.Context,
		principal UserPrincipal,
		agentID string,
	) ([]AgentSession, error)
	ListSessions(
		ctx context.Context,
		principal UserPrincipal,
		filter ListSessionsFilter,
	) (ListSessionsResult, error)
	GetSession(ctx context.Context, principal UserPrincipal, sessionID string) (AgentSession, error)
	UpdateSessionRuntimeState(ctx context.Context, state SessionRuntimeState) error
	ListRepresentativeAgents(
		ctx context.Context,
		principal UserPrincipal,
		runtimeAgentID string,
	) ([]RepresentativeAgent, error)
	GetAgentOwnerInfo(
		ctx context.Context,
		principal UserPrincipal,
		req AgentOwnerInfoRequest,
	) (AgentOwnerInfo, error)
	UpsertRepresentativeAgent(
		ctx context.Context,
		principal UserPrincipal,
		req UpsertRepresentativeAgentRequest,
	) (RepresentativeAgent, AgentProfile, error)
	StartAgentConversation(
		ctx context.Context,
		node Node,
		req StartAgentConversationRequest,
	) (AgentConversationStart, error)
	StartAgentConversationForUser(
		ctx context.Context,
		principal UserPrincipal,
		req StartAgentConversationRequest,
	) (AgentConversationStart, error)
	DeliverAgentConversation(
		ctx context.Context,
		node Node,
		req DeliverConversationRequest,
	) (ConversationDelivery, error)
	CompleteAgentConversationInvocation(ctx context.Context, invocationID string) error
	ListConversationMessages(
		ctx context.Context,
		principal UserPrincipal,
		conversationID string,
		limit int,
	) ([]MessageWithParts, error)
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
	RecordApprovalResponse(
		ctx context.Context,
		principal UserPrincipal,
		approvalID string,
		responseBody json.RawMessage,
		responseError string,
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
	UpsertAuditEvent(ctx context.Context, event AgentAuditEvent) (AgentAuditEvent, error)
	ListAuditEvents(ctx context.Context, filter AuditEventFilter) ([]AgentAuditEvent, error)
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
	ListMessageHistoryPage(
		ctx context.Context,
		agentID string,
		sessionID string,
		beforeID int64,
		limit int,
	) (MessageHistoryPage, error)
	ListMessageParts(ctx context.Context, messageID string) ([]MessagePart, error)
	ListMessagePartsByMessageIDs(
		ctx context.Context,
		messageIDs []string,
	) (map[string][]MessagePart, error)
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
	CreateEnvelope(ctx context.Context, envelope Envelope) (Envelope, error)
	GetEnvelopeAgentRecipient(
		ctx context.Context,
		principal UserPrincipal,
		fromAgentID string,
		toAgentID string,
	) (User, error)
	ListEnvelopes(ctx context.Context, filter ListEnvelopesFilter) ([]Envelope, error)
	GetEnvelope(ctx context.Context, principal UserPrincipal, envelopeID string) (Envelope, error)
	AcceptEnvelope(
		ctx context.Context,
		principal UserPrincipal,
		envelopeID string,
		acceptedAt time.Time,
	) (Envelope, error)
	ArchiveEnvelope(
		ctx context.Context,
		principal UserPrincipal,
		envelopeID string,
		archivedAt time.Time,
	) (Envelope, error)
	CreateFriend(ctx context.Context, friend Friend) (Friend, error)
	ListFriends(ctx context.Context, filter ListFriendsFilter) ([]Friend, error)
	GetAcceptedFriendByEmail(
		ctx context.Context,
		principal UserPrincipal,
		email string,
	) (Friend, error)
	ListFriendsBetween(
		ctx context.Context,
		principal UserPrincipal,
		email string,
	) ([]Friend, error)
	DeleteRemovedFriendsBetween(
		ctx context.Context,
		principal UserPrincipal,
		email string,
	) error
	GetFriend(ctx context.Context, principal UserPrincipal, friendID string) (Friend, error)
	AcceptFriend(
		ctx context.Context,
		principal UserPrincipal,
		friendID string,
		alias string,
		acceptedAt time.Time,
	) (Friend, error)
	UpdateFriendAlias(
		ctx context.Context,
		principal UserPrincipal,
		friendID string,
		alias string,
	) (Friend, error)
	RemoveFriend(
		ctx context.Context,
		principal UserPrincipal,
		friendID string,
		removedAt time.Time,
	) (Friend, error)
	BlockFriend(
		ctx context.Context,
		principal UserPrincipal,
		friendID string,
		blockedAt time.Time,
	) (Friend, error)
	CreateTeam(ctx context.Context, team Team, owner TeamMember) (Team, error)
	ListTeams(ctx context.Context, principal UserPrincipal) ([]TeamSummary, error)
	GetTeam(ctx context.Context, principal UserPrincipal, teamID string) (Team, error)
	ListTeamMembers(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
	) ([]TeamMember, error)
	ListTeamAgents(ctx context.Context, principal UserPrincipal, teamID string) ([]TeamAgent, error)
	ListTeamAuditEvents(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		limit int,
	) ([]TeamAuditEvent, error)
	ListTeamMemexDocuments(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
	) ([]TeamMemexDocument, error)
	ListTeamMemexDocumentPaths(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
	) ([]string, error)
	AuthorizeTeamMemexRun(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
	) error
	GetTeamMemexDocument(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		path string,
	) (TeamMemexDocument, error)
	CreateTeamMemexRun(
		ctx context.Context,
		principal UserPrincipal,
		run TeamMemexRun,
	) (TeamMemexRun, error)
	GetTeamMemexRun(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		runID string,
	) (TeamMemexRun, error)
	PublishTeamMemexRun(
		ctx context.Context,
		principal UserPrincipal,
		run TeamMemexRun,
		operations []TeamMemexDocumentOperation,
		now time.Time,
	) (TeamMemexRun, error)
	CreateTeamInvite(
		ctx context.Context,
		principal UserPrincipal,
		invite TeamInvite,
	) (TeamInvite, error)
	ListTeamInvites(ctx context.Context, principal UserPrincipal) ([]TeamInvite, error)
	ListTeamSentInvites(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
	) ([]TeamInvite, error)
	AcceptTeamInvite(
		ctx context.Context,
		principal UserPrincipal,
		inviteID string,
		acceptedAt time.Time,
	) (TeamInvite, error)
	DeclineTeamInvite(
		ctx context.Context,
		principal UserPrincipal,
		inviteID string,
		declinedAt time.Time,
	) (TeamInvite, error)
	CancelTeamInvite(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		inviteID string,
		canceledAt time.Time,
	) (TeamInvite, error)
	AddTeamAgent(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		req AddTeamAgentRequest,
		addedAt time.Time,
	) (TeamAgent, error)
	RemoveTeamAgent(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		agentID string,
		removedAt time.Time,
	) (TeamAgent, error)
	RemoveTeamMember(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		userID string,
		removedAt time.Time,
	) (TeamMember, error)
	UpdateTeamMemberRole(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		userID string,
		role string,
		updatedAt time.Time,
	) (TeamMember, error)
	ArchiveTeam(
		ctx context.Context,
		principal UserPrincipal,
		teamID string,
		archivedAt time.Time,
	) (Team, error)
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
