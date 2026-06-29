package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	vaultsecrets "github.com/pax-beehive/pax-manager/internal/manager/secrets"
)

type Store interface {
	UserStore
	UserAPIKeyStore
	RegistrationTokenStore
	SecretStore
	FleetStore
	SessionHistoryStore
	KnowledgeStore
	EnvelopeStore
	FriendStore
	TeamStore
	TeamMemexStore
	MailboxStore
}

type UserStore interface {
	GetUser(ctx context.Context, userID string) (domain.User, error)
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
}

type RegistrationTokenStore interface {
	CreateRegistrationToken(
		ctx context.Context,
		ownerUserID string,
		tokenHash string,
		expiresAt *time.Time,
	) error
}

type UserAPIKeyStore interface {
	CreateUserAPIKey(
		ctx context.Context,
		principal domain.UserPrincipal,
		name string,
		keyHash string,
		prefix string,
	) (domain.UserAPIKey, error)
	ListUserAPIKeys(
		ctx context.Context,
		principal domain.UserPrincipal,
	) ([]domain.UserAPIKey, error)
	RevokeUserAPIKey(ctx context.Context, principal domain.UserPrincipal, keyID string) error
}

type SecretStore interface {
	CreateSecret(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.CreateSecretRequest,
		encrypted domain.SecretVersion,
	) (domain.Secret, domain.SecretVersion, error)
	ListSecrets(ctx context.Context, principal domain.UserPrincipal) ([]domain.Secret, error)
	GetSecret(
		ctx context.Context,
		principal domain.UserPrincipal,
		secretID string,
	) (domain.Secret, error)
}

type FleetStore interface {
	ListAgents(ctx context.Context, principal domain.UserPrincipal) ([]domain.Agent, error)
	ListNodes(ctx context.Context, principal domain.UserPrincipal) ([]domain.Node, error)
	GetNode(ctx context.Context, principal domain.UserPrincipal, nodeID string) (domain.Node, error)
	UpdateNode(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.UpdateNodeRequest,
	) (domain.Node, error)
	ListNodeAgents(
		ctx context.Context,
		principal domain.UserPrincipal,
		nodeID string,
	) ([]domain.Agent, error)
	CreateNodeAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.CreateAgentRequest,
	) (domain.Agent, domain.MailboxMessage, error)
	UpdateNodeAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.UpdateAgentProfileRequest,
	) (domain.Agent, error)
	CreateNodeAgentSession(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.CreateSessionRequest,
	) (domain.AgentSession, error)
	GetAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		agentID string,
	) (domain.Agent, error)
	ListAgentSessions(
		ctx context.Context,
		principal domain.UserPrincipal,
		agentID string,
	) ([]domain.AgentSession, error)
	GetSession(
		ctx context.Context,
		principal domain.UserPrincipal,
		sessionID string,
	) (domain.AgentSession, error)
	ListSessionMessages(
		ctx context.Context,
		principal domain.UserPrincipal,
		sessionID string,
	) ([]domain.MailboxMessage, error)
}

type SessionHistoryStore interface {
	ListMessages(
		ctx context.Context,
		agentID string,
		sessionID string,
		limit int,
	) ([]domain.Message, error)
	ListMessageParts(ctx context.Context, messageID string) ([]domain.MessagePart, error)
}

type KnowledgeStore interface {
	CreateKnowledgeCapsule(
		ctx context.Context,
		capsule domain.KnowledgeCapsule,
	) (domain.KnowledgeCapsule, error)
	ListKnowledgeCapsules(
		ctx context.Context,
		filter domain.ListKnowledgeCapsulesFilter,
	) ([]domain.KnowledgeCapsule, error)
	GetKnowledgeCapsule(
		ctx context.Context,
		principal domain.UserPrincipal,
		capsuleID string,
	) (domain.KnowledgeCapsule, error)
	ArchiveKnowledgeCapsule(
		ctx context.Context,
		principal domain.UserPrincipal,
		capsuleID string,
		archivedAt time.Time,
	) (domain.KnowledgeCapsule, error)
	CreateKnowledgeInjection(
		ctx context.Context,
		injection domain.SessionKnowledgeInjection,
	) (domain.SessionKnowledgeInjection, error)
	ListKnowledgeInjections(
		ctx context.Context,
		filter domain.ListKnowledgeInjectionsFilter,
	) ([]domain.SessionKnowledgeInjection, error)
}

