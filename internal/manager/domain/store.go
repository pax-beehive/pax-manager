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
	UpsertAgentStatus(ctx context.Context, report AgentStatusReport) error
	ListAgents(ctx context.Context, principal UserPrincipal) ([]Agent, error)
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
	PullMailbox(ctx context.Context, agentID string, offset int64, limit int) (MailboxPull, error)
	MarkMessageResult(
		ctx context.Context,
		agentID string,
		messageID string,
		req MessageResultRequest,
	) error
	UpdateOffset(ctx context.Context, agentID string, offset int64) error
}
