package manager

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

// Snapshot processing never waits for a queue query or a network operation.
// Saturated/repeated wakes are retried by the next periodic paxd snapshot.
func (s *Service) wakeQueuedTurns(
	ctx context.Context,
	node Node,
	snapshot domain.AgentRuntimeSnapshot,
) {
	if s.conversationTurns == nil || s.conversationTurns.store == nil {
		return
	}
	if _, loaded := s.queueChecks.LoadOrStore(snapshot.AgentID, struct{}{}); loaded {
		return
	}
	select {
	case s.queueCheckSlots <- struct{}{}:
	default:
		s.queueChecks.Delete(snapshot.AgentID)
		return
	}
	go func() {
		defer s.queueChecks.Delete(snapshot.AgentID)
		defer func() { <-s.queueCheckSlots }()
		workCtx, cancel := context.WithTimeout(s.queueContext, 10*time.Second)
		defer cancel()
		turns, err := s.conversationTurns.store.ListQueuedTurns(
			workCtx,
			node.NodeID,
			snapshot.AgentID,
		)
		if err != nil {
			logging.Warn(workCtx, "queued turn lookup failed", logging.Err(err))
			return
		}
		active := make(map[string]domain.ActiveTurnSnapshot, len(snapshot.ActiveTurns))
		for _, turn := range snapshot.ActiveTurns {
			active[turn.NativeSessionID] = turn
		}
		for _, turn := range turns {
			running, busy := active[turn.NativeID]
			if turn.State == "sending" {
				if busy && running.TurnInstanceID == turn.TurnID {
					s.finishQueuedTurn(workCtx, turn, true)
				} else if s.clock().Sub(turn.UpdatedAt) > 2*time.Minute {
					// A vanished sender must never cause an automatic duplicate prompt.
					s.finishQueuedTurn(workCtx, turn, false)
				}
				continue
			}
			if busy || turn.NativeID == "" {
				continue
			}
			s.startQueuedTurn(workCtx, turn, snapshot)
		}
	}()
}

func (s *Service) startQueuedTurn(
	ctx context.Context,
	turn domain.QueuedTurn,
	snapshot domain.AgentRuntimeSnapshot,
) {
	select {
	case s.queueRunSlots <- struct{}{}:
	default:
		return
	}
	user, authErr := s.store.GetUser(ctx, turn.OwnerID)
	if authErr == nil {
		var session domain.AgentSession
		session, authErr = s.store.GetSession(ctx, UserPrincipal{User: user}, turn.SessionID)
		if authErr == nil &&
			(session.AgentID != turn.AgentID || session.NodeID != turn.NodeID || session.Transport == domain.SessionTransportE2EE) {
			authErr = domain.ErrNotFound
		}
	}
	if authErr != nil {
		<-s.queueRunSlots
		return
	}
	conn, release, err := s.acpTunnels.claimStructuredAny(
		turn.AgentID,
		turn.SessionID,
		turn.SessionID,
		turn.NativeID,
		"",
	)
	if err != nil {
		<-s.queueRunSlots
		return
	}
	claimed, err := s.conversationTurns.store.ClaimQueuedTurn(ctx, turn, snapshot)
	if err != nil || !claimed {
		release()
		<-s.queueRunSlots
		if err != nil {
			logging.Warn(ctx, "queued turn claim failed", logging.Err(err))
		}
		return
	}
	go func() {
		defer release()
		defer func() { <-s.queueRunSlots }()
		runCtx, cancel := context.WithCancel(s.queueContext)
		defer cancel()
		runner := conversationRunner{service: s, agentConn: conn, managerSessionID: turn.SessionID}
		session := conversationSession{managerID: turn.SessionID, nativeID: turn.NativeID}
		sub := conn.subscribeSSE(turn.SessionID)
		defer conn.unsubscribeSSE(sub)
		writer := &queuedTurnWriter{header: make(http.Header)}
		completed, err := s.promptConversationOnce(
			runCtx,
			writer,
			writer,
			&runner,
			session,
			sub,
			turn.TurnID,
			[]map[string]any{{"type": "text", "text": turn.Input}},
		)
		if err != nil {
			logging.Warn(runCtx, "queued turn execution interrupted", logging.Err(err))
		}
		// Normal completion proves acceptance. Active/approval snapshots can retire
		// the queue row sooner; conditional deletion cannot affect a newer turn.
		s.finishQueuedTurn(runCtx, turn, completed || writer.accepted)
	}()
}

func (s *Service) finishQueuedTurn(ctx context.Context, turn domain.QueuedTurn, accepted bool) {
	if err := s.conversationTurns.store.FinishQueuedTurn(ctx, turn, accepted); err != nil {
		logging.Warn(ctx, "queued turn acknowledgement failed", logging.Err(err))
	}
}

// Output remains durable through the normal ACP history/observer pipeline.
// No browser response is required to run a queued turn.
type queuedTurnWriter struct {
	header   http.Header
	accepted bool
}

func (w *queuedTurnWriter) Header() http.Header { return w.header }
func (w *queuedTurnWriter) Write(payload []byte) (int, error) {
	if bytes.Contains(payload, []byte(`"type":"approval_required"`)) {
		w.accepted = true
	}
	return len(payload), nil
}
func (w *queuedTurnWriter) WriteHeader(int) {}
func (w *queuedTurnWriter) Flush()          {}