type EnvelopeStore interface {
	CreateEnvelope(ctx context.Context, envelope domain.Envelope) (domain.Envelope, error)
	GetEnvelopeAgentRecipient(
		ctx context.Context,
		principal domain.UserPrincipal,
		fromAgentID string,
		toAgentID string,
	) (domain.User, error)
	ListEnvelopes(
		ctx context.Context,
		filter domain.ListEnvelopesFilter,
	) ([]domain.Envelope, error)
	GetEnvelope(
		ctx context.Context,
		principal domain.UserPrincipal,
		envelopeID string,
	) (domain.Envelope, error)
	AcceptEnvelope(
		ctx context.Context,
		principal domain.UserPrincipal,
		envelopeID string,
		acceptedAt time.Time,
	) (domain.Envelope, error)
	ArchiveEnvelope(
		ctx context.Context,
		principal domain.UserPrincipal,
		envelopeID string,
		archivedAt time.Time,
	) (domain.Envelope, error)
}

type FriendStore interface {
	CreateFriend(ctx context.Context, friend domain.Friend) (domain.Friend, error)
	ListFriends(
		ctx context.Context,
		filter domain.ListFriendsFilter,
	) ([]domain.Friend, error)
	GetAcceptedFriendByEmail(
		ctx context.Context,
		principal domain.UserPrincipal,
		email string,
	) (domain.Friend, error)
	GetFriend(
		ctx context.Context,
		principal domain.UserPrincipal,
		friendID string,
	) (domain.Friend, error)
	AcceptFriend(
		ctx context.Context,
		principal domain.UserPrincipal,
		friendID string,
		alias string,
		acceptedAt time.Time,
	) (domain.Friend, error)
	UpdateFriendAlias(
		ctx context.Context,
		principal domain.UserPrincipal,
		friendID string,
		alias string,
	) (domain.Friend, error)
	RemoveFriend(
		ctx context.Context,
		principal domain.UserPrincipal,
		friendID string,
		removedAt time.Time,
	) (domain.Friend, error)
	BlockFriend(
		ctx context.Context,
		principal domain.UserPrincipal,
		friendID string,
		blockedAt time.Time,
	) (domain.Friend, error)
}

type TeamStore interface {
	CreateTeam(
		ctx context.Context,
		team domain.Team,
		owner domain.TeamMember,
	) (domain.Team, error)
	ListTeams(ctx context.Context, principal domain.UserPrincipal) ([]domain.TeamSummary, error)
	GetTeam(ctx context.Context, principal domain.UserPrincipal, teamID string) (domain.Team, error)
	ListTeamMembers(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
	) ([]domain.TeamMember, error)
	ListTeamAgents(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
	) ([]domain.TeamAgent, error)
	ListTeamAuditEvents(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		limit int,
	) ([]domain.TeamAuditEvent, error)
	CreateTeamInvite(
		ctx context.Context,
		principal domain.UserPrincipal,
		invite domain.TeamInvite,
	) (domain.TeamInvite, error)
	ListTeamInvites(
		ctx context.Context,
		principal domain.UserPrincipal,
	) ([]domain.TeamInvite, error)
	ListTeamSentInvites(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
	) ([]domain.TeamInvite, error)
	AcceptTeamInvite(
		ctx context.Context,
		principal domain.UserPrincipal,
		inviteID string,
		acceptedAt time.Time,
	) (domain.TeamInvite, error)
	DeclineTeamInvite(
		ctx context.Context,
		principal domain.UserPrincipal,
		inviteID string,
		declinedAt time.Time,
	) (domain.TeamInvite, error)
	CancelTeamInvite(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		inviteID string,
		canceledAt time.Time,
	) (domain.TeamInvite, error)
	AddTeamAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		req domain.AddTeamAgentRequest,
		addedAt time.Time,
	) (domain.TeamAgent, error)
	RemoveTeamAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		agentID string,
		removedAt time.Time,
	) (domain.TeamAgent, error)
	RemoveTeamMember(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		userID string,
		removedAt time.Time,
	) (domain.TeamMember, error)
	UpdateTeamMemberRole(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		userID string,
		role string,
		updatedAt time.Time,
	) (domain.TeamMember, error)
	ArchiveTeam(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		archivedAt time.Time,
	) (domain.Team, error)
}

