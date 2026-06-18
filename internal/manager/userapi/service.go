package userapi

import (
	"context"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type Store interface {
	GetUser(ctx context.Context, userID string) (domain.User, error)
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	CreateRegistrationToken(
		ctx context.Context,
		ownerUserID string,
		tokenHash string,
		expiresAt *time.Time,
	) error
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
	ListAgents(ctx context.Context, principal domain.UserPrincipal) ([]domain.Agent, error)
	ListNodes(ctx context.Context, principal domain.UserPrincipal) ([]domain.Node, error)
	GetNode(ctx context.Context, principal domain.UserPrincipal, nodeID string) (domain.Node, error)
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
	store     Store
	clock     func() time.Time
	principal PrincipalResolver
	secrets   SecretIssuer
}

func NewService(
	store Store,
	clock func() time.Time,
	principal PrincipalResolver,
	secrets SecretIssuer,
) *Service {
	return &Service{
		store:     store,
		clock:     clock,
		principal: principal,
		secrets:   secrets,
	}
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
			Message: "message_type must be chat, steer, or command",
		}
	}
	if req.NodeID == "" && req.SessionID != "" {
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
