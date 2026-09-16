package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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
	ProjectStore
	ProjectTargetStore
	TeamStore
	TeamMemexStore
	MailboxStore
}

const (
	defaultSessionPageSize = 50
	maxSessionPageSize     = 200
	defaultHistoryPageSize = 1000
	maxHistoryPageSize     = 1000
	maxSessionNameLength   = 120
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
	domain.MessageSummaryStore
	ListMessages(
		ctx context.Context,
		agentID string,
		sessionID string,
		limit int,
	) ([]domain.Message, error)
	ListMessageHistoryPage(
		ctx context.Context,
		agentID string,
		sessionID string,
		beforeID int64,
		limit int,
	) (domain.MessageHistoryPage, error)
	ListMessageHistoryPageBySeq(
		ctx context.Context,
		agentID string,
		sessionID string,
		afterSeq int64,
		beforeSeq int64,
		limit int,
	) (domain.MessageHistoryPage, error)
	ListMessageParts(ctx context.Context, messageID string) ([]domain.MessagePart, error)
	ListMessagePartsByMessageIDs(
		ctx context.Context,
		messageIDs []string,
	) (map[string][]domain.MessagePart, error)
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
	ListFriendsBetween(
		ctx context.Context,
		principal domain.UserPrincipal,
		email string,
	) ([]domain.Friend, error)
	DeleteRemovedFriendsBetween(
		ctx context.Context,
		principal domain.UserPrincipal,
		email string,
	) error
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

type ProjectStore interface {
	CreateProject(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.CreateProjectRequest,
	) (domain.Project, error)
	ListProjects(
		ctx context.Context,
		principal domain.UserPrincipal,
		filter domain.ListProjectsFilter,
	) ([]domain.Project, error)
	GetProject(
		ctx context.Context,
		principal domain.UserPrincipal,
		projectID string,
	) (domain.Project, error)
	UpdateProject(
		ctx context.Context,
		principal domain.UserPrincipal,
		projectID string,
		req domain.UpdateProjectRequest,
	) (domain.Project, error)
	ArchiveProject(
		ctx context.Context,
		principal domain.UserPrincipal,
		projectID string,
	) (domain.Project, error)
}

type ProjectTargetStore interface {
	CreateProjectTarget(
		ctx context.Context,
		principal domain.UserPrincipal,
		projectID string,
		req domain.CreateProjectTargetRequest,
	) (domain.ProjectTarget, error)
	ListProjectTargets(
		ctx context.Context,
		principal domain.UserPrincipal,
		projectID string,
	) ([]domain.ProjectTarget, error)
	GetProjectTarget(
		ctx context.Context,
		principal domain.UserPrincipal,
		projectID string,
		targetID string,
	) (domain.ProjectTarget, error)
	UpdateProjectTarget(
		ctx context.Context,
		principal domain.UserPrincipal,
		projectID string,
		targetID string,
		req domain.UpdateProjectTargetRequest,
	) (domain.ProjectTarget, error)
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

type nodeMaintenanceTracker interface {
	TrackMaintenance(nodeID, commandID, requestedBootID, expectedVersion string)
	MaintenanceConfirmation(
		nodeID string,
		commandID string,
	) (domain.NodeDaemonMaintenanceConfirmation, bool)
}

type Service struct {
	store             Store
	clock             func() time.Time
	principal         PrincipalResolver
	secrets           SecretIssuer
	vault             *vaultsecrets.Cipher
	backgroundRunner  func(context.Context, func(context.Context))
	memexExecutor     TeamMemexExecutor
	nodeControl       NodeControlClient
	historyReconciler func(context.Context, string, string) error
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

func (s *Service) SetHistoryReconciler(
	reconciler func(context.Context, string, string) error,
) {
	s.historyReconciler = reconciler
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

func (s *Service) ListAgents(
	c context.Context,
	meta auth.RequestMetadata,
	scope string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != "" && scope != "accessible" && scope != "owned" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "scope must be accessible or owned",
		}
	}
	agents, err := s.store.ListAgents(c, principal)
	if err != nil {
		return 0, nil, err
	}
	if scope == "owned" {
		owned := make([]domain.Agent, 0, len(agents))
		for _, agent := range agents {
			if agent.OwnerUserID == principal.User.UserID {
				owned = append(owned, agent)
			}
		}
		agents = owned
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

// OpenNodeDaemonSecretChannel forwards a secret_channel.open query to the
// connected paxd control tunnel. The response carries a short-lived,
// single-use public key that paxd generated in memory; pax-manager never
// holds the corresponding private key, so it has no way to decrypt whatever
// a browser later seals against this key.
func (s *Service) OpenNodeDaemonSecretChannel(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
) (int, any, error) {
	return s.queryNodeDaemon(c, meta, nodeID, map[string]any{
		"type":                "secret_channel.open",
		"open_secret_channel": map[string]any{},
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

func (s *Service) DiscoverNodeDaemonHarnesses(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.DiscoverNodeDaemonHarnessesRequest,
) (int, any, error) {
	discover := map[string]any{"probe": req.Probe}
	if len(req.Names) > 0 {
		names := make([]string, 0, len(req.Names))
		for _, name := range req.Names {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			discover["names"] = names
		}
	}
	return s.queryNodeDaemon(c, meta, req.NodeID, map[string]any{
		"type":               "harnesses.discover",
		"discover_harnesses": discover,
	})
}

func (s *Service) ListNodeDaemonAgentConnections(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	includeDisabled bool,
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
	if s.nodeControl == nil {
		return 0, nil, nodeControlUnavailableError()
	}
	remoteID, err := s.nodeControl.RemoteID(node.NodeID)
	if err != nil {
		return 0, nil, nodeControlUnavailableError()
	}
	result, err := s.queryNodeControl(c, node.NodeID, map[string]any{
		"type": "agent_connections.list",
		"list_agent_connections": map[string]any{
			"remote_id":        remoteID,
			"include_disabled": includeDisabled,
		},
	})
	if err != nil {
		return 0, nil, err
	}
	result, err = s.filterOwnedNodeDaemonAgentConnections(c, principal, node.NodeID, result)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, result, nil
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
	status, data, err := s.queryNodeDaemon(c, meta, nodeID, map[string]any{
		"type": "command.get",
		"get_command": map[string]any{
			"command_id": commandID,
		},
	})
	if err != nil {
		return status, data, err
	}
	tracker, ok := s.nodeControl.(nodeMaintenanceTracker)
	if !ok {
		return status, data, nil
	}
	raw, ok := data.(json.RawMessage)
	if !ok {
		return status, data, nil
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return status, data, nil
	}
	confirmation, found := tracker.MaintenanceConfirmation(nodeID, commandID)
	if !found {
		command, _ := result["command"].(map[string]any)
		maintenanceResult, _ := command["result"].(map[string]any)
		requestedBootID, _ := maintenanceResult["requested_boot_id"].(string)
		commandType, _ := command["type"].(string)
		expectedVersion := ""
		if commandType == "paxd.upgrade" {
			expectedVersion, _ = maintenanceResult["target_version"].(string)
		}
		if requestedBootID != "" &&
			(commandType == "paxd.restart" || commandType == "paxd.upgrade") {
			tracker.TrackMaintenance(nodeID, commandID, requestedBootID, expectedVersion)
			confirmation, found = tracker.MaintenanceConfirmation(nodeID, commandID)
		}
	}
	if found {
		result["maintenance_confirmation"] = confirmation
	}
	return status, result, nil
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

func (s *Service) filterOwnedNodeDaemonAgentConnections(
	c context.Context,
	principal domain.UserPrincipal,
	nodeID string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	var result nodeDaemonAgentConnectionsQueryResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, apperr.Error{
			Status:  http.StatusBadGateway,
			Message: "paxd returned an invalid agent connection result",
		}
	}
	if result.Error != nil || result.AgentConnections == nil ||
		len(result.AgentConnections.Items) == 0 {
		return raw, nil
	}

	identities := make([]nodeDaemonAgentConnectionIdentity, len(result.AgentConnections.Items))
	hasBoundAgent := false
	for i, item := range result.AgentConnections.Items {
		if err := json.Unmarshal(item, &identities[i]); err != nil {
			return nil, apperr.Error{
				Status:  http.StatusBadGateway,
				Message: "paxd returned an invalid agent connection",
			}
		}
		hasBoundAgent = hasBoundAgent || strings.TrimSpace(identities[i].CloudAgentID) != ""
	}
	if !hasBoundAgent {
		return raw, nil
	}

	agents, err := s.store.ListNodeAgents(c, principal, nodeID)
	if err != nil {
		return nil, err
	}
	ownedAgentIDs := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		if agent.OwnerUserID == principal.User.UserID {
			ownedAgentIDs[agent.AgentID] = struct{}{}
		}
	}

	filtered := make([]json.RawMessage, 0, len(result.AgentConnections.Items))
	for i, item := range result.AgentConnections.Items {
		agentID := strings.TrimSpace(identities[i].CloudAgentID)
		if agentID == "" {
			filtered = append(filtered, item)
			continue
		}
		if _, ok := ownedAgentIDs[agentID]; ok {
			filtered = append(filtered, item)
		}
	}
	result.AgentConnections.Items = filtered
	filteredRaw, err := json.Marshal(result)
	if err != nil {
		return nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "failed to encode filtered agent connections",
		}
	}
	return filteredRaw, nil
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

type nodeDaemonAgentConnectionsQueryResult struct {
	Type             string                          `json:"type"`
	Error            *nodeDaemonControlError         `json:"error,omitempty"`
	AgentConnections *nodeDaemonAgentConnectionItems `json:"agent_connections,omitempty"`
}

type nodeDaemonAgentConnectionItems struct {
	Items []json.RawMessage `json:"items"`
}

type nodeDaemonAgentConnectionIdentity struct {
	CloudAgentID string `json:"cloud_agent_id"`
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
		PaxdRestart *struct {
			RequestedBootID string `json:"requested_boot_id"`
		} `json:"paxd_restart,omitempty"`
		PaxdUpgrade *struct {
			RequestedBootID string `json:"requested_boot_id"`
			TargetVersion   string `json:"target_version"`
		} `json:"paxd_upgrade,omitempty"`
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
	desiredSlots, err := nodeDaemonDesiredSlots(req.DesiredSlots)
	if err != nil {
		return 0, nil, err
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
			"remote_id":             remoteID,
			"name":                  name,
			"cloud_agent_id":        agent.AgentID,
			"instance_id":           instanceID,
			"agent_type":            agentType,
			"harness":               req.Harness,
			"command":               command,
			"working_dir":           strings.TrimSpace(req.WorkingDir),
			"desired_state":         "running",
			"desired_slots":         desiredSlots,
			"report_local_sessions": nodeDaemonReportLocalSessions(req.ReportLocalSessions),
		},
	}, data)
}

func (s *Service) RestartNodeDaemon(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.RestartNodeDaemonRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.CommandID = strings.TrimSpace(req.CommandID)
	if req.NodeID == "" || req.CommandID == "" {
		return 0, nil, apperr.Error{
			Status: http.StatusBadRequest, Message: "node_id and command_id are required",
		}
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "immediate"
	}
	if mode != "immediate" && mode != "when_idle" {
		return 0, nil, apperr.Error{
			Status: http.StatusBadRequest, Message: "mode must be immediate or when_idle",
		}
	}
	if err := validateDaemonMaintenanceOptions(req.ShutdownGraceSeconds, req.IdleGraceSeconds, req.DrainTimeoutSeconds, req.Reason); err != nil {
		return 0, nil, err
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
	restart := map[string]any{"mode": mode}
	if req.ShutdownGraceSeconds != nil {
		restart["shutdown_grace_seconds"] = *req.ShutdownGraceSeconds
	}
	if req.IdleGraceSeconds != nil {
		restart["idle_grace_seconds"] = *req.IdleGraceSeconds
	}
	if req.DrainTimeoutSeconds != nil {
		restart["drain_timeout_seconds"] = *req.DrainTimeoutSeconds
	}
	if req.ForceAtDeadline {
		restart["force_at_deadline"] = true
	}
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		restart["reason"] = reason
	}
	data := map[string]any{
		"command_id": req.CommandID, "remote_id": remoteID, "dispatch_status": "unknown",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id":   req.CommandID,
		"type":         "paxd.restart",
		"restart_paxd": restart,
	}, data)
}
func (s *Service) UpgradeNodeDaemon(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.UpgradeNodeDaemonRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.CommandID = strings.TrimSpace(req.CommandID)
	req.Version = strings.TrimSpace(req.Version)
	if req.NodeID == "" || req.CommandID == "" || req.Version == "" {
		return 0, nil, apperr.Error{
			Status: http.StatusBadRequest, Message: "node_id, command_id, and version are required",
		}
	}
	tag := strings.TrimSpace(req.Tag)
	if tag == "" {
		tag = "stable"
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "when_idle"
	}
	if mode != "immediate" && mode != "when_idle" && mode != "opportunistic" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "mode must be immediate, when_idle, or opportunistic",
		}
	}
	if err := validateDaemonMaintenanceOptions(req.ShutdownGraceSeconds, req.IdleGraceSeconds, req.DrainTimeoutSeconds, req.Reason); err != nil {
		return 0, nil, err
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
	upgrade := map[string]any{
		"version": req.Version,
		"tag":     tag,
		"mode":    mode,
	}
	if req.ShutdownGraceSeconds != nil {
		upgrade["shutdown_grace_seconds"] = *req.ShutdownGraceSeconds
	}
	if req.IdleGraceSeconds != nil {
		upgrade["idle_grace_seconds"] = *req.IdleGraceSeconds
	}
	if req.DrainTimeoutSeconds != nil {
		upgrade["drain_timeout_seconds"] = *req.DrainTimeoutSeconds
	}
	if req.ForceAtDeadline {
		upgrade["force_at_deadline"] = true
	}
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		upgrade["reason"] = reason
	}
	data := map[string]any{
		"command_id":          req.CommandID,
		"remote_id":           remoteID,
		"dispatch_status":     "unknown",
		"expected_version":    req.Version,
		"confirmation_status": "awaiting_new_boot",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id":   req.CommandID,
		"type":         "paxd.upgrade",
		"upgrade_paxd": upgrade,
	}, data)
}