type TeamMemexStore interface {
	ListTeamMemexDocuments(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
	) ([]domain.TeamMemexDocument, error)
	ListTeamMemexDocumentPaths(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
	) ([]string, error)
	AuthorizeTeamMemexRun(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
	) error
	GetTeamMemexDocument(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		path string,
	) (domain.TeamMemexDocument, error)
	CreateTeamMemexRun(
		ctx context.Context,
		principal domain.UserPrincipal,
		run domain.TeamMemexRun,
	) (domain.TeamMemexRun, error)
	GetTeamMemexRun(
		ctx context.Context,
		principal domain.UserPrincipal,
		teamID string,
		runID string,
	) (domain.TeamMemexRun, error)
	PublishTeamMemexRun(
		ctx context.Context,
		principal domain.UserPrincipal,
		run domain.TeamMemexRun,
		operations []domain.TeamMemexDocumentOperation,
		now time.Time,
	) (domain.TeamMemexRun, error)
}

type MailboxStore interface {
	CreateMailboxMessage(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.CreateMailboxRequest,
	) (domain.MailboxMessage, error)
	ListMailbox(ctx context.Context, filter domain.MailboxFilter) ([]domain.MailboxMessage, error)
}

type PrincipalResolver interface {
	Principal(context.Context, auth.RequestMetadata) (domain.UserPrincipal, error)
}

type SecretIssuer interface {
	New(prefix string) (string, error)
	Hash(secret string) string
	Prefix(secret string) string
}

type Service struct {
	store            Store
	clock            func() time.Time
	principal        PrincipalResolver
	secrets          SecretIssuer
	vault            *vaultsecrets.Cipher
	backgroundRunner func(context.Context, func(context.Context))
	memexExecutor    TeamMemexExecutor
}

func NewService(
	store Store,
	clock func() time.Time,
	principal PrincipalResolver,
	secrets SecretIssuer,
	vault ...*vaultsecrets.Cipher,
) *Service {
	return NewServiceWithBackgroundRunner(
		store,
		clock,
		principal,
		secrets,
		func(ctx context.Context, task func(context.Context)) {
			go task(context.WithoutCancel(ctx))
		},
		vault...,
	)
}

func NewServiceWithBackgroundRunner(
	store Store,
	clock func() time.Time,
	principal PrincipalResolver,
	secrets SecretIssuer,
	backgroundRunner func(context.Context, func(context.Context)),
	vault ...*vaultsecrets.Cipher,
) *Service {
	cipher := firstVaultCipher(vault)
	if backgroundRunner == nil {
		backgroundRunner = func(ctx context.Context, task func(context.Context)) {
			go task(context.WithoutCancel(ctx))
		}
	}
	return &Service{
		store:            store,
		clock:            clock,
		principal:        principal,
		secrets:          secrets,
		vault:            cipher,
		backgroundRunner: backgroundRunner,
		memexExecutor:    dryRunTeamMemexExecutor{},
	}
}

func (s *Service) SetTeamMemexExecutor(executor TeamMemexExecutor) {
	if executor == nil {
		s.memexExecutor = dryRunTeamMemexExecutor{}
		return
	}
	s.memexExecutor = executor
}

func firstVaultCipher(values []*vaultsecrets.Cipher) *vaultsecrets.Cipher {
	if len(values) > 0 && values[0] != nil {
		return values[0]
	}
	cipher, err := vaultsecrets.NewCipherFromEnv()
	if err != nil {
		panic(err)
	}
	return cipher
}

func (s *Service) CreateSecret(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateSecretRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "name is required"}
	}
	if req.Value == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "value is required"}
	}
	encrypted, err := s.vault.Encrypt(req.Value, nil)
	if err != nil {
		return 0, nil, err
	}
	secret, version, err := s.store.CreateSecret(c, principal, req, domain.SecretVersion{
		Ciphertext: encrypted.Ciphertext,
		Nonce:      encrypted.Nonce,
		KeyID:      encrypted.KeyID,
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"secret": secret, "version": version}, nil
}

