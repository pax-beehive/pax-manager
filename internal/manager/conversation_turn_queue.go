package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	turnQueueEffectQueued   = "queued"
	turnQueueEffectReplaced = "replaced"

	turnQueueDeleteEffectDeleted = "deleted"
	turnQueueDeleteEffectNoop    = "noop"

	turnStopEffectCancelling        = "cancelling"
	turnStopEffectAlreadyCancelling = "already_cancelling"
	turnStopEffectNoop              = "noop"

	turnSteerEffectSteering = "steering"
)

type conversationQueuedTurn = domain.QueuedTurn

type conversationTurnQueue struct {
	store domain.TurnQueueStore
}

func newConversationTurnQueue(store Store) *conversationTurnQueue {
	queueStore, _ := store.(domain.TurnQueueStore)
	return &conversationTurnQueue{store: queueStore}
}

func (q *conversationTurnQueue) get(
	ctx context.Context,
	agentID, sessionID string,
) (conversationQueuedTurn, bool, error) {
	turn, err := q.store.GetQueuedTurn(ctx, agentID, sessionID)
	if errors.Is(err, domain.ErrNotFound) {
		return turn, false, nil
	}
	return turn, err == nil, err
}

func (q *conversationTurnQueue) upsert(
	ctx context.Context,
	turn conversationQueuedTurn,
) (conversationQueuedTurn, string, error) {
	var err error
	turn.TurnID, err = newConversationTurnID()
	if err != nil {
		return turn, "", err
	}
	stored, replaced, err := q.store.PutQueuedTurn(ctx, turn, false)
	effect := turnQueueEffectQueued
	if replaced {
		effect = turnQueueEffectReplaced
	}
	return stored, effect, err
}

func newConversationTurnID() (string, error) { return auth.Secrets{}.New("turn") }

func (q *conversationTurnQueue) patch(
	ctx context.Context,
	agentID, sessionID, commandID, input string,
) (conversationQueuedTurn, error) {
	turn, _, err := q.store.PutQueuedTurn(
		ctx,
		conversationQueuedTurn{
			AgentID:   agentID,
			SessionID: sessionID,
			CommandID: commandID,
			Input:     input,
		},
		true,
	)
	return turn, err
}

func (q *conversationTurnQueue) delete(
	ctx context.Context,
	agentID, sessionID string,
) (conversationQueuedTurn, string, error) {
	turn, err := q.store.DeleteQueuedTurn(ctx, agentID, sessionID)
	if errors.Is(err, domain.ErrNotFound) {
		return turn, turnQueueDeleteEffectNoop, nil
	}
	return turn, turnQueueDeleteEffectDeleted, err
}

type conversationTurnInput struct {
	Input  string `json:"input"`
	Reason string `json:"reason"`
}

type conversationTurnStopResponse struct {
	ActivePromptRequestID string `json:"active_prompt_request_id,omitempty"`
	AgentID               string `json:"agent_id"`
	CommandID             string `json:"command_id"`
	Effect                string `json:"effect"`
	NodeID                string `json:"node_id,omitempty"`
	SessionID             string `json:"session_id"`
	Status                string `json:"status"`
}

type conversationTurnQueueResponse struct {
	ActivePromptRequestID string `json:"active_prompt_request_id,omitempty"`
	AgentID               string `json:"agent_id"`
	CommandID             string `json:"command_id"`
	Effect                string `json:"effect"`
	NodeID                string `json:"node_id,omitempty"`
	QueuedTurnID          string `json:"queued_turn_id"`
	SessionID             string `json:"session_id"`
	Status                string `json:"status"`
}

type conversationTurnQueueDeleteResponse struct {
	AgentID      string `json:"agent_id"`
	CommandID    string `json:"command_id"`
	Effect       string `json:"effect"`
	NodeID       string `json:"node_id,omitempty"`
	QueuedTurnID string `json:"queued_turn_id,omitempty"`
	SessionID    string `json:"session_id"`
	Status       string `json:"status"`
}