func (s *Service) CancelNodeDaemonMaintenance(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CancelNodeDaemonMaintenanceRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.CommandID = strings.TrimSpace(req.CommandID)
	req.MaintenanceCommandID = strings.TrimSpace(req.MaintenanceCommandID)
	if req.NodeID == "" || req.CommandID == "" || req.MaintenanceCommandID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, command_id, and maintenance_command_id are required",
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
	data := map[string]any{
		"command_id":             req.CommandID,
		"maintenance_command_id": req.MaintenanceCommandID,
		"remote_id":              remoteID,
		"dispatch_status":        "unknown",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id": req.CommandID,
		"type":       "paxd.maintenance.cancel",
		"cancel_paxd_maintenance": map[string]any{
			"maintenance_command_id": req.MaintenanceCommandID,
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
	if !hasNodeDaemonAgentConnectionUpdate(req) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "at least one update field is required",
		}
	}
	if err := validateOptionalNodeDaemonDesiredSlots(req.DesiredSlots); err != nil {
		return 0, nil, err
	}
	if err := validateOptionalNodeDaemonDesiredState(req.DesiredState); err != nil {
		return 0, nil, err
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
	setNodeDaemonDesiredSlots(update, req.DesiredSlots)
	setNodeDaemonDesiredState(update, req.DesiredState)
	if req.ReportLocalSessions != nil {
		update["report_local_sessions"] = *req.ReportLocalSessions
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

func validateNodeDaemonDesiredSlots(desiredSlots int) error {
	if desiredSlots < 1 || desiredSlots > 16 {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "desired_slots must be between 1 and 16",
		}
	}
	return nil
}

func nodeDaemonDesiredSlots(desiredSlots *int) (int, error) {
	if desiredSlots == nil {
		return 2, nil
	}
	return *desiredSlots, validateNodeDaemonDesiredSlots(*desiredSlots)
}

func nodeDaemonReportLocalSessions(enabled *bool) bool {
	return enabled != nil && *enabled
}

func validateOptionalNodeDaemonDesiredSlots(desiredSlots *int) error {
	if desiredSlots == nil {
		return nil
	}
	return validateNodeDaemonDesiredSlots(*desiredSlots)
}

func setNodeDaemonDesiredSlots(update map[string]any, desiredSlots *int) {
	if desiredSlots != nil {
		update["desired_slots"] = *desiredSlots
	}
}

func hasNodeDaemonAgentConnectionUpdate(req domain.UpdateNodeDaemonAgentConnectionRequest) bool {
	return req.Name != nil || req.Harness != nil || req.Command != nil ||
		req.WorkingDir != nil || req.DesiredSlots != nil || req.DesiredState != nil ||
		req.ReportLocalSessions != nil
}

func validateOptionalNodeDaemonDesiredState(desiredState *string) error {
	if desiredState == nil {
		return nil
	}
	state := strings.TrimSpace(*desiredState)
	if state != "running" && state != "stopped" {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "desired_state must be running or stopped",
		}
	}
	return nil
}

func setNodeDaemonDesiredState(update map[string]any, desiredState *string) {
	if desiredState != nil {
		update["desired_state"] = strings.TrimSpace(*desiredState)
	}
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

// PushNodeDaemonSecretChannel forwards an already-sealed secret to the
// connected paxd control tunnel. req's byte fields are base64 ciphertext
// produced by a browser encrypting against the public key returned from
// OpenNodeDaemonSecretChannel; this method (and pax-manager generally)
// never decrypts them, it only relays opaque bytes.
func (s *Service) PushNodeDaemonSecretChannel(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.PushNodeDaemonSecretChannelRequest,
) (int, any, error) {
	node, _, err := s.authorizeNodeDaemonSecretChannelPush(c, meta, &req)
	if err != nil {
		return 0, nil, err
	}
	data := map[string]any{
		"channel_id":      req.ChannelID,
		"command_id":      req.CommandID,
		"dispatch_status": "unknown",
	}
	return s.dispatchNodeDaemonCommand(c, node.NodeID, req.CommandID, map[string]any{
		"command_id": req.CommandID,
		"type":       "secret_channel.push",
		"push_secret_channel": map[string]any{
			"channel_id":        req.ChannelID,
			"sender_public_key": req.SenderPublicKey,
			"nonce":             req.Nonce,
			"ciphertext":        req.Ciphertext,
		},
	}, data)
}

func (s *Service) authorizeNodeDaemonSecretChannelPush(
	c context.Context,
	meta auth.RequestMetadata,
	req *domain.PushNodeDaemonSecretChannelRequest,
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
	req.CommandID = strings.TrimSpace(req.CommandID)
	req.ChannelID = strings.TrimSpace(req.ChannelID)
	req.SenderPublicKey = strings.TrimSpace(req.SenderPublicKey)
	req.Nonce = strings.TrimSpace(req.Nonce)
	req.Ciphertext = strings.TrimSpace(req.Ciphertext)
	if req.NodeID == "" || req.CommandID == "" || req.ChannelID == "" ||
		req.SenderPublicKey == "" || req.Nonce == "" || req.Ciphertext == "" {
		return domain.Node{}, "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "all secret channel push fields are required",
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
	requestedBootID := ""
	expectedVersion := ""
	if ack.Result != nil {
		if ack.Result.PaxdRestart != nil {
			requestedBootID = ack.Result.PaxdRestart.RequestedBootID
		}
		if ack.Result.PaxdUpgrade != nil {
			requestedBootID = ack.Result.PaxdUpgrade.RequestedBootID
			expectedVersion = ack.Result.PaxdUpgrade.TargetVersion
		}
	}
	if tracker, ok := s.nodeControl.(nodeMaintenanceTracker); ok && requestedBootID != "" {
		tracker.TrackMaintenance(nodeID, commandID, requestedBootID, expectedVersion)
		data["requested_boot_id"] = requestedBootID
		if expectedVersion != "" {
			data["expected_version"] = expectedVersion
		}
		if confirmation, found := tracker.MaintenanceConfirmation(nodeID, commandID); found {
			data["maintenance_confirmation"] = confirmation
			data["confirmation_status"] = confirmation.Status
		}
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

func (s *Service) ResetSessionRuntime(
	c context.Context,
	meta auth.RequestMetadata,
	agentID string,
	sessionID string,
	expectedTurnInstanceID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	agentID = strings.TrimSpace(agentID)
	sessionID = strings.TrimSpace(sessionID)
	expectedTurnInstanceID = strings.TrimSpace(expectedTurnInstanceID)
	if agentID == "" || sessionID == "" || expectedTurnInstanceID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id, session_id, and expected_turn_instance_id are required",
		}
	}
	session, err := s.sessionTarget(c, principal, agentID, sessionID)
	if err != nil {
		return 0, nil, err
	}
	if session.RuntimeTurnInstanceID != expectedTurnInstanceID {
		return 0, nil, apperr.Error{
			Status: http.StatusConflict, Message: "session runtime turn has changed",
		}
	}
	if strings.TrimSpace(session.NativeID) == "" {
		return 0, nil, apperr.Error{
			Status: http.StatusConflict, Message: "session has no native runtime identity",
		}
	}
	agent, err := s.store.GetAgent(c, principal, agentID)
	if err != nil {
		return 0, nil, err
	}
	if agent.NodeID == "" || session.NodeID != "" && session.NodeID != agent.NodeID {
		return 0, nil, domain.ErrNotFound
	}
	connectionID := runtimeConnectionID(agent.Metadata)
	if connectionID == "" {
		return 0, nil, apperr.Error{
			Status: http.StatusConflict, Message: "agent has no daemon runtime connection",
		}
	}
	if s.nodeControl == nil {
		return 0, nil, nodeControlUnavailableError()
	}
	if _, err := s.nodeControl.RemoteID(agent.NodeID); err != nil {
		return 0, nil, nodeControlUnavailableError()
	}
	commandID, err := s.secrets.New("ctlcmd")
	if err != nil {
		return 0, nil, err
	}
	ackRaw, err := s.nodeControl.Command(c, agent.NodeID, commandID, map[string]any{
		"command_id": commandID,
		"type":       "session_runtime.reset",
		"reset_session_runtime": map[string]any{
			"agent_id":                  agentID,
			"connection_id":             connectionID,
			"native_session_id":         session.NativeID,
			"expected_turn_instance_id": expectedTurnInstanceID,
		},
	})
	if err != nil {
		return 0, nil, nodeControlUnavailableError()
	}
	return sessionRuntimeResetResponse(ackRaw, expectedTurnInstanceID, commandID)
}

func runtimeConnectionID(metadata json.RawMessage) string {
	var value struct {
		Runtime struct {
			ConnectionID string `json:"connection_id"`
		} `json:"runtime"`
	}
	if json.Unmarshal(metadata, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value.Runtime.ConnectionID)
}

func (s *Service) ListSessions(
	c context.Context,
	meta auth.RequestMetadata,
	ownerUserID string,
	nodeID string,
	agentID string,
	primaryProjectID string,
	includeArchived bool,
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
		OwnerUserID:      ownerUserID,
		NodeIDs:          splitCommaSeparatedIDs(nodeID),
		AgentIDs:         splitCommaSeparatedIDs(agentID),
		PrimaryProjectID: strings.TrimSpace(primaryProjectID),
		IncludeArchived:  includeArchived,
		PageSize:         pageSize,
		PageNum:          pageNum,
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
	req, err = normalizeUpdateNodeAgentSessionRequest(req)
	if err != nil {
		return 0, nil, err
	}
	if _, err := s.nodeSessionTarget(c, principal, req.NodeID, req.AgentID, req.SessionID); err != nil {
		return 0, nil, err
	}
	session, err := s.store.UpdateNodeAgentSession(c, principal, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, session, nil
}

func normalizeUpdateNodeAgentSessionRequest(
	req domain.UpdateSessionRequest,
) (domain.UpdateSessionRequest, error) {
	hasName := req.SessionName != nil
	hasPaxConfig := req.PaxConfig.CWD != "" || req.PaxConfig.ApprovalMode != ""
	hasArchived := req.Archived != nil
	if err := validateSessionUpdateShape(
		hasName,
		req.UseReportedName,
		hasPaxConfig,
		hasArchived,
	); err != nil {
		return domain.UpdateSessionRequest{}, err
	}
	if hasName {
		name := strings.TrimSpace(*req.SessionName)
		if name == "" {
			return domain.UpdateSessionRequest{}, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "name must not be empty",
			}
		}
		if utf8.RuneCountInString(name) > maxSessionNameLength {
			return domain.UpdateSessionRequest{}, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "name must be at most 120 characters",
			}
		}
		req.SessionName = &name
	}
	if hasPaxConfig && req.PaxConfig.CWD != "" {
		return domain.UpdateSessionRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "pax_config.cwd is create-only and cannot be changed",
		}
	}
	mode := strings.TrimSpace(req.PaxConfig.ApprovalMode)
	if hasPaxConfig && mode == "" {
		return domain.UpdateSessionRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "pax_config.approval_mode is required",
		}
	}
	if hasPaxConfig && !domain.IsSessionApprovalMode(mode) {
		return domain.UpdateSessionRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "pax_config.approval_mode must be manual or auto_approve_all",
		}
	}
	if hasPaxConfig {
		req.PaxConfig.ApprovalMode = mode
	}
	return req, nil
}