func (s *Service) ListSecrets(
	c context.Context,
	meta auth.RequestMetadata,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	secrets, err := s.store.ListSecrets(c, principal)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"secrets": secrets}, nil
}

func (s *Service) GetSecret(
	c context.Context,
	meta auth.RequestMetadata,
	secretID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	secret, err := s.store.GetSecret(c, principal, secretID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"secret": secret}, nil
}

func (s *Service) ListAgents(c context.Context, meta auth.RequestMetadata) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	agents, err := s.store.ListAgents(c, principal)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"agents": agents}, nil
}

func (s *Service) CurrentUser(c context.Context, meta auth.RequestMetadata) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{
		"user": map[string]any{
			"user_id":      principal.User.UserID,
			"email":        principal.User.Email,
			"name":         principal.User.DisplayName,
			"role":         principal.User.Role,
			"is_admin":     principal.IsAdmin,
			"created_at":   principal.User.CreatedAt,
			"last_seen_at": principal.User.LastSeenAt,
		},
	}, nil
}

func (s *Service) ListNodes(c context.Context, meta auth.RequestMetadata) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	nodes, err := s.store.ListNodes(c, principal)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"nodes": nodes}, nil
}

func (s *Service) GetNode(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if nodeID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "node_id is required"}
	}
	node, err := s.store.GetNode(c, principal, nodeID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, node, nil
}

func (s *Service) UpdateNode(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.UpdateNodeRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if req.NodeID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "node_id is required"}
	}
	node, err := s.store.UpdateNode(c, principal, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, node, nil
}

func (s *Service) ListNodeAgents(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	agents, err := s.store.ListNodeAgents(c, principal, nodeID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"agents": agents}, nil
}

func (s *Service) CreateNodeAgent(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateAgentRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if req.NodeID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "node_id is required"}
	}
	agent, bootstrap, err := s.store.CreateNodeAgent(c, principal, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"agent": agent, "bootstrap_message": bootstrap}, nil
}

func (s *Service) UpdateNodeAgent(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.UpdateAgentProfileRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if req.NodeID == "" || req.AgentID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id and agent_id are required",
		}
	}
	agent, err := s.store.UpdateNodeAgent(c, principal, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, agent, nil
}

func (s *Service) CreateNodeAgentSession(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateSessionRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if req.NodeID == "" || req.AgentID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id and agent_id are required",
		}
	}
	session, err := s.store.CreateNodeAgentSession(c, principal, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, session, nil
}

func (s *Service) GetAgent(
	c context.Context,
	meta auth.RequestMetadata,
	agentID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if agentID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id is required",
		}
	}
	agent, err := s.store.GetAgent(c, principal, agentID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, agent, nil
}

func (s *Service) GetNodeAgent(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	agentID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if nodeID == "" || agentID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id and agent_id are required",
		}
	}
	agent, err := s.nodeAgentTarget(c, principal, nodeID, agentID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, agent, nil
}

func (s *Service) ListAgentSessions(
	c context.Context,
	meta auth.RequestMetadata,
	agentID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	sessions, err := s.store.ListAgentSessions(c, principal, agentID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"sessions": sessions}, nil
}

func (s *Service) ListNodeAgentSessions(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	agentID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if nodeID == "" || agentID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id and agent_id are required",
		}
	}
	if _, err := s.nodeAgentTarget(c, principal, nodeID, agentID); err != nil {
		return 0, nil, err
	}
	sessions, err := s.store.ListAgentSessions(c, principal, agentID)
	if err != nil {
		return 0, nil, err
	}
	sessions = filterNodeSessions(sessions, nodeID)
	return http.StatusOK, map[string]any{"sessions": sessions}, nil
}

func (s *Service) GetAgentSession(
	c context.Context,
	meta auth.RequestMetadata,
	agentID string,
	sessionID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if agentID == "" || sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id and session_id are required",
		}
	}
	session, err := s.sessionTarget(c, principal, agentID, sessionID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, session, nil
}

func (s *Service) GetNodeAgentSession(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	agentID string,
	sessionID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if nodeID == "" || agentID == "" || sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, agent_id, and session_id are required",
		}
	}
	session, err := s.nodeSessionTarget(c, principal, nodeID, agentID, sessionID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, session, nil
}

