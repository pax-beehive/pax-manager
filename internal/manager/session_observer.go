package manager

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

func (s *Service) handleSessionObserverEvents(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	if r.Method != http.MethodGet {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	control, err := s.resolveConversationTurnControlSession(r)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	activePromptID := conversationActivePromptRequestID(control.session)
	activeTurnID := conversationActiveTurnID(control.session)
	if !conversationSessionHasActiveTurn(control.session) {
		_ = writeSessionObserverEvent(w, flusher, conversationEvent{
			Type:      "no_running_turn",
			NodeID:    control.agent.NodeID,
			AgentID:   control.agent.AgentID,
			SessionID: control.session.SessionID,
			Status:    conversationSessionStatus(control.session),
		})
		return
	}
	agentConn, err := s.acpTunnels.findAny(
		control.agent.AgentID,
		control.session.SessionID,
		control.session.NativeID,
		"",
	)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	sub := agentConn.subscribeSSE(control.session.SessionID)
	defer agentConn.unsubscribeSSE(sub)

	timer := time.NewTimer(conversationRequestIdleTimeout)
	defer timer.Stop()
	for {
		select {
		case payload, ok := <-sub.ch:
			if !ok {
				return
			}
			resetConversationIdleTimer(timer)
			if err := writeSessionObserverEvent(w, flusher, conversationEvent{
				Type:      "acp",
				NodeID:    control.agent.NodeID,
				AgentID:   control.agent.AgentID,
				SessionID: control.session.SessionID,
				TurnID:    activeTurnID,
				Frame:     append(json.RawMessage(nil), payload...),
			}); err != nil {
				return
			}
			if sessionObserverPayloadCompletesTurn(payload, activePromptID) {
				_ = writeSessionObserverEvent(w, flusher, conversationEvent{
					Type:      "turn_done",
					NodeID:    control.agent.NodeID,
					AgentID:   control.agent.AgentID,
					SessionID: control.session.SessionID,
					TurnID:    activeTurnID,
					Status:    "done",
				})
				return
			}
		case err, ok := <-sub.terminal:
			if ok && err != nil {
				_ = writeSessionObserverEvent(w, flusher, conversationEvent{
					Type:      "error",
					NodeID:    control.agent.NodeID,
					AgentID:   control.agent.AgentID,
					SessionID: control.session.SessionID,
					Message:   err.Error(),
				})
			}
			return
		case <-timer.C:
			_ = writeSessionObserverEvent(w, flusher, conversationEvent{
				Type:      "error",
				NodeID:    control.agent.NodeID,
				AgentID:   control.agent.AgentID,
				SessionID: control.session.SessionID,
				Message:   "session observer idle timed out",
			})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func sessionObserverPayloadCompletesTurn(payload []byte, activePromptID string) bool {
	if activePromptID == "" {
		return false
	}
	var frame acpJSONRPCMessage
	if json.Unmarshal(payload, &frame) != nil {
		return false
	}
	if acpRequestID(frame.ID) != activePromptID {
		return false
	}
	return len(frame.Result) > 0 || len(frame.Error) > 0
}

func writeSessionObserverEvent(w http.ResponseWriter, flusher http.Flusher, event conversationEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
