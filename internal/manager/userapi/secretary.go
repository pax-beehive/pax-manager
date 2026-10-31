package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type SecretaryFleet interface {
	GetAgent(context.Context, domain.UserPrincipal, string) (domain.Agent, error)
}

type SecretaryAvailability func(context.Context, string, string) bool

type SecretaryService struct {
	principal PrincipalResolver
	store     domain.SecretaryStore
	fleet     SecretaryFleet
	available SecretaryAvailability
}

func NewSecretaryService(
	principal PrincipalResolver,
	store domain.SecretaryStore,
	fleet SecretaryFleet,
	available SecretaryAvailability,
) *SecretaryService {
	return &SecretaryService{principal: principal, store: store, fleet: fleet, available: available}
}

type SecretaryResponse struct {
	AgentID         string     `json:"agent_id"`
	Enabled         bool       `json:"enabled"`
	Revision        int64      `json:"revision"`
	State           string     `json:"state"`
	PendingCount    int        `json:"pending_count"`
	AgentAvailable  bool       `json:"agent_available"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
}

func (s *SecretaryService) owner(
	ctx context.Context,
	meta auth.RequestMetadata,
) (domain.UserPrincipal, error) {
	if s.principal == nil {
		return domain.UserPrincipal{}, domain.ErrUnauthorized
	}
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return domain.UserPrincipal{}, err
	}
	if principal.User.UserID == "" {
		return domain.UserPrincipal{}, domain.ErrUnauthorized
	}
	if s.store == nil {
		return domain.UserPrincipal{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary is unavailable",
		)
	}
	return principal, nil
}

func (s *SecretaryService) Get(
	ctx context.Context,
	meta auth.RequestMetadata,
) (SecretaryResponse, error) {
	principal, err := s.owner(ctx, meta)
	if err != nil {
		return SecretaryResponse{}, err
	}
	return s.status(ctx, principal.User.UserID)
}

func (s *SecretaryService) Update(
	ctx context.Context,
	meta auth.RequestMetadata,
	request domain.SecretarySettingsUpdate,
) (SecretaryResponse, error) {
	principal, err := s.owner(ctx, meta)
	if err != nil {
		return SecretaryResponse{}, err
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	if request.ExpectedRevision < 0 || len(request.AgentID) > 512 ||
		(request.Enabled && request.AgentID == "") {
		return SecretaryResponse{}, searchHTTPError(
			http.StatusBadRequest,
			"Select a valid Secretary agent and settings revision",
		)
	}
	if request.AgentID != "" {
		if err = s.validateAgent(ctx, principal, request); err != nil {
			return SecretaryResponse{}, err
		}
	}
	if _, err = s.store.UpdateSecretarySettings(ctx, principal.User.UserID, request); err != nil {
		return SecretaryResponse{}, err
	}
	return s.status(ctx, principal.User.UserID)
}

func (s *SecretaryService) validateAgent(
	ctx context.Context,
	principal domain.UserPrincipal,
	request domain.SecretarySettingsUpdate,
) error {
	if s.fleet == nil {
		return searchHTTPError(http.StatusServiceUnavailable, "Agent inventory is unavailable")
	}
	agent, err := s.fleet.GetAgent(ctx, principal, request.AgentID)
	if err == nil && agent.OwnerUserID == principal.User.UserID &&
		agent.AgentID == request.AgentID {
		return nil
	}
	if err == nil {
		err = domain.ErrNotFound
	}
	if !request.Enabled && errors.Is(err, domain.ErrNotFound) {
		current, readErr := s.store.GetSecretarySettings(ctx, principal.User.UserID)
		if readErr != nil {
			return readErr
		}
		if current.AgentID == request.AgentID {
			return nil
		}
	}
	return err
}

func (s *SecretaryService) Run(
	ctx context.Context,
	meta auth.RequestMetadata,
) (SecretaryResponse, error) {
	principal, err := s.owner(ctx, meta)
	if err != nil {
		return SecretaryResponse{}, err
	}
	status, err := s.store.GetSecretaryStatus(ctx, principal.User.UserID)
	if err != nil {
		return SecretaryResponse{}, err
	}
	if !status.Enabled || status.AgentID == "" {
		return SecretaryResponse{}, searchHTTPError(
			http.StatusConflict,
			"Enable Secretary before requesting a run",
		)
	}
	if err = s.store.RetrySecretaryJobs(ctx, principal.User.UserID); err != nil {
		return SecretaryResponse{}, err
	}
	return s.status(ctx, principal.User.UserID)
}

func (s *SecretaryService) status(ctx context.Context, owner string) (SecretaryResponse, error) {
	status, err := s.store.GetSecretaryStatus(ctx, owner)
	if err != nil {
		return SecretaryResponse{}, err
	}
	available := status.AgentID != "" && s.available != nil &&
		s.available(ctx, owner, status.AgentID)
	result := SecretaryResponse{
		AgentID:         status.AgentID,
		Enabled:         status.Enabled,
		Revision:        status.Revision,
		State:           "idle",
		PendingCount:    status.PendingCount,
		AgentAvailable:  available,
		LastCompletedAt: status.LastCompletedAt,
	}
	if status.LastError != "" {
		result.LastError = "The last summary task did not finish. Retry it when the agent is available."
	}
	switch {
	case !status.Enabled:
		result.State = "disabled"
	case !available:
		result.State = "waiting_for_agent"
	case status.Processing:
		result.State = "processing"
	case status.LastError != "":
		result.State = "error"
	}
	return result, nil
}