func (s *Service) ListAgentSessionMessages(
	c context.Context,
	meta auth.RequestMetadata,
	agentID string,
	sessionID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if agentID == "" || sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id and session_id are required",
		}
	}
	if _, err := s.sessionTarget(c, principal, agentID, sessionID); err != nil {
		return 0, nil, err
	}
	messages, err := s.store.ListSessionMessages(c, principal, sessionID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"messages": messages}, nil
}

func (s *Service) ListNodeAgentSessionMessages(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	agentID string,
	sessionID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if nodeID == "" || agentID == "" || sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, agent_id, and session_id are required",
		}
	}
	if _, err := s.nodeSessionTarget(c, principal, nodeID, agentID, sessionID); err != nil {
		return 0, nil, err
	}
	messages, err := s.store.ListSessionMessages(c, principal, sessionID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"messages": messages}, nil
}

func (s *Service) ListAgentSessionHistory(
	c context.Context,
	meta auth.RequestMetadata,
	agentID string,
	sessionID string,
	limit int,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if agentID == "" || sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id and session_id are required",
		}
	}
	session, err := s.sessionTarget(c, principal, agentID, sessionID)
	if err == nil {
		sessionID = session.SessionID
	} else if !errors.Is(err, domain.ErrNotFound) {
		return 0, nil, err
	} else {
		if _, agentErr := s.store.GetAgent(c, principal, agentID); agentErr != nil {
			return 0, nil, agentErr
		}
	}
	return s.listSessionHistory(c, agentID, sessionID, limit)
}

func (s *Service) listSessionHistory(
	c context.Context,
	agentID string,
	sessionID string,
	limit int,
) (int, any, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	messages, err := s.store.ListMessages(c, agentID, sessionID, limit)
	if err != nil {
		return 0, nil, err
	}
	history := make([]domain.MessageWithParts, 0, len(messages))
	for _, message := range messages {
		parts, err := s.store.ListMessageParts(c, message.MessageID)
		if err != nil {
			return 0, nil, err
		}
		history = append(history, domain.MessageWithParts{
			Message: message,
			Parts:   parts,
		})
	}
	return http.StatusOK, map[string]any{"messages": history}, nil
}

func (s *Service) CreateMailboxMessage(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateMailboxRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if req.AgentID == "" || req.SessionID == "" || req.Message == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id, session_id, and message are required",
		}
	}
	if domain.DefaultMessageType(req.MessageType) == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "message_type must be chat, steer, command, or system_handoff",
		}
	}
	if req.NodeID != "" {
		if req.SessionID != "" {
			if _, err := s.nodeSessionTarget(
				c,
				principal,
				req.NodeID,
				req.AgentID,
				req.SessionID,
			); err != nil {
				return 0, nil, err
			}
		} else if _, err := s.nodeAgentTarget(c, principal, req.NodeID, req.AgentID); err != nil {
			return 0, nil, err
		}
	} else if req.SessionID != "" {
		if _, err := s.sessionTarget(c, principal, req.AgentID, req.SessionID); err != nil {
			return 0, nil, err
		}
	}

	msg, err := s.store.CreateMailboxMessage(c, principal, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, msg, nil
}

func (s *Service) ListNodeMailbox(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	agentID string,
	sessionID string,
	status string,
	limit int,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if nodeID == "" || agentID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id and agent_id are required",
		}
	}
	if sessionID != "" {
		if _, err := s.nodeSessionTarget(c, principal, nodeID, agentID, sessionID); err != nil {
			return 0, nil, err
		}
	} else if _, err := s.nodeAgentTarget(c, principal, nodeID, agentID); err != nil {
		return 0, nil, err
	}
	if limit == 0 {
		limit = 50
	}
	messages, err := s.store.ListMailbox(c, domain.MailboxFilter{
		Principal: principal,
		NodeID:    nodeID,
		AgentID:   agentID,
		SessionID: sessionID,
		Status:    status,
		Limit:     limit,
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"messages": messages}, nil
}

func (s *Service) CreateSessionMessage(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateMailboxRequest,
) (int, any, error) {
	if req.AgentID == "" || req.SessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id and session_id are required",
		}
	}
	return s.CreateMailboxMessage(c, meta, req)
}

