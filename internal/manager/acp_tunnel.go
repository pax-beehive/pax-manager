package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
)

type ACPTunnelHub struct {
	mu     sync.RWMutex
	agents map[string]*ACPTunnelAgent
}

type ACPTunnelAgent struct {
	agentID string
	ws      *websocket.Conn
	mu      sync.Mutex
	paired  bool
}

func NewACPTunnelHub() *ACPTunnelHub {
	return &ACPTunnelHub{agents: make(map[string]*ACPTunnelAgent)}
}

func (h *ACPTunnelHub) add(agentID string, conn *ACPTunnelAgent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agents[agentID] = conn
}

func (h *ACPTunnelHub) remove(agentID string, conn *ACPTunnelAgent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[agentID] == conn {
		delete(h.agents, agentID)
	}
}

func (h *ACPTunnelHub) claim(agentID string) (*ACPTunnelAgent, error) {
	h.mu.RLock()
	conn := h.agents[agentID]
	h.mu.RUnlock()
	if conn == nil {
		return nil, apperr.Error{Status: http.StatusNotFound, Message: "agent tunnel not connected"}
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.paired {
		return nil, apperr.Error{Status: http.StatusConflict, Message: "agent tunnel already in use"}
	}
	conn.paired = true
	return conn, nil
}

func (h *ACPTunnelHub) release(conn *ACPTunnelAgent) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.paired = false
}

func (s *Server) handleAgentACPTunnel(w http.ResponseWriter, r *http.Request) {
	log.Printf(
		"agent acp tunnel request: path=%s query_agent_id=%q remote=%s",
		r.URL.Path,
		websocketAgentID(r),
		r.RemoteAddr,
	)
	initial, authMode, err := s.authenticateAgentACPTunnel(r)
	if err != nil {
		status, message := endpointErrorStatus(err)
		log.Printf(
			"agent acp tunnel rejected: query_agent_id=%q status=%d reason=%s err=%v",
			websocketAgentID(r),
			status,
			message,
			err,
		)
		writeHTTPEndpointError(w, err)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf(
			"agent acp tunnel upgrade failed: agent_id=%s auth_mode=%s err=%v",
			initial.AgentID,
			authMode,
			err,
		)
		return
	}

	conn := &ACPTunnelAgent{agentID: initial.AgentID, ws: ws}
	s.acpTunnels.add(initial.AgentID, conn)
	log.Printf("agent acp tunnel connected: agent_id=%s auth_mode=%s", initial.AgentID, authMode)
	defer func() {
		s.acpTunnels.remove(initial.AgentID, conn)
		_ = ws.Close()
		log.Printf("agent acp tunnel disconnected: agent_id=%s auth_mode=%s", initial.AgentID, authMode)
	}()

	<-r.Context().Done()
}

func (s *Server) authenticateAgentACPTunnel(
	r *http.Request,
) (agentWSInitialRequest, string, error) {
	paxKey := websocketPaxKey(r)
	if paxKey == "" {
		return agentWSInitialRequest{}, "", apperr.Error{
			Status:  http.StatusUnauthorized,
			Message: "missing pax key",
		}
	}

	requestAgentID := websocketAgentID(r)
	keyHash := s.secrets.Hash(paxKey)
	agent, agentErr := s.store.AuthenticateAgent(r.Context(), keyHash)
	if agentErr == nil {
		if requestAgentID != "" && requestAgentID != agent.AgentID {
			return agentWSInitialRequest{}, "", apperr.Error{
				Status:  http.StatusForbidden,
				Message: "agent_id does not match pax key",
			}
		}
		return agentWSInitialRequest{
			AgentID:   agent.AgentID,
			SessionID: websocketSessionID(r),
		}, "agent_key", nil
	}

	node, nodeErr := s.store.AuthenticateNode(r.Context(), keyHash)
	if nodeErr != nil {
		log.Printf(
			"agent acp tunnel auth failed: query_agent_id=%q key_prefix=%q agent_err=%v node_err=%v",
			requestAgentID,
			s.secrets.Prefix(paxKey),
			agentErr,
			nodeErr,
		)
		return agentWSInitialRequest{}, "", nodeErr
	}
	if requestAgentID == "" {
		return agentWSInitialRequest{}, "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "agent_id is required for node-authenticated tunnel",
		}
	}
	agent, err := s.store.GetNodeAgent(r.Context(), node.NodeID, requestAgentID)
	if err != nil {
		log.Printf(
			"agent acp tunnel node auth accepted but agent not found: node_id=%s query_agent_id=%q err=%v",
			node.NodeID,
			requestAgentID,
			err,
		)
		return agentWSInitialRequest{}, "", err
	}

	return agentWSInitialRequest{
		AgentID:   agent.AgentID,
		SessionID: websocketSessionID(r),
	}, "node_key", nil
}

