package manager

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
)

type AgentWSConn struct {
	id        string
	agentID   string
	sessionID string
	ws        *websocket.Conn
	send      chan []byte
}

type AgentWSHub struct {
	mu     sync.RWMutex
	agents map[string]*AgentWSConn
}

func NewAgentWSHub() *AgentWSHub {
	return &AgentWSHub{agents: make(map[string]*AgentWSConn)}
}

func (h *AgentWSHub) add(agentID string, conn *AgentWSConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agents[agentID] = conn
}

func (h *AgentWSHub) remove(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.agents, agentID)
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	owner, agent, initial, err := s.authenticateAgentWS(r)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("agent websocket upgrade: %v", err)
		return
	}

	connID := owner.UserID + ":" + agent.AgentID

	conn := &AgentWSConn{
		id:        connID,
		agentID:   agent.AgentID,
		sessionID: initial.SessionID,
		ws:        ws,
		send:      make(chan []byte, 256),
	}
	s.agentWS.add(connID, conn)
	log.Printf("agent websocket connected: %s owner=%s", agent.AgentID, owner.UserID)
	defer func() {
		s.agentWS.remove(connID)
		close(conn.send)
		_ = ws.Close()
		log.Printf("agent websocket disconnected: %s owner=%s", agent.AgentID, owner.UserID)
	}()

	if err := writeAgentWSResponse(ws, agentWSResponse{
		Type:    "connected",
		Code:    http.StatusOK,
		Message: "ok",
		Data: map[string]string{
			"agent_id":      agent.AgentID,
			"owner_user_id": owner.UserID,
			"session_id":    initial.SessionID,
		},
	}); err != nil {
		return
	}

	for {
		messageType, payload, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		s.handleAgentWSMessage(r, ws, agent, payload)
	}
}

type agentWSInitialRequest struct {
	AgentID   string
	SessionID string
}

func (s *Server) authenticateAgentWS(r *http.Request) (User, Agent, agentWSInitialRequest, error) {
	paxKey := websocketPaxKey(r)
	if paxKey == "" {
		return User{}, Agent{}, agentWSInitialRequest{}, apperr.Error{
			Status:  http.StatusUnauthorized,
			Message: "missing pax key",
		}
	}
	agent, err := s.store.AuthenticateAgent(r.Context(), s.secrets.Hash(paxKey))
	if err != nil {
		return User{}, Agent{}, agentWSInitialRequest{}, err
	}
	initial := agentWSInitialRequest{
		AgentID:   websocketAgentID(r),
		SessionID: websocketSessionID(r),
	}
	if initial.AgentID != "" && initial.AgentID != agent.AgentID {
		return User{}, Agent{}, agentWSInitialRequest{}, apperr.Error{
			Status:  http.StatusForbidden,
			Message: "agent_id does not match pax key",
		}
	}
	if initial.AgentID == "" {
		initial.AgentID = agent.AgentID
	}
	owner, err := s.store.GetUser(r.Context(), agent.OwnerUserID)
	if err != nil {
		return User{}, Agent{}, agentWSInitialRequest{}, err
	}
	return owner, agent, initial, nil
}

func websocketPaxKey(r *http.Request) string {
	if token := r.Header.Get("X-Pax-Key"); token != "" {
		return token
	}
	if token := r.Header.Get("X-PAX-Key"); token != "" {
		return token
	}
	if token := auth.BearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	if token := r.URL.Query().Get("pax_key"); token != "" {
		return token
	}
	if token := r.URL.Query().Get("paxKey"); token != "" {
		return token
	}
	return r.URL.Query().Get("key")
}

func websocketAgentID(r *http.Request) string {
	if agentID := r.URL.Query().Get("agent_id"); agentID != "" {
		return agentID
	}
	if agentID := r.URL.Query().Get("agentId"); agentID != "" {
		return agentID
	}
	return r.URL.Query().Get("agentKey")
}

func websocketSessionID(r *http.Request) string {
	if sessionID := r.Header.Get("X-Pax-Session-ID"); sessionID != "" {
		return sessionID
	}
	if sessionID := r.URL.Query().Get("session_id"); sessionID != "" {
		return sessionID
	}
	return r.URL.Query().Get("sessionId")
}

