package userapi

import (
	"context"
	"net/http"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
)

// GetUserSession resolves canonical metadata without listing session history.
func (s *Service) GetUserSession(
	ctx context.Context,
	meta auth.RequestMetadata,
	sessionID string,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	if sessionID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "session_id is required",
		}
	}
	session, err := s.store.GetSession(ctx, principal, sessionID)
	if err != nil {
		return 0, nil, err
	}
	if session.NodeID == "" {
		agent, agentErr := s.store.GetAgent(ctx, principal, session.AgentID)
		if agentErr != nil {
			return 0, nil, agentErr
		}
		session.NodeID = agent.NodeID
	}
	if err := s.fillSessionHistoryHead(ctx, &session); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, session, nil
}
