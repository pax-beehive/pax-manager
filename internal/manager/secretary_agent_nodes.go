package manager

import (
	"context"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

type secretaryAgentExecutor struct {
	service *Service
}

func (e secretaryAgentExecutor) Execute(
	ctx context.Context,
	request secretary.ExecutionRequest,
) (secretary.ExecutionResult, error) {
	return e.service.executeSecretaryAgent(ctx, request)
}

func (e secretaryAgentExecutor) Recover(
	ctx context.Context,
	request secretary.ExecutionRequest,
) (secretary.ExecutionResult, bool, error) {
	return e.service.recoverSecretaryAgent(ctx, request)
}

func (s *Service) recoverSecretaryAgent(
	ctx context.Context,
	request secretary.ExecutionRequest,
) (secretary.ExecutionResult, bool, error) {
	var result secretary.ExecutionResult
	if !validSecretaryRecoveryRequest(request) {
		return result, false, domain.ErrSecretaryFenced
	}
	user, err := s.store.GetUser(ctx, request.OwnerID)
	if err != nil {
		return result, false, err
	}
	principal := domain.UserPrincipal{User: user}
	agent, err := s.store.GetAgent(ctx, principal, request.AgentID)
	if err != nil {
		return result, false, err
	}
	if agent.OwnerUserID != request.OwnerID {
		return result, false, domain.ErrNotFound
	}
	result.SessionID = secretary.ExecutionSessionID(request.OwnerID, request.RunID)
	session, err := s.store.GetSession(ctx, principal, result.SessionID)
	if errors.Is(err, domain.ErrNotFound) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if session.AgentID != request.AgentID || session.ArchivedAt != nil ||
		session.Transport == domain.SessionTransportE2EE {
		return result, false, domain.ErrNotFound
	}
	var completed bool
	result.Text, _, completed, err = s.readSecretaryExecution(
		ctx,
		request.AgentID,
		result.SessionID,
		secretary.ExecutionTurnID(request.OwnerID, request.RunID),
	)
	if err != nil || !completed {
		return result, false, err
	}
	result, err = s.finishSecretaryExecution(ctx, request, result)
	return result, true, err
}

func validSecretaryRecoveryRequest(request secretary.ExecutionRequest) bool {
	attempt := request.Job.Attempt
	return request.OwnerID != "" && request.AgentID != "" && request.Job.BatchID != "" &&
		attempt != nil &&
		attempt.BatchID == request.Job.BatchID &&
		attempt.ID == request.RunID &&
		request.Job.AttemptID == attempt.ID &&
		request.Job.OwnerUserID == request.OwnerID &&
		request.Job.AgentID == request.AgentID &&
		(attempt.State == "running" || attempt.State == "unknown")
}