type conversationTurnSteerResponse struct {
	ActivePromptRequestID string `json:"active_prompt_request_id,omitempty"`
	AgentID               string `json:"agent_id"`
	CommandID             string `json:"command_id"`
	Effect                string `json:"effect"`
	NodeID                string `json:"node_id,omitempty"`
	QueueEffect           string `json:"queue_effect"`
	QueuedTurnID          string `json:"queued_turn_id"`
	SessionID             string `json:"session_id"`
	Status                string `json:"status"`
	StopEffect            string `json:"stop_effect"`
}

func (s *Service) handleConversationTurnQueue(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	control, err := s.resolveConversationTurnControlSession(r)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	if s.conversationTurns == nil || s.conversationTurns.store == nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "turn queue unavailable")
		return
	}
	if control.session.Transport == domain.SessionTransportE2EE {
		writeHTTPError(w, http.StatusConflict, "turn queue requires Manager transport")
		return
	}

	commandID := conversationCommandID(r)
	switch r.Method {
	case http.MethodGet:
		turn, ok, err := s.conversationTurns.get(
			r.Context(),
			control.agent.AgentID,
			control.session.SessionID,
		)
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
		if !ok {
			writeHTTPData(w, http.StatusOK, nil)
			return
		}
		turn.NodeID = control.agent.NodeID
		writeHTTPData(w, http.StatusOK, conversationQueuedTurnData(turn))
	case http.MethodPost:
		if control.session.NativeID == "" || control.session.ArchivedAt != nil {
			writeHTTPError(
				w,
				http.StatusConflict,
				"queue requires an initialized, unarchived session",
			)
			return
		}

		input, ok := readConversationTurnInput(w, r)
		if !ok {
			return
		}
		turn, effect, err := s.conversationTurns.upsert(r.Context(), conversationQueuedTurn{
			CommandID: commandID,
			OwnerID:   control.principal.User.UserID,
			NodeID:    control.agent.NodeID,
			AgentID:   control.agent.AgentID,
			SessionID: control.session.SessionID,
			Input:     input.Input,
		})
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
		writeHTTPData(w, http.StatusOK, conversationTurnQueueResponse{
			ActivePromptRequestID: conversationActivePromptRequestID(control.session),
			AgentID:               control.agent.AgentID,
			CommandID:             commandID,
			Effect:                effect,
			NodeID:                control.agent.NodeID,
			QueuedTurnID:          turn.TurnID,
			SessionID:             control.session.SessionID,
			Status:                conversationSessionStatus(control.session),
		})
	case http.MethodPatch:
		input, ok := readConversationTurnInput(w, r)
		if !ok {
			return
		}
		turn, err := s.conversationTurns.patch(
			r.Context(),
			control.agent.AgentID,
			control.session.SessionID,
			commandID,
			input.Input,
		)
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
		turn.NodeID = control.agent.NodeID
		writeHTTPData(w, http.StatusOK, conversationQueuedTurnData(turn))
	case http.MethodDelete:
		turn, effect, err := s.conversationTurns.delete(
			r.Context(),
			control.agent.AgentID,
			control.session.SessionID,
		)
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
		resp := conversationTurnQueueDeleteResponse{
			AgentID:   control.agent.AgentID,
			CommandID: commandID,
			Effect:    effect,
			NodeID:    control.agent.NodeID,
			SessionID: control.session.SessionID,
			Status:    conversationSessionStatus(control.session),
		}
		if turn.TurnID != "" {
			resp.QueuedTurnID = turn.TurnID
		}
		writeHTTPData(w, http.StatusOK, resp)
	default:
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Service) handleConversationTurnStop(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	control, err := s.resolveConversationTurnControlSession(r)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	commandID := conversationCommandID(r)
	effect, err := s.stopConversationTurn(r.Context(), control)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	writeHTTPData(w, http.StatusOK, conversationTurnStopResponse{
		ActivePromptRequestID: conversationActivePromptRequestID(control.session),
		AgentID:               control.agent.AgentID,
		CommandID:             commandID,
		Effect:                effect,
		NodeID:                control.agent.NodeID,
		SessionID:             control.session.SessionID,
		Status:                conversationSessionStatus(control.session),
	})
}