type agentWSRequest struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	MessageID string          `json:"message_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type agentWSResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Data      any    `json:"data"`
	Code      int    `json:"code"`
	Message   string `json:"message"`
}

func (s *Server) handleAgentWSMessage(
	r *http.Request,
	ws *websocket.Conn,
	agent Agent,
	payload []byte,
) {
	var req agentWSRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		_ = writeAgentWSError(ws, "", "", http.StatusBadRequest, "invalid JSON message")
		return
	}
	_ = writeAgentWSResponse(ws, s.handleAgentWSRequest(r.Context(), agent, req))
}

func (s *Server) handleAgentWSRequest(
	ctx context.Context,
	agent Agent,
	req agentWSRequest,
) agentWSResponse {
	switch req.Type {
	case "status", "report_status":
		var report AgentStatusReport
		if err := decodeAgentWSData(req.Data, &report); err != nil {
			return agentWSErrorResponse(req.Type, req.RequestID, http.StatusBadRequest, err.Error())
		}
		status, data, err := s.paxd.ReportStatus(ctx, agent, report)
		return agentWSResult("status_result", req.RequestID, status, data, err)
	case "pull_mailbox":
		var pull OffsetRequest
		var input struct {
			Offset int64 `json:"offset"`
			Limit  int   `json:"limit"`
		}
		if err := decodeAgentWSData(req.Data, &input); err != nil {
			return agentWSErrorResponse(req.Type, req.RequestID, http.StatusBadRequest, err.Error())
		}
		pull.Offset = input.Offset
		status, data, err := s.paxd.PullMailbox(ctx, agent, pull.Offset, input.Limit)
		return agentWSResult("pull_mailbox_result", req.RequestID, status, data, err)
	case "update_offset":
		var input OffsetRequest
		if err := decodeAgentWSData(req.Data, &input); err != nil {
			return agentWSErrorResponse(req.Type, req.RequestID, http.StatusBadRequest, err.Error())
		}
		status, data, err := s.paxd.UpdateOffset(ctx, agent, input.Offset)
		return agentWSResult("update_offset_result", req.RequestID, status, data, err)
	case "message_result", "report_message_result":
		var result MessageResultRequest
		if err := decodeAgentWSData(req.Data, &result); err != nil {
			return agentWSErrorResponse(req.Type, req.RequestID, http.StatusBadRequest, err.Error())
		}
		if result.MessageID == "" {
			result.MessageID = req.MessageID
		}
		status, data, err := s.paxd.ReportMessageResult(ctx, agent, result)
		return agentWSResult("message_result_result", req.RequestID, status, data, err)
	default:
		return agentWSErrorResponse(
			req.Type,
			req.RequestID,
			http.StatusBadRequest,
			"unknown message type",
		)
	}
}

func decodeAgentWSData(data json.RawMessage, v any) error {
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return errors.New("invalid message data")
	}
	return nil
}

func agentWSResult(
	responseType string,
	requestID string,
	status int,
	data any,
	err error,
) agentWSResponse {
	if err != nil {
		code, message := endpointErrorStatus(err)
		return agentWSErrorResponse(responseType, requestID, code, message)
	}
	return agentWSResponse{
		Type:      responseType,
		RequestID: requestID,
		Data:      data,
		Code:      status,
		Message:   "ok",
	}
}

func writeAgentWSError(
	ws *websocket.Conn,
	responseType string,
	requestID string,
	status int,
	message string,
) error {
	return writeAgentWSResponse(ws, agentWSErrorResponse(responseType, requestID, status, message))
}

func agentWSErrorResponse(
	responseType string,
	requestID string,
	status int,
	message string,
) agentWSResponse {
	if responseType == "" {
		responseType = "error"
	}
	return agentWSResponse{
		Type:      responseType,
		RequestID: requestID,
		Data:      nil,
		Code:      status,
		Message:   message,
	}
}

func writeAgentWSResponse(ws *websocket.Conn, resp agentWSResponse) error {
	return ws.WriteJSON(resp)
}