func validateSessionUpdateShape(
	hasName bool,
	useReportedName bool,
	hasPaxConfig bool,
	hasArchived bool,
) error {
	if !hasName && !useReportedName && !hasPaxConfig && !hasArchived {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "name, use_reported_name, archived, or pax_config is required",
		}
	}
	if hasName && useReportedName {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "name and use_reported_name cannot be combined",
		}
	}
	if hasPaxConfig && (hasName || useReportedName) {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "session name updates cannot be combined with pax_config",
		}
	}
	if hasArchived && (hasName || useReportedName || hasPaxConfig) {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "archived cannot be combined with other session updates",
		}
	}
	return nil
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
	beforeID int64,
	afterSeq int64,
	beforeSeq int64,
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
	return s.listSessionHistory(c, agentID, sessionID, limit, beforeID, afterSeq, beforeSeq)
}

func (s *Service) ListSessionHistory(
	c context.Context,
	meta auth.RequestMetadata,
	sessionID string,
	limit int,
	beforeID int64,
	afterSeq int64,
	beforeSeq int64,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	if sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "session_id is required",
		}
	}
	session, err := s.store.GetSession(c, principal, sessionID)
	if err != nil {
		return 0, nil, err
	}
	return s.listSessionHistory(
		c,
		session.AgentID,
		session.SessionID,
		limit,
		beforeID,
		afterSeq,
		beforeSeq,
	)
}

