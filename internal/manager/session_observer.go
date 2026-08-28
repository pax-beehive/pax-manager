package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
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

	// Resume from the client's cursor (seq refactor): replay durable transcript
	// items past the cursor before joining the live stream, so a reconnect never
	// requires a separate full history refetch. Then advertise the head so the
	// client knows if it is still behind.
	afterSeq := sessionObserverCursor(r)
	headSeq, err := writeSessionSeqCatchup(
		ctx, w, flusher, s.store,
		control.agent.NodeID, control.agent.AgentID, control.session.SessionID, afterSeq,
	)
	if err != nil {
		logging.Error(ctx, "session observer catch-up failed", logging.Err(err))
	}
	_ = writeSessionObserverEvent(w, flusher, conversationEvent{
		Type:      "head",
		NodeID:    control.agent.NodeID,
		AgentID:   control.agent.AgentID,
		SessionID: control.session.SessionID,
		HeadSeq:   headSeq,
	})

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
		// Headers are already flushed as an event stream, so surface the failure
		// as an SSE error rather than an HTTP status.
		_ = writeSessionObserverEvent(w, flusher, conversationEvent{
			Type:      "error",
			NodeID:    control.agent.NodeID,
			AgentID:   control.agent.AgentID,
			SessionID: control.session.SessionID,
			Message:   err.Error(),
		})
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
				if errors.Is(err, errACPSSESubscriberOverflow) {
					// Tell the client the exact watermark to pull from instead of
					// an opaque "reconnect": it resumes via history?after_seq.
					_ = writeSessionObserverEvent(w, flusher, conversationEvent{
						Type:      "resync",
						NodeID:    control.agent.NodeID,
						AgentID:   control.agent.AgentID,
						SessionID: control.session.SessionID,
						HeadSeq:   headSeq,
						Message:   err.Error(),
					})
				} else {
					_ = writeSessionObserverEvent(w, flusher, conversationEvent{
						Type:      "error",
						NodeID:    control.agent.NodeID,
						AgentID:   control.agent.AgentID,
						SessionID: control.session.SessionID,
						Message:   err.Error(),
					})
				}
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

// sessionObserverCursor extracts the resume cursor from the request: the
// explicit after_seq query parameter, or the SSE Last-Event-ID header set by the
// browser EventSource on automatic reconnect.
func sessionObserverCursor(r *http.Request) int64 {
	if v := strings.TrimSpace(r.URL.Query().Get("after_seq")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	if v := strings.TrimSpace(r.Header.Get("Last-Event-ID")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// writeSessionSeqCatchup replays durable transcript items with session_seq >
// afterSeq as ordered "history_item" SSE events (each tagged with its seq as the
// SSE id), so a reconnecting client resumes from its cursor without a separate
// full history refetch. It returns the session's current head seq.
func writeSessionSeqCatchup(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	store domain.Store,
	nodeID string,
	agentID string,
	sessionID string,
	afterSeq int64,
) (int64, error) {
	cursor := afterSeq
	headSeq := int64(0)
	for {
		page, err := store.ListMessageHistoryPageBySeq(ctx, agentID, sessionID, cursor, 0, 200)
		if err != nil {
			return headSeq, err
		}
		if page.HeadSeq > headSeq {
			headSeq = page.HeadSeq
		}
		if len(page.Messages) == 0 {
			return headSeq, nil
		}
		ids := make([]string, 0, len(page.Messages))
		for _, msg := range page.Messages {
			ids = append(ids, msg.MessageID)
		}
		partsByID, err := store.ListMessagePartsByMessageIDs(ctx, ids)
		if err != nil {
			return headSeq, err
		}
		for _, msg := range page.Messages {
			item := domain.MessageWithParts{Message: msg, Parts: partsByID[msg.MessageID]}
			if err := writeSessionObserverEventWithID(w, flusher, msg.SessionSeq, conversationEvent{
				Type:      "history_item",
				NodeID:    nodeID,
				AgentID:   agentID,
				SessionID: sessionID,
				TurnID:    msg.TurnID,
				MessageID: msg.MessageID,
				Seq:       msg.SessionSeq,
				Item:      &item,
			}); err != nil {
				return headSeq, err
			}
			cursor = msg.SessionSeq
		}
		if !page.HasNewer {
			return headSeq, nil
		}
	}
}

func writeSessionObserverEvent(
	w http.ResponseWriter,
	flusher http.Flusher,
	event conversationEvent,
) error {
	return writeSessionObserverEventWithID(w, flusher, 0, event)
}

// writeSessionObserverEventWithID writes an SSE frame, optionally prefixed with
// an `id:` line so the browser EventSource records it as Last-Event-ID for
// resume.
func writeSessionObserverEventWithID(
	w http.ResponseWriter,
	flusher http.Flusher,
	id int64,
	event conversationEvent,
) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if id > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
			return err
		}
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
