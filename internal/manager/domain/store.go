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
	UpdateOffset(ctx context.Context, agentID string, offset int64) error
	UpdateNodeOffset(ctx context.Context, nodeID string, offset int64) error
}