func (s *Service) listSessionHistory(
	c context.Context,
	agentID string,
	sessionID string,
	limit int,
	beforeID int64,
	afterSeq int64,
	beforeSeq int64,
) (int, any, error) {
	if limit <= 0 {
		limit = defaultHistoryPageSize
	}
	if limit > maxHistoryPageSize {
		limit = maxHistoryPageSize
	}
	if beforeID < 0 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "before_id must be non-negative",
		}
	}
	if afterSeq < 0 || beforeSeq < 0 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "before_seq and after_seq must be non-negative",
		}
	}
	if s.historyReconciler != nil {
		if err := s.historyReconciler(c, agentID, sessionID); err != nil {
			return 0, nil, err
		}
	}
	// Prefer the seq cursor unless the caller is a legacy client that passed an
	// explicit before_id (which forces the id-ordered path).
	useSeq := beforeID == 0 || afterSeq > 0 || beforeSeq > 0
	var page domain.MessageHistoryPage
	var err error
	if useSeq {
		page, err = s.store.ListMessageHistoryPageBySeq(
			c,
			agentID,
			sessionID,
			afterSeq,
			beforeSeq,
			limit,
		)
	} else {
		page, err = s.store.ListMessageHistoryPage(c, agentID, sessionID, beforeID, limit)
	}
	if err != nil {
		return 0, nil, err
	}
	messageIDs := make([]string, 0, len(page.Messages))
	for _, message := range page.Messages {
		messageIDs = append(messageIDs, message.MessageID)
	}
	partsByMessageID, err := s.store.ListMessagePartsByMessageIDs(c, messageIDs)
	if err != nil {
		return 0, nil, err
	}
	history := make([]domain.MessageWithParts, 0, len(page.Messages))
	for _, message := range page.Messages {
		history = append(history, domain.MessageWithParts{
			Message: message,
			Parts:   partsByMessageID[message.MessageID],
		})
	}
	history = domain.NormalTranscriptMessages(history)
	return http.StatusOK, map[string]any{
		"messages": history,
		"pagination": domain.MessageHistoryPagination{
			NextBeforeID:  page.NextBeforeID,
			HasMore:       page.HasMore,
			HeadSeq:       page.HeadSeq,
			HasOlder:      page.HasOlder,
			HasNewer:      page.HasNewer,
			NextBeforeSeq: page.NextBeforeSeq,
			NextAfterSeq:  page.NextAfterSeq,
		},
	}, nil
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

