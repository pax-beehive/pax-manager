package userapi

import (
	"context"
	"crypto/rand"
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
	ExecutionState  string     `json:"execution_state,omitempty"`
	BlockedReason   string     `json:"blocked_reason,omitempty"`
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
	requestID string,
) (domain.SecretaryRunReceipt, error) {
	principal, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryRunReceipt{}, err
	}
	if requestID == "" {
		requestID = "srq_" + rand.Text()
	}
	if !domain.ValidSecretaryRunRequestID(requestID) {
		return domain.SecretaryRunReceipt{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid Secretary run request ID",
		)
	}
	store, ok := s.store.(domain.SecretaryRunStore)
	if !ok {
		return domain.SecretaryRunReceipt{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary run queue is unavailable",
		)
	}
	receipt, err := store.RequestSecretaryRun(ctx, principal.User.UserID, requestID)
	if errors.Is(err, domain.ErrConflict) {
		return domain.SecretaryRunReceipt{}, searchHTTPError(
			http.StatusConflict,
			"Enable Secretary before requesting a run",
		)
	}
	return receipt, err
}

func (s *SecretaryService) status(ctx context.Context, owner string) (SecretaryResponse, error) {
	status, err := s.store.GetSecretaryStatus(ctx, owner)
	if err != nil {
		return SecretaryResponse{}, err
	}
	available := status.AgentID != "" && s.available != nil &&
		s.available(ctx, owner, status.AgentID)
	result := SecretaryResponse{
		ExecutionState:  safeSecretaryExecutionState(status.ExecutionState),
		AgentID:         status.AgentID,
		Enabled:         status.Enabled,
		Revision:        status.Revision,
		State:           "idle",
		PendingCount:    status.PendingCount,
		AgentAvailable:  available,
		LastCompletedAt: status.LastCompletedAt,
	}
	if result.ExecutionState != "" {
		result.BlockedReason = safeSecretaryBlockedReason(status.BlockedReason)
	}
	connectionBackoff := result.ExecutionState == "waiting" &&
		result.BlockedReason == "connection_backoff"
	connectionExhausted := result.ExecutionState == "blocked" &&
		result.BlockedReason == "connection_retries_exhausted"
	if connectionExhausted {
		result.LastError = "Secretary could not connect to the agent after automatic retries. Retry the run when the agent is available."
	} else if !connectionBackoff &&
		(status.LastError != "" || secretaryExecutionNeedsAttention(result.ExecutionState)) {
		result.LastError = "The last summary task did not finish. Retry it when the agent is available."
	}
	switch {
	case !status.Enabled:
		result.State = "disabled"
	case connectionBackoff:
		result.State = "waiting"
	case connectionExhausted:
		result.State = "error"
	case !available:
		result.State = "waiting_for_agent"
	case result.ExecutionState == "needs_recovery":
		result.State = "error"
	case status.Processing:
		result.State = "processing"
	case secretaryExecutionNeedsAttention(result.ExecutionState):
		result.State = "error"
	case status.LastError != "":
		result.State = "error"
	}
	return result, nil
}

func safeSecretaryExecutionState(value string) string {
	switch value {
	case "running", "needs_recovery", "unknown", "blocked", "output_received", "queued", "waiting":
		return value
	default:
		return ""
	}
}

func safeSecretaryBlockedReason(value string) string {
	switch value {
	case "execution_unknown",
		"lease_expired",
		"invalid_output",
		"execution_failed",
		"canceled",
		"retry_required",
		"connection_backoff",
		"connection_retries_exhausted":
		return value
	default:
		return ""
	}
}

func secretaryExecutionNeedsAttention(value string) bool {
	return value == "unknown" || value == "blocked" || value == "needs_recovery"
}