func (s *Server) handleUserACPTunnel(w http.ResponseWriter, r *http.Request) {
	agentID := userTunnelAgentID(r)
	log.Printf("user acp tunnel request: path=%s agent_id=%q remote=%s", r.URL.Path, agentID, r.RemoteAddr)
	if agentID == "" {
		log.Printf("user acp tunnel rejected: reason=missing_agent_id")
		writeHTTPError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if err := s.authorizeUserTunnel(r, agentID); err != nil {
		status, message := endpointErrorStatus(err)
		log.Printf("user acp tunnel rejected: agent_id=%s status=%d reason=%s err=%v", agentID, status, message, err)
		writeHTTPEndpointError(w, err)
		return
	}

	agentConn, err := s.acpTunnels.claim(agentID)
	if err != nil {
		status, message := endpointErrorStatus(err)
		log.Printf("user acp tunnel claim failed: agent_id=%s status=%d reason=%s err=%v", agentID, status, message, err)
		writeHTTPEndpointError(w, err)
		return
	}
	defer s.acpTunnels.release(agentConn)

	userWS, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("user acp tunnel upgrade failed: agent_id=%s err=%v", agentID, err)
		return
	}
	defer userWS.Close()

	log.Printf("user acp tunnel connected: %s", agentID)
	defer log.Printf("user acp tunnel disconnected: %s", agentID)

	errCh := make(chan error, 2)
	go func() { errCh <- relayWebSocketFrames(userWS, agentConn.ws) }()
	go func() { errCh <- relayWebSocketFrames(agentConn.ws, userWS) }()

	err = <-errCh
	_ = userWS.Close()
	_ = agentConn.ws.Close()
	if err != nil && !isWebSocketCloseError(err) {
		log.Printf("acp tunnel relay ended: %v", err)
	}
}

func (s *Server) authorizeUserTunnel(r *http.Request, agentID string) error {
	principal, err := s.auth.Principal(r.Context(), httpRequestMetadata(r))
	if err != nil {
		return err
	}
	_, err = s.store.GetAgent(r.Context(), principal, agentID)
	return err
}

func httpRequestMetadata(r *http.Request) auth.RequestMetadata {
	headers := map[string]string{}
	for k, values := range r.Header {
		if len(values) > 0 {
			headers[k] = values[0]
		}
	}
	return auth.NewRequestMetadata(headers)
}

func userTunnelAgentID(r *http.Request) string {
	if agentID := r.PathValue("agentID"); agentID != "" {
		return agentID
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/user/"); ok {
		_, rest, ok := strings.Cut(rest, "/agents/")
		if ok {
			agentID, suffix, ok := strings.Cut(rest, "/")
			if ok && suffix == "tunnel" {
				return agentID
			}
		}
	}
	if agentID := r.URL.Query().Get("agent_id"); agentID != "" {
		return agentID
	}
	return r.URL.Query().Get("agentId")
}

func relayWebSocketFrames(src *websocket.Conn, dst *websocket.Conn) error {
	for {
		messageType, payload, err := src.ReadMessage()
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if err := dst.WriteMessage(messageType, payload); err != nil {
			return err
		}
	}
}

func isWebSocketCloseError(err error) bool {
	if err == nil {
		return true
	}
	var closeErr *websocket.CloseError
	return errors.As(err, &closeErr) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, http.ErrServerClosed) ||
		fmt.Sprint(err) == "websocket: close sent"
}
