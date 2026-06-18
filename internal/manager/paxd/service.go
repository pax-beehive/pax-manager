package paxd

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type Store interface {
	RegisterAgent(
		ctx context.Context,
		owner domain.User,
		req domain.RegisterAgentRequest,
		apiKeyHash string,
	) (domain.Agent, error)
	AuthenticateNode(ctx context.Context, apiKeyHash string) (domain.Node, error)
	UpsertAgentStatus(ctx context.Context, report domain.AgentStatusReport) error
	PullMailbox(
		ctx context.Context,
		agentID string,
		sessionID string,
		offset int64,
		limit int,
	) (domain.MailboxPull, error)
	UpdateOffset(ctx context.Context, agentID string, offset int64) error
	MarkMessageResult(
		ctx context.Context,
		agentID string,
		messageID string,
		req domain.MessageResultRequest,
	) error
	RegisterNode(
		ctx context.Context,
		owner domain.User,
		req domain.RegisterNodeRequest,
		apiKeyHash string,
	) (domain.Node, error)
	CreateNodeAgent(
		ctx context.Context,
		principal domain.UserPrincipal,
		req domain.CreateAgentRequest,
	) (domain.Agent, domain.MailboxMessage, error)
	UpsertNodeStatus(ctx context.Context, node domain.Node, report domain.NodeStatusReport) error
	PullNodeMailbox(
		ctx context.Context,
		nodeID string,
		agentID string,
		sessionID string,
		offset int64,
		limit int,
	) (domain.MailboxPull, error)
	UpdateNodeOffset(ctx context.Context, nodeID string, offset int64) error
	MarkNodeMessageResult(
		ctx context.Context,
		nodeID string,
		messageID string,
		req domain.MessageResultRequest,
	) error
	MarkNodeMessageDelivered(
		ctx context.Context,
		nodeID string,
		req domain.MarkDeliveredRequest,
	) error
	CreateNodeOutboundMessage(
		ctx context.Context,
		node domain.Node,
		req domain.CreateOutboundMessageRequest,
	) (domain.MailboxMessage, error)
}

type RegistrationOwnerResolver interface {
	RegistrationOwner(context.Context, auth.RequestMetadata) (domain.User, error)
}

type SecretIssuer interface {
	New(prefix string) (string, error)
	Hash(secret string) string
}

type Service struct {
	store             Store
	clock             func() time.Time
	registrationOwner RegistrationOwnerResolver
	secrets           SecretIssuer
}

func NewService(
	store Store,
	clock func() time.Time,
	registrationOwner RegistrationOwnerResolver,
	secrets SecretIssuer,
) *Service {
	return &Service{
		store:             store,
		clock:             clock,
		registrationOwner: registrationOwner,
		secrets:           secrets,
	}
}

func (s *Service) RegisterAgent(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.RegisterAgentRequest,
) (int, any, error) {
	if req.Hostname == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "hostname is required"}
	}
	if req.OS == "" {
		req.OS = "unknown"
	}
	owner, err := s.registrationOwner.RegistrationOwner(c, meta)
	if err != nil {
		return 0, nil, err
	}

	apiKey, err := s.secrets.New("pax")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate api key",
		}
	}

	agent, err := s.store.RegisterAgent(c, owner, req, s.secrets.Hash(apiKey))
	if err != nil {
		return 0, nil, err
	}

	log.Printf("agent registered: %s", agent.AgentID)
	return http.StatusOK, domain.RegisterAgentResponse{
		AgentID: agent.AgentID,
		APIKey:  apiKey,
	}, nil
}

func (s *Service) RegisterNode(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.RegisterNodeRequest,
) (int, any, error) {
	if req.Hostname == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "hostname is required"}
	}
	if req.OS == "" {
		req.OS = "unknown"
	}
	owner, err := s.registrationOwner.RegistrationOwner(c, meta)
	if err != nil {
		return 0, nil, err
	}
	apiKey, err := s.secrets.New("pax")
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "could not generate api key",
		}
	}
	node, err := s.store.RegisterNode(c, owner, req, s.secrets.Hash(apiKey))
	if err != nil {
		return 0, nil, err
	}
	log.Printf("node registered: %s", node.NodeID)
	return http.StatusOK, domain.RegisterNodeResponse{NodeID: node.NodeID, APIKey: apiKey}, nil
}

