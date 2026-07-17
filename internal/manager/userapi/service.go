package userapi

import (
	"context"
	"encoding/json"
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

const (
	defaultSessionPageSize = 50
	maxSessionPageSize     = 200
)

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
	DeleteAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.DeleteAgentRequest,
	) (domain.Agent, error)
	ListNodes(ctx context.Context, principal domain.UserPrincipal) ([]domain.Node, error)
	GetNode(ctx context.Context, principal domain.UserPrincipal, nodeID string) (domain.Node, error)
	UpdateNode(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.UpdateNodeRequest,
	) (domain.Node, error)
	DeleteNode(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.DeleteNodeRequest,
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
	DeleteNodeAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.DeleteAgentRequest,
	) (domain.Agent, error)
	CreateNodeAgentSession(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.CreateSessionRequest,
	) (domain.AgentSession, error)
	UpdateNodeAgentSession(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.UpdateSessionRequest,
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
	ListSessions(
		ctx context.Context,
		principal domain.UserPrincipal,
		filter domain.ListSessionsFilter,
	) (domain.ListSessionsResult, error)
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

type NodeControlClient interface {
	RemoteID(nodeID string) (string, error)
	Query(
		ctx context.Context,
		nodeID string,
		requestID string,
		query any,
	) (json.RawMessage, error)
	Command(
		ctx context.Context,
		nodeID string,
		commandID string,
		command any,
	) (json.RawMessage, error)
}

type Service struct {
	store            Store
	clock            func() time.Time
	principal        PrincipalResolver
	secrets          SecretIssuer
	vault            *vaultsecrets.Cipher
	backgroundRunner func(context.Context, func(context.Context))
	memexExecutor    TeamMemexExecutor
	nodeControl      NodeControlClient
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

func (s *Service) SetNodeControlClient(client NodeControlClient) {
	s.nodeControl = client
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

func (s *Service) GetNodeDaemonStatus(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
) (int, any, error) {
	return s.queryNodeDaemon(c, meta, nodeID, map[string]any{
		"type":       "status.get",
		"get_status": map[string]any{},
	})
}

func (s *Service) ListNodeDaemonHarnesses(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	includeMissing bool,
) (int, any, error) {
	return s.queryNodeDaemon(c, meta, nodeID, map[string]any{
		"type": "harnesses.list",
		"list_harnesses": map[string]any{
			"include_missing": includeMissing,
		},
	})
}

func (s *Service) ListNodeDaemonAgentConnections(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	includeDisabled bool,
) (int, any, error) {
	return s.queryNodeDaemon(c, meta, nodeID, map[string]any{
		"type": "agent_connections.list",
		"list_agent_connections": map[string]any{
			"include_disabled": includeDisabled,
		},
	})
}

func (s *Service) GetNodeDaemonCommand(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	commandID string,
) (int, any, error) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "command_id is required",
		}
	}
	return s.queryNodeDaemon(c, meta, nodeID, map[string]any{
		"type": "command.get",
		"get_command": map[string]any{
			"command_id": commandID,
		},
	})
}

func (s *Service) queryNodeDaemon(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	query any,
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
	result, err := s.queryNodeControl(c, node.NodeID, query)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, result, nil
}

func (s *Service) queryNodeControl(
	c context.Context,
	nodeID string,
	query any,
) (json.RawMessage, error) {
	if s.nodeControl == nil {
		return nil, nodeControlUnavailableError()
	}
	requestID, err := s.secrets.New("ctlq")
	if err != nil {
		return nil, err
	}
	result, err := s.nodeControl.Query(c, nodeID, requestID, query)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, apperr.Error{
				Status:  http.StatusGatewayTimeout,
				Message: "node control query timed out",
			}
		}
		return nil, nodeControlUnavailableError()
	}
	return result, nil
}

func nodeControlUnavailableError() error {
	return apperr.Error{
		Status:  http.StatusServiceUnavailable,
		Message: "node control tunnel is not connected",
	}
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

func (s *Service) DeleteNode(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.DeleteNodeRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if req.NodeID == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "node_id is required"}
	}
	node, err := s.store.DeleteNode(c, principal, req)
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

type nodeDaemonHarnessQueryResult struct {
	Error     *nodeDaemonControlError `json:"error,omitempty"`
	Harnesses *struct {
		Items []nodeDaemonHarness `json:"items"`
	} `json:"harnesses,omitempty"`
}

type nodeDaemonControlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type nodeDaemonHarness struct {
	Harness     string   `json:"harness"`
	State       string   `json:"state"`
	Command     []string `json:"command"`
	InstallHint string   `json:"install_hint"`
	LastError   string   `json:"last_error"`
}