func (s *Service) NodeBrowserControl(
	c context.Context, meta auth.RequestMetadata, nodeID, operation string, payload json.RawMessage,
) (int, any, error) {
	if len(payload) > 16384 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "browser payload is too large",
		}
	}
	switch operation {
	case "state",
		"policy",
		"decide",
		"revoke",
		"secret",
		"resume_sensitive",
		"view",
		"vnc_open",
		"vnc_exchange",
		"vnc_close":
	default:
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "unsupported browser operation",
		}
	}
	return s.queryNodeDaemon(c, meta, nodeID, map[string]any{
		"type": "browser.control", "browser_control": map[string]any{"operation": operation, "payload": payload},
	})
}

func validateDaemonMaintenanceOptions(
	shutdownGrace, idleGrace, drainTimeout *int,
	reason string,
) error {
	if shutdownGrace != nil &&
		(*shutdownGrace < 1 || *shutdownGrace > 60) {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "shutdown_grace_seconds must be between 1 and 60",
		}
	}
	if idleGrace != nil &&
		(*idleGrace < 1 || *idleGrace > 60) {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "idle_grace_seconds must be between 1 and 60",
		}
	}
	if drainTimeout != nil &&
		(*drainTimeout < 1 || *drainTimeout > 3600) {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "drain_timeout_seconds must be between 1 and 3600",
		}
	}
	if len(reason) > 512 {
		return apperr.Error{
			Status: http.StatusBadRequest, Message: "reason must not exceed 512 bytes",
		}
	}
	return nil
}