func (s *Service) nodeAgentTarget(
	c context.Context,
	principal domain.UserPrincipal,
	nodeID string,
	agentID string,
) (domain.Agent, error) {
	agent, err := s.store.GetAgent(c, principal, agentID)
	if err != nil {
		return domain.Agent{}, err
	}
	if agent.NodeID != nodeID {
		return domain.Agent{}, domain.ErrNotFound
	}
	return agent, nil
}

func (s *Service) nodeSessionTarget(
	c context.Context,
	principal domain.UserPrincipal,
	nodeID string,
	agentID string,
	sessionID string,
) (domain.AgentSession, error) {
	if _, err := s.nodeAgentTarget(c, principal, nodeID, agentID); err != nil {
		return domain.AgentSession{}, err
	}
	session, err := s.sessionTarget(c, principal, agentID, sessionID)
	if err != nil {
		return domain.AgentSession{}, err
	}
	if session.NodeID != "" && session.NodeID != nodeID {
		return domain.AgentSession{}, domain.ErrNotFound
	}
	return session, nil
}

func (s *Service) sessionTarget(
	c context.Context,
	principal domain.UserPrincipal,
	agentID string,
	sessionID string,
) (domain.AgentSession, error) {
	session, err := s.store.GetSession(c, principal, sessionID)
	if err != nil {
		return domain.AgentSession{}, err
	}
	if session.AgentID != agentID {
		return domain.AgentSession{}, domain.ErrNotFound
	}
	return session, nil
}

func filterNodeSessions(sessions []domain.AgentSession, nodeID string) []domain.AgentSession {
	filtered := sessions[:0]
	for _, session := range sessions {
		if session.NodeID == "" || session.NodeID == nodeID {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

func (s *Service) ListMailbox(
	c context.Context,
	meta auth.RequestMetadata,
	agentID string,
	sessionID string,
	status string,
	limit int,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if limit == 0 {
		limit = 50
	}
	messages, err := s.store.ListMailbox(c, domain.MailboxFilter{
		Principal: principal,
		AgentID:   agentID,
		SessionID: sessionID,
		Status:    status,
		Limit:     limit,
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"messages": messages}, nil
}

func (s *Service) CreateRegistrationToken(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateRegistrationTokenRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}

	owner := principal.User
	if req.OwnerEmail != "" || req.OwnerUserID != "" {
		if !principal.IsAdmin {
			return 0, nil, apperr.Error{
				Status:  http.StatusForbidden,
				Message: "only admins can mint tokens for another user",
			}
		}
		if req.OwnerEmail != "" {
			owner, err = s.store.GetUserByEmail(c, req.OwnerEmail)
			if err != nil {
				return 0, nil, err
			}
		} else {
			owner, err = s.store.GetUser(c, req.OwnerUserID)
			if err != nil {
				return 0, nil, err
			}
		}
	}

	token, err := s.secrets.New("reg")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate token",
		}
	}
	var expiresAt *time.Time
	if req.ExpiresInSeconds > 0 {
		t := s.clock().UTC().Add(time.Duration(req.ExpiresInSeconds) * time.Second)
		expiresAt = &t
	}
	if err := s.store.CreateRegistrationToken(c, owner.UserID, s.secrets.Hash(token), expiresAt); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, domain.CreateRegistrationTokenResponse{
		Token:       token,
		OwnerUserID: owner.UserID,
		ExpiresAt:   expiresAt,
	}, nil
}

func (s *Service) CreateUserAPIKey(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateUserAPIKeyRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	key, err := s.secrets.New("paxu")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate api key",
		}
	}
	keyMeta, err := s.store.CreateUserAPIKey(
		c,
		principal,
		req.Name,
		s.secrets.Hash(key),
		s.secrets.Prefix(key),
	)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, domain.CreateUserAPIKeyResponse{APIKey: keyMeta, Key: key}, nil
}

func (s *Service) ListUserAPIKeys(c context.Context, meta auth.RequestMetadata) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	keys, err := s.store.ListUserAPIKeys(c, principal)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"api_keys": keys}, nil
}

func (s *Service) RevokeUserAPIKey(
	c context.Context,
	meta auth.RequestMetadata,
	keyID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if keyID == "" {
		return 0, nil, apperr.Error{Status: http.StatusNotFound, Message: "unknown api key route"}
	}
	if err := s.store.RevokeUserAPIKey(c, principal, keyID); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}
