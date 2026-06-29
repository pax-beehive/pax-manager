package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	conversationTunnelClaimTimeout = 2 * time.Second
	conversationTunnelClaimTick    = 50 * time.Millisecond
)

var conversationRequestIdleTimeout = 30 * time.Second

type conversationRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Input     string `json:"input"`
}

type conversationEvent struct {
	Type      string          `json:"type"`
	NodeID    string          `json:"node_id,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Frame     json.RawMessage `json:"frame,omitempty"`
	Message   string          `json:"message,omitempty"`
}

type conversationSession struct {
	managerID string
	nativeID  string
}

type conversationRunner struct {
	service   *Service
	agentConn *ACPTunnelAgent
}

type conversationResponse struct {
	acpJSONRPCMessage
	raw []byte
}

func (s *Service) handleConversation(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, nodeID, agentID := conversationRouteIDs(r)
	if nodeID == "" || agentID == "" {
		writeHTTPError(w, http.StatusBadRequest, "node_id and agent_id are required")
		return
	}
	principal, agent, err := s.authorizeConversation(r, nodeID, agentID)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	var req conversationRequest
	body := r.Body
	if s.maxBodyBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Input = strings.TrimSpace(req.Input)
	if req.Input == "" {
		writeHTTPError(w, http.StatusBadRequest, "input is required")
		return
	}

	session, err := s.resolveConversationSession(
		r.Context(),
		principal,
		nodeID,
		agentID,
		req.SessionID,
	)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	claimSessionIDs := []string{""}
	if session.managerID != "" {
		claimSessionIDs = []string{session.managerID, session.nativeID, ""}
	}
	agentConn, err := s.acpTunnels.claimAnyWait(
		r.Context(),
		conversationTunnelClaimTimeout,
		conversationTunnelClaimTick,
		agentID,
		claimSessionIDs...,
	)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	defer s.acpTunnels.release(agentConn)

	runner := conversationRunner{
		service:   s,
		agentConn: agentConn,
	}
	if session.managerID == "" {
		session, err = s.createConversationSession(r.Context(), &runner)
		if err != nil {
			writeHTTPEndpointError(w, err)
			return
		}
	}

	restoreSessionContext := agentConn.withSessionContext(session.managerID)
	defer restoreSessionContext()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	if err := s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "session",
		NodeID:    agent.NodeID,
		AgentID:   agent.AgentID,
		SessionID: session.managerID,
	}); err != nil {
		return
	}
	if err := s.promptConversation(r.Context(), w, flusher, &runner, session, req.Input); err != nil {
		_ = s.writeConversationEvent(w, flusher, conversationEvent{
			Type:      "error",
			NodeID:    agent.NodeID,
			AgentID:   agent.AgentID,
			SessionID: session.managerID,
			Message:   err.Error(),
		})
	}
}

func (s *Service) authorizeConversation(
	r *http.Request,
	nodeID string,
	agentID string,
) (UserPrincipal, Agent, error) {
	principal, err := s.auth.Principal(r.Context(), httpRequestMetadata(r))
	if err != nil {
		return UserPrincipal{}, Agent{}, err
	}
	agent, err := s.store.GetAgent(r.Context(), principal, agentID)
	if err != nil {
		return UserPrincipal{}, Agent{}, err
	}
	if agent.NodeID != nodeID {
		return UserPrincipal{}, Agent{}, ErrNotFound
	}
	return principal, agent, nil
}

func (s *Service) resolveConversationSession(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
	agentID string,
	sessionID string,
) (conversationSession, error) {
	if sessionID == "" {
		return conversationSession{}, nil
	}
	session, err := s.store.GetSession(ctx, principal, sessionID)
	if err != nil {
		return conversationSession{}, err
	}
	if session.AgentID != agentID || session.NodeID != nodeID {
		return conversationSession{}, ErrNotFound
	}
	if session.NativeID == "" {
		return conversationSession{}, apperr.Error{
			Status:  http.StatusConflict,
			Message: "session has no native ACP session id",
		}
	}
	return conversationSession{managerID: session.SessionID, nativeID: session.NativeID}, nil
}

func (s *Service) createConversationSession(
	ctx context.Context,
	runner *conversationRunner,
) (conversationSession, error) {
	managerSessionID, err := newManagerSessionID()
	if err != nil {
		return conversationSession{}, err
	}
	restoreSessionContext := runner.agentConn.withSessionContext(managerSessionID)
	defer restoreSessionContext()
	if _, err := runner.request(
		ctx,
		"initialize",
		map[string]any{
			"protocolVersion":    1,
			"clientCapabilities": map[string]any{},
			"clientInfo": map[string]any{
				"name":    "pax-manager-conversation",
				"version": "0.1.0",
			},
		},
		nil,
	); err != nil {
		return conversationSession{}, err
	}
	resp, err := runner.request(
		ctx,
		"session/new",
		map[string]any{
			"cwd":        "/tmp",
			"mcpServers": []any{},
		},
		nil,
	)
	if err != nil {
		return conversationSession{}, err
	}
	createdSessionID := findStringFromRaw(resp.Result, "sessionId", "session_id")
	if createdSessionID == "" {
		return conversationSession{}, apperr.Error{
			Status:  http.StatusBadGateway,
			Message: "ACP session/new did not return a sessionId",
		}
	}
	return conversationSession{managerID: managerSessionID}, nil
}

func (s *Service) promptConversation(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	runner *conversationRunner,
	session conversationSession,
	input string,
) error {
	sub := runner.agentConn.subscribeSSE(session.managerID)
	defer runner.agentConn.unsubscribeSSE(sub)

	done := make(chan struct{})
	activity := make(chan struct{}, 1)
	go func() {
		defer close(done)
		for payload := range sub.ch {
			notifyConversationActivity(activity)
			_ = s.writeConversationEvent(w, flusher, conversationEvent{
				Type:      "acp",
				NodeID:    runner.agentConn.nodeID,
				AgentID:   runner.agentConn.agentID,
				SessionID: session.managerID,
				Frame:     append(json.RawMessage(nil), payload...),
			})
		}
	}()

	_, err := runner.request(
		ctx,
		"session/prompt",
		map[string]any{
			"sessionId": session.managerID,
			"prompt": []map[string]string{{
				"type": "text",
				"text": input,
			}},
		},
		activity,
	)
	runner.agentConn.unsubscribeSSE(sub)
	<-done
	if err != nil {
		return err
	}
	return s.writeConversationEvent(w, flusher, conversationEvent{
		Type:      "done",
		NodeID:    runner.agentConn.nodeID,
		AgentID:   runner.agentConn.agentID,
		SessionID: session.managerID,
	})
}

func (r *conversationRunner) request(
	ctx context.Context,
	method string,
	params map[string]any,
	activity <-chan struct{},
) (conversationResponse, error) {
	requestID := r.agentConn.nextManagerRequestID()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return conversationResponse{}, err
	}
	waiter, cancel := r.agentConn.addResponseWaiter(fmt.Sprint(requestID))
	defer cancel()
	originalPayload := append([]byte(nil), payload...)
	frame := newACPFrameContext(r.agentConn, acpUserToAgent, websocket.TextMessage, payload)
	if err := r.service.userACPFramePipeline().Handle(
		ctx,
		frame,
		func(_ context.Context, frame *acpFrameContext) error {
			if err := r.agentConn.writeToAgent(ctx, frame.messageType, frame.payload); err != nil {
				return err
			}
			return projectACPUserPrompt(ctx, r.agentConn, originalPayload)
		},
	); err != nil {
		return conversationResponse{}, err
	}
	timer := time.NewTimer(conversationRequestIdleTimeout)
	defer timer.Stop()
	for {
		select {
		case payload := <-waiter:
			var msg acpJSONRPCMessage
			_ = json.Unmarshal(payload, &msg)
			if len(msg.Error) > 0 {
				return conversationResponse{}, apperr.Error{
					Status:  http.StatusBadGateway,
					Message: firstNonEmpty(acpErrorMessage(msg.Error), "ACP request failed"),
				}
			}
			return conversationResponse{acpJSONRPCMessage: msg, raw: payload}, nil
		case <-activity:
			resetConversationIdleTimer(timer)
		case <-timer.C:
			return conversationResponse{}, apperr.Error{
				Status:  http.StatusGatewayTimeout,
				Message: "ACP request idle timed out: " + method,
			}
		case <-ctx.Done():
			return conversationResponse{}, ctx.Err()
		}
	}
}

func notifyConversationActivity(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func resetConversationIdleTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(conversationRequestIdleTimeout)
}

func (s *Service) writeConversationEvent(
	w http.ResponseWriter,
	flusher http.Flusher,
	event conversationEvent,
) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func conversationRouteIDs(r *http.Request) (string, string, string) {
	userID := r.PathValue("userID")
	if userID == "" {
		userID = r.PathValue("user_id")
	}
	nodeID := r.PathValue("nodeID")
	if nodeID == "" {
		nodeID = r.PathValue("node_id")
	}
	agentID := r.PathValue("agentID")
	if agentID == "" {
		agentID = r.PathValue("agent_id")
	}
	if userID != "" || nodeID != "" || agentID != "" {
		return userID, nodeID, agentID
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/user/")
	if !ok {
		return "", "", ""
	}
	userID, rest, ok = strings.Cut(rest, "/nodes/")
	if !ok {
		return userID, "", ""
	}
	nodeID, rest, ok = strings.Cut(rest, "/agents/")
	if !ok {
		return userID, nodeID, ""
	}
	agentID, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "conversation" {
		return userID, nodeID, ""
	}
	return userID, nodeID, agentID
}

func acpErrorMessage(raw json.RawMessage) string {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return stringField(value, "message", "")
}