func (s *Service) RegisterNodeAgent(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.RegisterNodeAgentRequest,
) (int, any, error) {
	paxKey := firstNonEmpty(
		meta.Header("X-Pax-Key"),
		auth.BearerToken(meta.Header("Authorization")),
	)
	registrationToken := meta.Header("X-Registration-Token")
	if paxKey != "" && registrationToken != "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "provide either X-Pax-Key or X-Registration-Token, not both",
		}
	}

	var node domain.Node
	apiKey := ""
	if paxKey != "" {
		var err error
		node, err = s.store.AuthenticateNode(c, s.secrets.Hash(paxKey))
		if err != nil {
			return 0, nil, err
		}
	} else {
		if registrationToken == "" {
			return 0, nil, apperr.Error{
				Status:  http.StatusUnauthorized,
				Message: "missing pax key or registration token",
			}
		}
		if req.Node.Hostname == "" {
			return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "node.hostname is required"}
		}
		if req.Node.OS == "" {
			req.Node.OS = "unknown"
		}
		owner, err := s.registrationOwner.RegistrationOwner(c, meta)
		if err != nil {
			return 0, nil, err
		}
		apiKey, err = s.secrets.New("pax")
		if err != nil {
			return 0, nil, apperr.Error{
				Status:  http.StatusInternalServerError,
				Message: "could not generate api key",
			}
		}
		node, err = s.store.RegisterNode(c, owner, req.Node, s.secrets.Hash(apiKey))
		if err != nil {
			return 0, nil, err
		}
	}

	agentReq := req.Agent
	agentReq.NodeID = node.NodeID
	principal := domain.UserPrincipal{User: domain.User{UserID: node.OwnerUserID}}
	agent, _, err := s.store.CreateNodeAgent(c, principal, agentReq)
	if err != nil {
		return 0, nil, err
	}

	return http.StatusOK, domain.RegisterNodeAgentResponse{
		NodeID:  node.NodeID,
		APIKey:  apiKey,
		AgentID: agent.AgentID,
		Agent:   agent,
	}, nil
}

func (s *Service) ReportStatus(
	c context.Context,
	agent domain.Agent,
	report domain.AgentStatusReport,
) (int, any, error) {
	if report.AgentID == "" {
		report.AgentID = agent.AgentID
	}
	if report.AgentID != agent.AgentID {
		return 0, nil, apperr.Error{
			Status:  http.StatusForbidden,
			Message: "agent_id does not match token",
		}
	}
	if report.Timestamp.IsZero() {
		report.Timestamp = s.clock().UTC()
	}

	if err := s.store.UpsertAgentStatus(c, report); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Service) ReportNodeStatus(
	c context.Context,
	node domain.Node,
	report domain.NodeStatusReport,
) (int, any, error) {
	if report.NodeID == "" {
		report.NodeID = node.NodeID
	}
	if report.NodeID != node.NodeID {
		return 0, nil, apperr.Error{
			Status:  http.StatusForbidden,
			Message: "node_id does not match token",
		}
	}
	if report.Timestamp.IsZero() {
		report.Timestamp = s.clock().UTC()
	}
	if err := s.store.UpsertNodeStatus(c, node, report); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) PullNodeMailbox(
	c context.Context,
	node domain.Node,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
) (int, any, error) {
	if limit == 0 {
		limit = 10
	}
	pull, err := s.store.PullNodeMailbox(c, node.NodeID, agentID, sessionID, offset, limit)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, pull, nil
}

func (s *Service) UpdateNodeOffset(
	c context.Context,
	node domain.Node,
	offset int64,
) (int, any, error) {
	if offset < 0 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "offset must be non-negative",
		}
	}
	if err := s.store.UpdateNodeOffset(c, node.NodeID, offset); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) ReportNodeMessageResult(
	c context.Context,
	node domain.Node,
	req domain.MessageResultRequest,
) (int, any, error) {
	if req.MessageID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "message_id is required",
		}
	}
	if req.Status == "" {
		req.Status = "completed"
	}
	if err := s.store.MarkNodeMessageResult(c, node.NodeID, req.MessageID, req); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) MarkNodeMessageDelivered(
	c context.Context,
	node domain.Node,
	req domain.MarkDeliveredRequest,
) (int, any, error) {
	if req.MessageID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "message_id is required",
		}
	}
	if err := s.store.MarkNodeMessageDelivered(c, node.NodeID, req); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) CreateNodeOutboundMessage(
	c context.Context,
	node domain.Node,
	req domain.CreateOutboundMessageRequest,
) (int, any, error) {
	if req.AgentID == "" || req.SessionID == "" || req.Content == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id, session_id, and content are required",
		}
	}
	msg, err := s.store.CreateNodeOutboundMessage(c, node, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, msg, nil
}

func (s *Service) PullMailbox(
	c context.Context,
	agent domain.Agent,
	offset int64,
	limit int,
) (int, any, error) {
	if limit == 0 {
		limit = 10
	}
	pull, err := s.store.PullMailbox(c, agent.AgentID, "", offset, limit)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, pull, nil
}

func (s *Service) PullSessionMailbox(
	c context.Context,
	agent domain.Agent,
	sessionID string,
	offset int64,
	limit int,
) (int, any, error) {
	if sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "session_id is required",
		}
	}
	if limit == 0 {
		limit = 10
	}
	pull, err := s.store.PullMailbox(c, agent.AgentID, sessionID, offset, limit)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, pull, nil
}

func (s *Service) UpdateOffset(
	c context.Context,
	agent domain.Agent,
	offset int64,
) (int, any, error) {
	if offset < 0 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "offset must be non-negative",
		}
	}
	if err := s.store.UpdateOffset(c, agent.AgentID, offset); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}

func (s *Service) ReportMessageResult(
	c context.Context,
	agent domain.Agent,
	req domain.MessageResultRequest,
) (int, any, error) {
	if req.Status == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "status is required"}
	}
	if err := s.store.MarkMessageResult(c, agent.AgentID, req.MessageID, req); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]bool{"ok": true}, nil
}