type nodeDaemonCommandAck struct {
	CommandID  string                  `json:"command_id"`
	OK         bool                    `json:"ok"`
	Status     string                  `json:"status"`
	TargetID   string                  `json:"target_id"`
	Generation int64                   `json:"desired_generation"`
	Error      *nodeDaemonControlError `json:"error,omitempty"`
	Result     *struct {
		AgentConnection *struct {
			ID string `json:"id"`
		} `json:"agent_connection,omitempty"`
	} `json:"result,omitempty"`
}

func (s *Service) CreateNodeDaemonAgentConnection(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateNodeDaemonAgentConnectionRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.CommandID = strings.TrimSpace(req.CommandID)
	req.Harness = strings.TrimSpace(req.Harness)
	if req.NodeID == "" || req.CommandID == "" || req.Harness == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, command_id, and harness are required",
		}
	}
	node, err := s.store.GetNode(c, principal, req.NodeID)
	if err != nil {
		return 0, nil, err
	}
	if s.nodeControl == nil {
		return 0, nil, nodeControlUnavailableError()
	}
	remoteID, err := s.nodeControl.RemoteID(node.NodeID)
	if err != nil {
		return 0, nil, nodeControlUnavailableError()
	}
	command, err := s.resolveNodeDaemonHarnessCommand(c, node.NodeID, req.Harness, req.Command)
	if err != nil {
		return 0, nil, err
	}

	name := firstNodeDaemonValue(req.Name, req.Harness)
	agentType := firstNodeDaemonValue(req.AgentType, req.Harness)
	instanceID := firstNodeDaemonValue(req.InstanceID, name)
	agent, bootstrap, err := s.store.CreateNodeAgent(c, principal, domain.CreateAgentRequest{
		NodeID:    node.NodeID,
		Name:      name,
		AgentType: agentType,
	})
	if err != nil {
		return 0, nil, err
	}

	data := map[string]any{
		"agent":             agent,
		"agent_id":          agent.AgentID,
		"bootstrap_message": bootstrap,
		"command_id":        req.CommandID,
		"remote_id":         remoteID,
		"dispatch_status":   "unknown",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id": req.CommandID,
		"type":       "agent_connection.create",
		"create_agent_connection": map[string]any{
			"remote_id":      remoteID,
			"name":           name,
			"cloud_agent_id": agent.AgentID,
			"instance_id":    instanceID,
			"agent_type":     agentType,
			"harness":        req.Harness,
			"command":        command,
			"working_dir":    strings.TrimSpace(req.WorkingDir),
			"desired_state":  "running",
		},
	}, data)
}

func (s *Service) UpdateNodeDaemonAgentConnection(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.UpdateNodeDaemonAgentConnectionRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.ConnectionID = strings.TrimSpace(req.ConnectionID)
	req.CommandID = strings.TrimSpace(req.CommandID)
	if req.NodeID == "" || req.ConnectionID == "" || req.CommandID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, connection_id, and command_id are required",
		}
	}
	if req.Name == nil && req.Harness == nil && req.Command == nil && req.WorkingDir == nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "at least one update field is required",
		}
	}
	node, err := s.store.GetNode(c, principal, req.NodeID)
	if err != nil {
		return 0, nil, err
	}
	if s.nodeControl == nil {
		return 0, nil, nodeControlUnavailableError()
	}
	remoteID, err := s.nodeControl.RemoteID(node.NodeID)
	if err != nil {
		return 0, nil, nodeControlUnavailableError()
	}

	update := map[string]any{"connection_id": req.ConnectionID}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return 0, nil, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "name cannot be empty",
			}
		}
		update["name"] = name
	}
	if req.Harness != nil {
		harness := strings.TrimSpace(*req.Harness)
		if harness == "" {
			return 0, nil, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "harness cannot be empty",
			}
		}
		var explicit []string
		if req.Command != nil {
			explicit = *req.Command
		}
		command, err := s.resolveNodeDaemonHarnessCommand(c, node.NodeID, harness, explicit)
		if err != nil {
			return 0, nil, err
		}
		update["harness"] = harness
		update["command"] = command
	} else if req.Command != nil {
		command := normalizedNodeDaemonCommand(*req.Command)
		if len(command) == 0 {
			return 0, nil, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "command cannot be empty",
			}
		}
		update["command"] = command
	}
	if req.WorkingDir != nil {
		update["working_dir"] = strings.TrimSpace(*req.WorkingDir)
	}
	data := map[string]any{
		"connection_id":   req.ConnectionID,
		"command_id":      req.CommandID,
		"remote_id":       remoteID,
		"dispatch_status": "unknown",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id":              req.CommandID,
		"type":                    "agent_connection.update",
		"update_agent_connection": update,
	}, data)
}

