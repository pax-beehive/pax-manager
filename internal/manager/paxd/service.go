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
	UpsertAgentStatus(ctx context.Context, report domain.AgentStatusReport) error
	PullMailbox(
		ctx context.Context,
		agentID string,
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
	return http.StatusCreated, domain.RegisterAgentResponse{
		AgentID: agent.AgentID,
		APIKey:  apiKey,
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

func (s *Service) PullMailbox(
	c context.Context,
	agent domain.Agent,
	offset int64,
	limit int,
) (int, any, error) {
	if limit == 0 {
		limit = 10
	}
	pull, err := s.store.PullMailbox(c, agent.AgentID, offset, limit)
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