func (s *Service) handleConversationTurnSteer(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	control, err := s.resolveConversationTurnControlSession(r)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	if s.conversationTurns == nil || s.conversationTurns.store == nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "turn queue unavailable")
		return
	}
	if control.session.Transport == domain.SessionTransportE2EE {
		writeHTTPError(w, http.StatusConflict, "turn queue requires Manager transport")
		return
	}

	if !conversationSessionHasActiveTurn(control.session) {
		writeHTTPEndpointError(
			w,
			apperr.Error{Status: http.StatusConflict, Message: "session has no active turn"},
		)
		return
	}
	input, ok := readConversationTurnInput(w, r)
	if !ok {
		return
	}
	commandID := conversationCommandID(r)
	turn, queueEffect, err := s.conversationTurns.upsert(r.Context(), conversationQueuedTurn{
		CommandID: commandID,
		OwnerID:   control.principal.User.UserID,
		NodeID:    control.agent.NodeID,
		AgentID:   control.agent.AgentID,
		SessionID: control.session.SessionID,
		Input:     input.Input,
	})
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	stopEffect, err := s.stopConversationTurn(r.Context(), control)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	writeHTTPData(w, http.StatusOK, conversationTurnSteerResponse{
		ActivePromptRequestID: conversationActivePromptRequestID(control.session),
		AgentID:               control.agent.AgentID,
		CommandID:             commandID,
		Effect:                turnSteerEffectSteering,
		NodeID:                control.agent.NodeID,
		QueueEffect:           queueEffect,
		QueuedTurnID:          turn.TurnID,
		SessionID:             control.session.SessionID,
		Status:                conversationSessionStatus(control.session),
		StopEffect:            stopEffect,
	})
}

func (s *Service) stopConversationTurn(
	ctx context.Context,
	control conversationTurnControlSession,
) (string, error) {
	if !conversationSessionHasActiveTurn(control.session) {
		return turnStopEffectNoop, nil
	}
	if control.session.RuntimeState != nil &&
		control.session.RuntimeState.Lifecycle == domain.RuntimeLifecycleCancelling {
		return turnStopEffectAlreadyCancelling, nil
	}
	agentConn, err := s.acpTunnels.findAny(
		control.agent.AgentID,
		control.session.SessionID,
		control.session.NativeID,
		"",
	)
	if err != nil {
		return "", err
	}
	runner := conversationRunner{
		service:          s,
		agentConn:        agentConn,
		managerSessionID: control.session.SessionID,
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params": map[string]any{
			"sessionId": control.session.SessionID,
		},
	})
	if err != nil {
		return "", err
	}
	if err := runner.sendCancelRaw(ctx, payload); err != nil {
		return "", err
	}
	return turnStopEffectCancelling, nil
}

type conversationTurnControlSession struct {
	principal UserPrincipal
	agent     Agent
	session   domain.AgentSession
}

func (s *Service) resolveConversationTurnControlSession(
	r *http.Request,
) (conversationTurnControlSession, error) {
	routeUserID, agentID, sessionID := conversationTurnControlRouteIDs(r.URL.Path)
	if agentID == "" || sessionID == "" {
		return conversationTurnControlSession{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id and session_id are required",
		}
	}
	principal, err := s.auth.Principal(r.Context(), httpRequestMetadata(r))
	if err != nil {
		return conversationTurnControlSession{}, err
	}
	if routeUserID != "" && routeUserID != "self" && routeUserID != principal.User.UserID {
		return conversationTurnControlSession{}, ErrNotFound
	}
	agent, err := s.store.GetAgent(r.Context(), principal, agentID)
	if err != nil {
		return conversationTurnControlSession{}, err
	}
	session, err := s.store.GetSession(r.Context(), principal, sessionID)
	if err != nil {
		return conversationTurnControlSession{}, err
	}
	if session.AgentID != agent.AgentID || session.NodeID != agent.NodeID {
		return conversationTurnControlSession{}, ErrNotFound
	}

	// Address a live request before its snapshot arrives, without persisting local state.
	local := s.acpRuntime.CurrentCorrelation(agentID, sessionID)
	if local.ActivePromptRequestID != "" {
		if conn, err := s.acpTunnels.findAny(agentID, sessionID, session.NativeID, ""); err == nil {
			waiterSession, _, waiting := conn.responseWaiterContext(local.ActivePromptRequestID)
			if waiting && waiterSession == sessionID {
				session.RuntimeState = &local
			}
		}
	}
	return conversationTurnControlSession{principal: principal, agent: agent, session: session}, nil
}