func (s *Service) StopNodeDaemonAgentConnection(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.NodeDaemonAgentConnectionActionRequest,
) (int, any, error) {
	node, remoteID, err := s.authorizeNodeDaemonAgentConnectionCommand(c, meta, &req)
	if err != nil {
		return 0, nil, err
	}
	data := map[string]any{
		"connection_id":   req.ConnectionID,
		"command_id":      req.CommandID,
		"remote_id":       remoteID,
		"dispatch_status": "unknown",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id": req.CommandID,
		"type":       "agent_connection.update",
		"update_agent_connection": map[string]any{
			"connection_id": req.ConnectionID,
			"desired_state": "stopped",
		},
	}, data)
}

func (s *Service) RestartNodeDaemonAgentConnection(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.NodeDaemonAgentConnectionActionRequest,
) (int, any, error) {
	node, remoteID, err := s.authorizeNodeDaemonAgentConnectionCommand(c, meta, &req)
	if err != nil {
		return 0, nil, err
	}
	data := map[string]any{
		"connection_id":   req.ConnectionID,
		"command_id":      req.CommandID,
		"remote_id":       remoteID,
		"dispatch_status": "unknown",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id": req.CommandID,
		"type":       "agent_connection.restart",
		"restart_agent_connection": map[string]any{
			"connection_id": req.ConnectionID,
		},
	}, data)
}

func (s *Service) RemoveNodeDaemonAgentConnection(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.NodeDaemonAgentConnectionActionRequest,
) (int, any, error) {
	node, remoteID, err := s.authorizeNodeDaemonAgentConnectionCommand(c, meta, &req)
	if err != nil {
		return 0, nil, err
	}
	data := map[string]any{
		"connection_id":   req.ConnectionID,
		"command_id":      req.CommandID,
		"remote_id":       remoteID,
		"dispatch_status": "unknown",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id": req.CommandID,
		"type":       "agent_connection.delete",
		"delete_agent_connection": map[string]any{
			"connection_id": req.ConnectionID,
		},
	}, data)
}

func (s *Service) authorizeNodeDaemonAgentConnectionCommand(
	c context.Context,
	meta auth.RequestMetadata,
	req *domain.NodeDaemonAgentConnectionActionRequest,
) (domain.Node, string, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return domain.Node{}, "", err
	}
	if req == nil {
		return domain.Node{}, "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "command request is required",
		}
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.ConnectionID = strings.TrimSpace(req.ConnectionID)
	req.CommandID = strings.TrimSpace(req.CommandID)
	if req.NodeID == "" || req.ConnectionID == "" || req.CommandID == "" {
		return domain.Node{}, "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, connection_id, and command_id are required",
		}
	}
	node, err := s.store.GetNode(c, principal, req.NodeID)
	if err != nil {
		return domain.Node{}, "", err
	}
	if s.nodeControl == nil {
		return domain.Node{}, "", nodeControlUnavailableError()
	}
	remoteID, err := s.nodeControl.RemoteID(node.NodeID)
	if err != nil {
		return domain.Node{}, "", nodeControlUnavailableError()
	}
	return node, remoteID, nil
}

func (s *Service) dispatchNodeDaemonCommand(
	c context.Context,
	nodeID string,
	commandID string,
	command any,
	data map[string]any,
) (int, any, error) {
	ackRaw, dispatchErr := s.nodeControl.Command(c, nodeID, commandID, command)
	if dispatchErr != nil {
		data["dispatch_error"] = dispatchErr.Error()
		return http.StatusAccepted, data, nil
	}
	var ack nodeDaemonCommandAck
	if err := json.Unmarshal(ackRaw, &ack); err != nil {
		data["dispatch_error"] = "invalid command acknowledgement"
		return http.StatusAccepted, data, nil
	}
	connectionID := ack.TargetID
	if connectionID == "" && ack.Result != nil && ack.Result.AgentConnection != nil {
		connectionID = ack.Result.AgentConnection.ID
	}
	data["command_ack"] = ackRaw
	data["command_status"] = ack.Status
	data["connection_id"] = connectionID
	data["desired_generation"] = ack.Generation
	data["dispatch_status"] = "acknowledged"
	return http.StatusAccepted, data, nil
}

func (s *Service) resolveNodeDaemonHarnessCommand(
	c context.Context,
	nodeID string,
	harness string,
	explicit []string,
) ([]string, error) {
	if command := normalizedNodeDaemonCommand(explicit); len(command) > 0 {
		return command, nil
	}
	result, err := s.queryNodeDaemonHarnesses(c, nodeID, map[string]any{
		"type": "harnesses.list",
		"list_harnesses": map[string]any{
			"include_missing": true,
		},
	})
	if err != nil {
		return nil, err
	}
	command, found, cachedErr := commandForNodeDaemonHarness(result, harness)
	if found && cachedErr == nil {
		return command, nil
	}
	result, err = s.queryNodeDaemonHarnesses(c, nodeID, map[string]any{
		"type": "harnesses.discover",
		"discover_harnesses": map[string]any{
			"names": []string{harness},
		},
	})
	if err != nil {
		return nil, err
	}
	if command, found, err = commandForNodeDaemonHarness(result, harness); found || err != nil {
		return command, err
	}
	if cachedErr != nil {
		return nil, cachedErr
	}
	return nil, apperr.Error{
		Status:  http.StatusBadRequest,
		Message: "requested harness is not known to paxd",
	}
}

