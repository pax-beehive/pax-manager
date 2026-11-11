package manager

import (
	"context"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

type workspaceAccessLifecycleMiddleware struct {
	service *Service
}

func (s *Service) prepareConversationSearchAccess(
	ctx context.Context, runner *conversationRunner, session conversationSession, turnID string,
) error {
	if runner.secretaryJob != nil {
		return nil
	}
	if runner.searchAccessOwnerID != "" &&
		runner.searchAccessOwnerID != runner.agentConn.ownerUserID {
		if runner.searchAccess != nil {
			return workspaceAccessError(http.StatusForbidden,
				"only the agent owner can preauthorize workspace search")
		}
		return nil
	}
	return s.prepareTurnSearchAccess(ctx, runner.agentConn.nodeID, runner.agentConn.agentID,
		session.managerID, turnID, runner.searchAccess)
}

func (m workspaceAccessLifecycleMiddleware) HandleACPFrame(
	ctx context.Context, frame *acpFrameContext, next acpFrameHandler,
) error {
	if frame.agent == nil || frame.direction != acpAgentToUser ||
		(len(frame.frame.Result) == 0 && len(frame.frame.Error) == 0) {
		return next(ctx, frame)
	}
	if len(frame.frame.Error) > 0 && isConversationACPErrorKind(
		decodeConversationACPError(frame.frame.Error), "session_route_missing",
	) {
		return next(ctx, frame)
	}
	state := m.service.acpRuntime.CurrentCorrelation(frame.agent.agentID, frame.managerSessionID)
	if frame.requestKind != "session/prompt" &&
		(state.ActivePromptRequestID == "" || acpRequestID(frame.frame.ID) != state.ActivePromptRequestID) {
		return next(ctx, frame)
	}
	turnID := firstNonEmpty(frame.businessTurnID, state.ActiveTurnID)
	if turnID != "" {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err := m.service.expireTurnSearchAccess(endCtx, frame.agent.nodeID,
			frame.agent.agentID, frame.managerSessionID, turnID)
		cancel()
		if err != nil {
			logging.Warn(ctx, "turn search authorization expiry failed", logging.Err(err))
		}
	}
	return next(ctx, frame)
}

func (s *Service) expireConversationSearchAccess(
	ctx context.Context, runner *conversationRunner, session conversationSession,
) {
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.expireTurnSearchAccess(endCtx, runner.agentConn.nodeID,
		runner.agentConn.agentID, session.managerID, runner.turnID); err != nil {
		logging.Warn(ctx, "turn search authorization expiry failed", logging.Err(err))
	}
}
