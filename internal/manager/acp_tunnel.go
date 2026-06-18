package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	agentID      string
	nodeID       string
	ownerUserID  string
	sessionID    string
	ws           *websocket.Conn
	mu           sync.Mutex
	agentWriteMu sync.Mutex
	userWriteMu  sync.Mutex
	paired       bool
	userWS       *websocket.Conn
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
		return nil, apperr.Error{
			Status:  http.StatusConflict,
			Message: "agent tunnel already in use",
		}
	}
	conn.paired = true
	return conn, nil
}

func (h *ACPTunnelHub) release(conn *ACPTunnelAgent) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.paired = false
	conn.userWS = nil
}

func (a *ACPTunnelAgent) attachUser(userWS *websocket.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.userWS = userWS
}

func (a *ACPTunnelAgent) closeUser() {
	a.mu.Lock()
	userWS := a.userWS
	a.userWS = nil
	a.paired = false
	a.mu.Unlock()
	if userWS != nil {
		_ = userWS.Close()
	}
}

func (a *ACPTunnelAgent) currentUser() *websocket.Conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.userWS
}

func (a *ACPTunnelAgent) writeToAgent(messageType int, payload []byte) error {
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	return a.ws.WriteMessage(messageType, payload)
}

func (a *ACPTunnelAgent) forwardAgentFrames(ctx context.Context, store Store) error {
	for {
		messageType, payload, err := a.ws.ReadMessage()
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		payload, handled, err := a.prepareAgentACPFrame(ctx, store, messageType, payload)
		if err != nil {
			log.Printf(
				"agent acp tunnel approval handling failed: agent_id=%s err=%v",
				a.agentID,
				err,
			)
		}
		if handled {
			continue
		}
		userWS := a.currentUser()
		if userWS == nil {
			log.Printf("agent acp tunnel dropped frame without user: agent_id=%s", a.agentID)
			continue
		}
		a.userWriteMu.Lock()
		err = userWS.WriteMessage(messageType, payload)
		a.userWriteMu.Unlock()
		if err != nil {
			log.Printf(
				"agent acp tunnel failed to write user frame: agent_id=%s err=%v",
				a.agentID,
				err,
			)
			a.closeUser()
		}
	}
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

	conn := &ACPTunnelAgent{
		agentID:     initial.AgentID,
		nodeID:      initial.NodeID,
		ownerUserID: initial.OwnerUserID,
		sessionID:   initial.SessionID,
		ws:          ws,
	}
	s.acpTunnels.add(initial.AgentID, conn)
	log.Printf("agent acp tunnel connected: agent_id=%s auth_mode=%s", initial.AgentID, authMode)
	defer func() {
		s.acpTunnels.remove(initial.AgentID, conn)
		conn.closeUser()
		_ = ws.Close()
		log.Printf(
			"agent acp tunnel disconnected: agent_id=%s auth_mode=%s",
			initial.AgentID,
			authMode,
		)
	}()

	err = conn.forwardAgentFrames(r.Context(), s.store)
	if err != nil && !isWebSocketCloseError(err) {
		log.Printf(
			"agent acp tunnel read ended: agent_id=%s auth_mode=%s err=%v",
			initial.AgentID,
			authMode,
			err,
		)
	}
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
		sessionID := s.virtualACPSessionID(
			r.Context(),
			agent.OwnerUserID,
			agent.AgentID,
			websocketSessionID(r),
		)
		return agentWSInitialRequest{
			AgentID:     agent.AgentID,
			NodeID:      agent.NodeID,
			OwnerUserID: agent.OwnerUserID,
			SessionID:   sessionID,
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

	sessionID := s.virtualACPSessionID(
		r.Context(),
		agent.OwnerUserID,
		agent.AgentID,
		websocketSessionID(r),
	)
	return agentWSInitialRequest{
		AgentID:     agent.AgentID,
		NodeID:      agent.NodeID,
		OwnerUserID: agent.OwnerUserID,
		SessionID:   sessionID,
	}, "node_key", nil
}

func (s *Server) virtualACPSessionID(
	ctx context.Context,
	ownerUserID string,
	agentID string,
	sessionID string,
) string {
	if sessionID == "" {
		return ""
	}
	principal := UserPrincipal{User: User{UserID: ownerUserID}}
	sessions, err := s.store.ListAgentSessions(ctx, principal, agentID)
	if err != nil {
		return sessionID
	}
	for _, session := range sessions {
		if session.NativeID == sessionID || session.SessionID == sessionID {
			return session.SessionID
		}
	}
	return sessionID
}

func (s *Server) handleUserACPTunnel(w http.ResponseWriter, r *http.Request) {
	agentID := userTunnelAgentID(r)
	log.Printf(
		"user acp tunnel request: path=%s agent_id=%q remote=%s",
		r.URL.Path,
		agentID,
		r.RemoteAddr,
	)
	if agentID == "" {
		log.Printf("user acp tunnel rejected: reason=missing_agent_id")
		writeHTTPError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if err := s.authorizeUserTunnel(r, agentID); err != nil {
		status, message := endpointErrorStatus(err)
		log.Printf(
			"user acp tunnel rejected: agent_id=%s status=%d reason=%s err=%v",
			agentID,
			status,
			message,
			err,
		)
		writeHTTPEndpointError(w, err)
		return
	}

	agentConn, err := s.acpTunnels.claim(agentID)
	if err != nil {
		status, message := endpointErrorStatus(err)
		log.Printf(
			"user acp tunnel claim failed: agent_id=%s status=%d reason=%s err=%v",
			agentID,
			status,
			message,
			err,
		)
		writeHTTPEndpointError(w, err)
		return
	}
	defer s.acpTunnels.release(agentConn)

	userWS, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("user acp tunnel upgrade failed: agent_id=%s err=%v", agentID, err)
		return
	}
	agentConn.attachUser(userWS)
	defer func() {
		agentConn.closeUser()
	}()

	log.Printf("user acp tunnel connected: %s", agentID)
	defer log.Printf("user acp tunnel disconnected: %s", agentID)

	err = relayUserFramesToAgent(userWS, agentConn)
	if err != nil && !isWebSocketCloseError(err) {
		log.Printf("user acp tunnel relay ended: agent_id=%s err=%v", agentID, err)
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

type acpJSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (a *ACPTunnelAgent) prepareAgentACPFrame(
	ctx context.Context,
	store Store,
	messageType int,
	payload []byte,
) ([]byte, bool, error) {
	if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
		return payload, false, nil
	}
	var msg acpJSONRPCMessage
	if err := json.Unmarshal(payload, &msg); err != nil ||
		msg.Method != "session/request_permission" {
		return payload, false, nil
	}
	params, err := decodeACPParams(msg.Params)
	if err != nil {
		return payload, false, err
	}
	fingerprint, err := acpPermissionFingerprint(params)
	if err != nil {
		return payload, false, err
	}
	domain := stringField(params, "domain", "agent_action")
	operation := stringField(params, "operation", "session/request_permission")
	_, err = store.FindReusableApprovalGrant(ctx, ApprovalGrantLookup{
		OwnerUserID:       a.ownerUserID,
		RequestNodeID:     a.nodeID,
		RequestAgentID:    a.agentID,
		RequestSessionID:  a.sessionID,
		Domain:            domain,
		Operation:         operation,
		ActionFingerprint: fingerprint,
	})
	if err == nil {
		response, buildErr := acpAllowOnceResponse(msg, params)
		if buildErr != nil {
			return payload, false, buildErr
		}
		if writeErr := a.writeToAgent(websocket.TextMessage, response); writeErr != nil {
			return payload, true, writeErr
		}
		return payload, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return payload, false, err
	}

	changed := appendACPAllowAlwaysOption(params)
	if !changed {
		return payload, false, nil
	}
	msg.Params = mustMarshalRaw(params)
	out, err := json.Marshal(msg)
	if err != nil {
		return payload, false, err
	}
	return out, false, nil
}

func decodeACPParams(raw json.RawMessage) (map[string]any, error) {
	params := map[string]any{}
	if len(raw) == 0 {
		return params, nil
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	return params, nil
}

func acpPermissionFingerprint(params map[string]any) (string, error) {
	if v := stringField(params, "action_fingerprint", ""); v != "" {
		return v, nil
	}
	if v := stringField(params, "actionFingerprint", ""); v != "" {
		return v, nil
	}
	if fingerprint := acpToolCallFingerprint(params); fingerprint != "" {
		return fingerprint, nil
	}
	canonical := map[string]any{}
	for key, value := range params {
		if key == "options" {
			continue
		}
		canonical[key] = value
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "acp:" + hex.EncodeToString(sum[:]), nil
}

func acpToolCallFingerprint(params map[string]any) string {
	toolCall, ok := params["toolCall"].(map[string]any)
	if !ok {
		return ""
	}
	kind := stringField(toolCall, "kind", "tool_call")
	rawInput, _ := toolCall["rawInput"].(map[string]any)
	subject := stringField(rawInput, "command", "")
	if subject == "" {
		subject = stringField(rawInput, "path", "")
	}
	if subject == "" {
		subject = stringField(rawInput, "url", "")
	}
	if subject == "" {
		subject = stringField(toolCall, "title", "")
	}
	if subject == "" {
		return ""
	}
	return "acp:tool_call:" + kind + ":" + subject
}

func appendACPAllowAlwaysOption(params map[string]any) bool {
	options, _ := params["options"].([]any)
	for _, option := range options {
		if acpOptionID(option) == "allow_always_on_all_agents" {
			return false
		}
	}
	options = append(options, acpAllowAlwaysOption(options))
	params["options"] = options
	return true
}

func acpAllowAlwaysOption(existing []any) any {
	switch {
	case len(existing) > 0 && acpOptionLooksLikeString(existing):
		return "allow_always_on_all_agents"
	case len(existing) > 0 && acpOptionUsesKey(existing, "id"):
		return map[string]any{
			"id":    "allow_always_on_all_agents",
			"label": "Allow always on all agents",
		}
	case len(existing) > 0 && acpOptionUsesKey(existing, "option_id"):
		return map[string]any{
			"option_id": "allow_always_on_all_agents",
			"label":     "Allow always on all agents",
		}
	default:
		return map[string]any{
			"optionId": "allow_always_on_all_agents",
			"kind":     "allow_always_on_all_agents",
			"name":     "Allow always on all agents",
			"label":    "Allow always on all agents",
		}
	}
}

func acpAllowOnceResponse(msg acpJSONRPCMessage, params map[string]any) ([]byte, error) {
	if len(msg.ID) == 0 {
		return nil, errors.New("permission request missing JSON-RPC id")
	}
	option, ok := acpFindOption(params, "allow_once")
	if !ok {
		return nil, errors.New("permission request missing allow_once option")
	}
	return json.Marshal(map[string]any{
		"jsonrpc": firstString(msg.JSONRPC, "2.0"),
		"id":      json.RawMessage(msg.ID),
		"result":  option,
	})
}

func acpFindOption(params map[string]any, optionID string) (any, bool) {
	options, _ := params["options"].([]any)
	for _, option := range options {
		if acpOptionID(option) == optionID || acpOptionKind(option) == optionID {
			return option, true
		}
	}
	return nil, false
}

func acpOptionKind(option any) string {
	optionMap, ok := option.(map[string]any)
	if !ok {
		return ""
	}
	kind, _ := optionMap["kind"].(string)
	return kind
}

func acpOptionID(option any) string {
	switch value := option.(type) {
	case string:
		return value
	case map[string]any:
		for _, key := range []string{"option_id", "optionId", "id", "name", "value"} {
			if id, ok := value[key].(string); ok && id != "" {
				return id
			}
		}
	}
	return ""
}

func acpOptionLooksLikeString(options []any) bool {
	_, ok := options[0].(string)
	return ok
}

func acpOptionUsesKey(options []any, key string) bool {
	option, ok := options[0].(map[string]any)
	if !ok {
		return false
	}
	_, ok = option[key]
	return ok
}

func stringField(values map[string]any, key string, fallback string) string {
	value, ok := values[key].(string)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func mustMarshalRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

func relayUserFramesToAgent(userWS *websocket.Conn, agentConn *ACPTunnelAgent) error {
	for {
		messageType, payload, err := userWS.ReadMessage()
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if err := agentConn.writeToAgent(messageType, payload); err != nil {
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