func sessionRuntimeResetResponse(
	ackRaw []byte,
	expectedTurnInstanceID, commandID string,
) (int, any, error) {
	var ack struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result *struct {
			SessionRuntimeReset *struct {
				Status             string `json:"status"`
				ProjectionRevision uint64 `json:"projection_revision"`
			} `json:"session_runtime_reset"`
		} `json:"result"`
	}
	if err := json.Unmarshal(ackRaw, &ack); err != nil {
		return 0, nil, apperr.Error{
			Status: http.StatusBadGateway, Message: "invalid session runtime reset acknowledgement",
		}
	}
	if !ack.OK || ack.Result == nil || ack.Result.SessionRuntimeReset == nil {
		message := "session runtime reset was rejected"
		if ack.Error != nil && strings.TrimSpace(ack.Error.Message) != "" {
			message = ack.Error.Message
		}
		return 0, nil, apperr.Error{Status: http.StatusConflict, Message: message}
	}
	reset := ack.Result.SessionRuntimeReset
	if reset.Status == "conflict" {
		return 0, nil, apperr.Error{
			Status: http.StatusConflict, Message: "session runtime turn has changed",
		}
	}
	return http.StatusAccepted, map[string]any{
		"status":                    "accepted_pending",
		"reset_status":              reset.Status,
		"projection_revision":       reset.ProjectionRevision,
		"expected_turn_instance_id": expectedTurnInstanceID,
		"command_id":                commandID,
	}, nil
}