func conversationTurnControlRouteIDs(path string) (userID, agentID, sessionID string) {
	rest, ok := strings.CutPrefix(path, "/api/v1/user/")
	if !ok {
		return "", "", ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 5 || parts[1] != "agents" || parts[3] != "sessions" {
		return "", "", ""
	}
	return parts[0], parts[2], parts[4]
}

func readConversationTurnInput(
	w http.ResponseWriter,
	r *http.Request,
) (conversationTurnInput, bool) {
	var input conversationTurnInput
	body := r.Body
	if err := json.NewDecoder(body).Decode(&input); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid JSON body")
		return conversationTurnInput{}, false
	}
	input.Input = strings.TrimSpace(input.Input)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Input == "" || len(input.Input) > domain.MaxQueuedTurnBytes {
		writeHTTPError(w, http.StatusBadRequest, "input must contain between 1 and 65536 bytes")
		return conversationTurnInput{}, false
	}
	return input, true
}

func conversationQueuedTurnData(turn conversationQueuedTurn) map[string]any {
	return map[string]any{
		"agent_id":       turn.AgentID,
		"command_id":     turn.CommandID,
		"created_at":     turn.CreatedAt,
		"input":          turn.Input,
		"node_id":        turn.NodeID,
		"queued_turn_id": turn.TurnID,
		"session_id":     turn.SessionID,
		"updated_at":     turn.UpdatedAt,
		"state":          turn.State,
	}
}

func conversationCommandID(r *http.Request) string {
	if commandID := strings.TrimSpace(r.Header.Get("Idempotency-Key")); commandID != "" {
		return commandID
	}
	commandID, err := auth.Secrets{}.New("cmd")
	if err == nil {
		return commandID
	}
	return "cmd"
}

func conversationSessionHasActiveTurn(session domain.AgentSession) bool {
	state := session.RuntimeState
	if state == nil || state.ActivePromptRequestID == "" {
		return false
	}
	switch state.Lifecycle {
	case domain.RuntimeLifecycleRunning,
		domain.RuntimeLifecycleWaitingApproval,
		domain.RuntimeLifecycleBlocked,
		domain.RuntimeLifecycleCancelling:
		return true
	default:
		return false
	}
}

func conversationActivePromptRequestID(session domain.AgentSession) string {
	if session.RuntimeState == nil {
		return ""
	}
	return session.RuntimeState.ActivePromptRequestID
}

func conversationActiveTurnID(session domain.AgentSession) string {
	if session.RuntimeState == nil {
		return ""
	}
	return session.RuntimeState.ActiveTurnID
}

func conversationSessionStatus(session domain.AgentSession) string {
	if session.RuntimeState != nil {
		return firstNonEmpty(session.RuntimeState.Lifecycle, session.Status)
	}
	return session.Status
}

func writeHTTPData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiResponse{Data: data, Code: status, Message: "ok"})
}

func (r *conversationRunner) sendCancelRaw(ctx context.Context, payload []byte) error {
	frame := newACPFrameContext(r.agentConn, acpUserToAgent, websocket.TextMessage, payload)
	frame.managerSessionID = r.managerSessionID
	return r.service.userACPFramePipeline().Handle(
		ctx,
		frame,
		func(_ context.Context, frame *acpFrameContext) error {
			return r.agentConn.writeSessionFrame(
				ctx,
				frame.managerSessionID,
				frame.nativeSessionID,
				frame.messageType,
				frame.payload,
			)
		},
	)
}