func (s *Service) queryNodeDaemonHarnesses(
	c context.Context,
	nodeID string,
	query any,
) (nodeDaemonHarnessQueryResult, error) {
	raw, err := s.queryNodeControl(c, nodeID, query)
	if err != nil {
		return nodeDaemonHarnessQueryResult{}, err
	}
	var result nodeDaemonHarnessQueryResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, apperr.Error{
			Status:  http.StatusBadGateway,
			Message: "paxd returned an invalid harness result",
		}
	}
	if result.Error != nil {
		message := strings.TrimSpace(result.Error.Message)
		if message == "" {
			message = "paxd harness query failed"
		}
		return result, apperr.Error{Status: http.StatusBadGateway, Message: message}
	}
	return result, nil
}

func commandForNodeDaemonHarness(
	result nodeDaemonHarnessQueryResult,
	harness string,
) ([]string, bool, error) {
	if result.Harnesses == nil {
		return nil, false, nil
	}
	for _, item := range result.Harnesses.Items {
		if !strings.EqualFold(strings.TrimSpace(item.Harness), harness) {
			continue
		}
		state := strings.TrimSpace(item.State)
		if state != "" && state != "available" {
			message := firstNodeDaemonValue(
				item.LastError,
				item.InstallHint,
				"requested harness is not available",
			)
			return nil, true, apperr.Error{Status: http.StatusBadRequest, Message: message}
		}
		command := normalizedNodeDaemonCommand(item.Command)
		if len(command) == 0 {
			return nil, true, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "requested harness has no command configured",
			}
		}
		return command, true, nil
	}
	return nil, false, nil
}

func normalizedNodeDaemonCommand(command []string) []string {
	normalized := make([]string, 0, len(command))
	for _, word := range command {
		if word = strings.TrimSpace(word); word != "" {
			normalized = append(normalized, word)
		}
	}
	return normalized
}

func firstNodeDaemonValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
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

func (s *Service) DeleteNodeAgent(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.DeleteAgentRequest,
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
	agent, err := s.store.DeleteNodeAgent(c, principal, req)
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

func (s *Service) DeleteAgent(
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
	agent, err := s.store.DeleteAgent(c, principal, domain.DeleteAgentRequest{
		AgentID: agentID,
	})
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

func (s *Service) ListSessions(
	c context.Context,
	meta auth.RequestMetadata,
	ownerUserID string,
	nodeID string,
	agentID string,
	pageSize int,
	pageNum int,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" || ownerUserID == "self" {
		ownerUserID = principal.User.UserID
	}
	if ownerUserID != principal.User.UserID {
		return 0, nil, domain.ErrNotFound
	}
	pageSize, pageNum = normalizeSessionPagination(pageSize, pageNum)
	result, err := s.store.ListSessions(c, principal, domain.ListSessionsFilter{
		OwnerUserID: ownerUserID,
		NodeIDs:     splitCommaSeparatedIDs(nodeID),
		AgentIDs:    splitCommaSeparatedIDs(agentID),
		PageSize:    pageSize,
		PageNum:     pageNum,
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, result, nil
}

func splitCommaSeparatedIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

func normalizeSessionPagination(pageSize int, pageNum int) (int, int) {
	if pageSize <= 0 {
		pageSize = defaultSessionPageSize
	}
	if pageSize > maxSessionPageSize {
		pageSize = maxSessionPageSize
	}
	if pageNum <= 0 {
		pageNum = 1
	}
	return pageSize, pageNum
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

func (s *Service) UpdateNodeAgentSession(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.UpdateSessionRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if req.NodeID == "" || req.AgentID == "" || req.SessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, agent_id, and session_id are required",
		}
	}
	if req.PaxConfig.CWD != "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "pax_config.cwd is create-only and cannot be changed",
		}
	}
	mode := strings.TrimSpace(req.PaxConfig.ApprovalMode)
	if mode == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "pax_config.approval_mode is required",
		}
	}
	if !domain.IsSessionApprovalMode(mode) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "pax_config.approval_mode must be manual or auto_approve_all",
		}
	}
	if _, err := s.nodeSessionTarget(c, principal, req.NodeID, req.AgentID, req.SessionID); err != nil {
		return 0, nil, err
	}
	req.PaxConfig.ApprovalMode = mode
	session, err := s.store.UpdateNodeAgentSession(c, principal, req)
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
	history = domain.NormalTranscriptMessages(history)
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
