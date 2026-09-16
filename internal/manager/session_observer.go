package manager

import (
	"context"
	"crypto/sha256"
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

// The observer has one source of truth: the durable transcript of one pinned
// business turn. Live updates replace message versions, never append raw ACP
// deltas to a history aggregate.
func (s *Service) handleSessionObserverEvents(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	if r.Method != http.MethodGet {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	control, err := s.resolveConversationTurnControlSession(r.WithContext(ctx))
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	cursor, err := parseSessionObserverCursor(r)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	turnID := strings.TrimSpace(r.URL.Query().Get("turn_id"))
	if turnID == "" && cursor > 0 {
		writeHTTPError(w, http.StatusBadRequest, "turn_id is required with a resume cursor")
		return
	}
	if turnID == "" && conversationSessionHasActiveTurn(control.session) {
		turnID = conversationActiveTurnID(control.session)
	}
	store, ok := s.store.(domain.TurnTranscriptStore)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "turn transcript store unavailable")
		return
	}
	var items []domain.MessageWithParts
	if turnID != "" {
		items, err = store.ListTurnTranscript(
			ctx,
			control.agent.AgentID,
			control.session.SessionID,
			turnID,
		)
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
		if len(items) == 0 && turnID != conversationActiveTurnID(control.session) {
			writeHTTPError(w, http.StatusNotFound, "turn not found in session")
			return
		}
		if cursor > turnTranscriptHead(items) {
			writeHTTPError(w, http.StatusConflict, "cursor is ahead of the target turn")
			return
		}
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
	base := conversationEvent{NodeID: control.agent.NodeID, AgentID: control.agent.AgentID,
		SessionID: control.session.SessionID, TurnID: turnID}
	if turnID == "" {
		base.Type = "no_running_turn"
		_ = writeSessionObserverEvent(w, flusher, base)
		return
	}
	base.Type = "turn_start"
	if err := writeSessionObserverEvent(w, flusher, base); err != nil {
		return
	}
	s.streamTurnTranscript(ctx, w, flusher, store, base, items)
}

func parseSessionObserverCursor(r *http.Request) (int64, error) {
	if r.URL.Query().Has("after_message_id") {
		return 0, errors.New("after_message_id is unsupported; use turn_id and after_seq")
	}
	value := r.URL.Query().Get("after_seq")
	if !r.URL.Query().Has("after_seq") {
		value = r.Header.Get("Last-Event-ID")
	}
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, errors.New("invalid observer cursor")
	}
	return cursor, nil
}

func turnTranscriptHead(items []domain.MessageWithParts) int64 {
	var head int64
	for _, item := range items {
		if item.SessionSeq > head {
			head = item.SessionSeq
		}
	}
	return head
}

func (s *Service) streamTurnTranscript(
	ctx context.Context, w http.ResponseWriter, flusher http.Flusher,
	store domain.TurnTranscriptStore, base conversationEvent, items []domain.MessageWithParts,
) {
	versions := make(map[string]string)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	idle := time.NewTimer(conversationRequestIdleTimeout)
	defer idle.Stop()
	for {
		changed, complete, err := writeTurnTranscriptUpdate(w, flusher, base, items, versions)
		if err != nil {
			return
		}
		if changed {
			resetConversationIdleTimer(idle)
		}
		if complete {
			base.Type = "turn_done"
			base.Status = "done"
			_ = writeSessionObserverEvent(w, flusher, base)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			base.Type = "resync"
			base.Message = "turn transcript idle timeout; reconnect to the same turn"
			_ = writeSessionObserverEvent(w, flusher, base)
			return
		case <-ticker.C:
			items, err = store.ListTurnTranscript(ctx, base.AgentID, base.SessionID, base.TurnID)
			if err != nil {
				base.Type = "error"
				base.Message = "turn transcript read failed"
				_ = writeSessionObserverEvent(w, flusher, base)
				return
			}
		}
	}
}

func writeTurnTranscriptUpdate(
	w http.ResponseWriter, flusher http.Flusher, base conversationEvent,
	items []domain.MessageWithParts, versions map[string]string,
) (bool, bool, error) {
	changed, complete := false, false
	present := make(map[string]bool, len(items))
	for i := range items {
		item := &items[i]
		if item.TurnID != base.TurnID || item.SessionID != base.SessionID {
			continue
		}
		present[item.MessageID] = true
		if item.MessageType == "turn_done" {
			complete = true
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return false, false, err
		}
		version := fmt.Sprintf("%x", sha256.Sum256(encoded))
		if versions[item.MessageID] == version {
			continue
		}
		event := base
		event.Type, event.MessageID, event.Seq, event.Item = "history_item", item.MessageID, item.SessionSeq, item
		if err := writeSessionObserverEvent(w, flusher, event); err != nil {
			return false, false, err
		}
		versions[item.MessageID] = version
		changed = true
	}
	// Invocation display projection may replace earlier transcript rows.
	for id := range versions {
		if present[id] {
			continue
		}
		event := base
		event.Type, event.MessageID = "history_remove", id
		if err := writeSessionObserverEvent(w, flusher, event); err != nil {
			return false, false, err
		}
		delete(versions, id)
		changed = true
	}
	// A head commits the batch. Individual items are not resume checkpoints:
	// existing rows can change without receiving another SessionSeq.
	base.Type, base.HeadSeq = "head", turnTranscriptHead(items)
	if err := writeSessionObserverEventWithID(w, flusher, base.HeadSeq, base); err != nil {
		return false, false, err
	}
	return changed, complete, nil
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
