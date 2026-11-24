package manager

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

const conversationSlotWait = time.Minute

func (r *conversationRunner) releaseConversationClaim() {
	if r.releaseStartupClaim != nil {
		r.releaseStartupClaim()
		r.releaseStartupClaim = nil
	}
}

// Only the user conversation owns a releasable startup claim. Background
// deliveries and Secretary use their own durable retry schedules.
func (r *conversationRunner) retryNewConversationSession(
	ctx context.Context,
	params map[string]any,
	rejection error,
	deadline time.Time,
) (conversationResponse, error) {
	retryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for delay := time.Second; ; delay = min(2*delay, 4*time.Second) {
		// A legacy tunnel claim is agent-wide. Other sessions can use it
		// while this request waits for the pool to admit another session.
		r.releaseConversationClaim()
		select {
		case <-retryCtx.Done():
			return conversationResponse{}, sessionSlotWaitError(ctx, rejection)
		case <-time.After(delay):
		}

		if retryCtx.Err() != nil {
			return conversationResponse{}, sessionSlotWaitError(ctx, rejection)
		}
		if claimErr := r.reclaimConversationSlot(retryCtx, deadline); claimErr != nil {
			if retryCtx.Err() != nil || !time.Now().Before(deadline) {
				return conversationResponse{}, sessionSlotWaitError(ctx, rejection)
			}
			return conversationResponse{}, claimErr
		}
		if retryCtx.Err() != nil {
			return conversationResponse{}, sessionSlotWaitError(ctx, rejection)
		}
		response, err := r.requestOnce(retryCtx, "session/new", params, nil)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && retryCtx.Err() != nil {
			return conversationResponse{}, apperr.Error{
				Status:  http.StatusGatewayTimeout,
				Message: "timed out waiting for session/new confirmation; session creation outcome is unknown",
			}
		}
		if !isUndispatchedSessionNew(err) {
			return response, err
		}
		rejection = err
	}
}

func (r *conversationRunner) reclaimConversationSlot(
	ctx context.Context,
	deadline time.Time,
) error {
	original := r.agentConn
	conn, release, err := r.service.acpTunnels.claimStructuredAnyWait(
		ctx,
		time.Until(deadline),
		conversationTunnelClaimTick,
		original.agentID,
		r.managerSessionID,
		"",
	)
	if err != nil {
		return err
	}
	if conn.agentID != original.agentID || conn.nodeID != original.nodeID ||
		conn.ownerUserID != original.ownerUserID {
		release()
		return apperr.Error{
			Status:  http.StatusConflict,
			Message: "agent tunnel identity changed during session startup",
		}
	}
	r.agentConn = conn
	r.releaseStartupClaim = release
	return nil
}

func isUndispatchedSessionNew(err error) bool {
	var acpErr conversationACPError
	return errors.As(err, &acpErr) && acpErr.code == "-32003" &&
		acpErr.kind == "slot_unavailable" && acpErr.retryable
}

// If nothing was dispatched, preserve the daemon's last rejection on exhaustion.
func sessionSlotWaitError(ctx context.Context, rejection error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return rejection
}
