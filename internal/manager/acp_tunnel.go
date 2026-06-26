package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/paxkit/reliablemq"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

type ACPTunnelHub struct {
	mu     sync.RWMutex
	agents map[acpTunnelKey]*ACPTunnelAgent
}

type acpTunnelKey struct {
	agentID   string
	sessionID string
}

type ACPTunnelAgent struct {
	agentID           string
	connectionID      string
	nodeID            string
	ownerUserID       string
	sessionID         string
	ws                *websocket.Conn
	mu                sync.Mutex
	agentWriteMu      sync.Mutex
	userWriteMu       sync.Mutex
	paired            bool
	userWS            *websocket.Conn
	responseWaiters   map[string]chan []byte
	sseSubscribers    map[*acpSSESubscriber]struct{}
	store             domain.Store
	historyGroups     acpHistoryGroups
	pendingSessionNew acpPendingSessionNews
	managerRequestSeq int64
}

type acpUserTunnelMetadata struct {
	ClientID string
	DeviceID string
	TunnelID string
}

type acpSSESubscriber struct {
	sessionID string
	ch        chan []byte
}

func NewACPTunnelHub() *ACPTunnelHub {
	return &ACPTunnelHub{agents: make(map[acpTunnelKey]*ACPTunnelAgent)}
}

func (h *ACPTunnelHub) add(agentID string, sessionID string, conn *ACPTunnelAgent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agents[acpTunnelKey{agentID: agentID, sessionID: sessionID}] = conn
}

func (h *ACPTunnelHub) remove(agentID string, sessionID string, conn *ACPTunnelAgent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := acpTunnelKey{agentID: agentID, sessionID: sessionID}
	if h.agents[key] == conn {
		delete(h.agents, key)
	}
}

func (h *ACPTunnelHub) claim(agentID string, sessionID string) (*ACPTunnelAgent, error) {
	return h.claimAny(agentID, sessionID, "")
}

func (h *ACPTunnelHub) claimAny(agentID string, sessionIDs ...string) (*ACPTunnelAgent, error) {
	h.mu.RLock()
	var conn *ACPTunnelAgent
	seen := map[string]struct{}{}
	for _, sessionID := range sessionIDs {
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		conn = h.agents[acpTunnelKey{agentID: agentID, sessionID: sessionID}]
		if conn != nil {
			break
		}
	}
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

func (a *ACPTunnelAgent) addResponseWaiter(requestID string) (<-chan []byte, func()) {
	ch := make(chan []byte, 1)
	a.mu.Lock()
	if a.responseWaiters == nil {
		a.responseWaiters = make(map[string]chan []byte)
	}
	a.responseWaiters[requestID] = ch
	a.mu.Unlock()
	cancel := func() {
		a.mu.Lock()
		if current := a.responseWaiters[requestID]; current == ch {
			delete(a.responseWaiters, requestID)
		}
		a.mu.Unlock()
	}
	return ch, cancel
}

func (a *ACPTunnelAgent) notifyResponseWaiter(requestID string, payload []byte) bool {
	if requestID == "" {
		return false
	}
	a.mu.Lock()
	ch := a.responseWaiters[requestID]
	if ch != nil {
		delete(a.responseWaiters, requestID)
	}
	a.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- append([]byte(nil), payload...):
	default:
	}
	return true
}

func (a *ACPTunnelAgent) subscribeSSE(sessionID string) *acpSSESubscriber {
	sub := &acpSSESubscriber{
		sessionID: sessionID,
		ch:        make(chan []byte, 64),
	}
	a.mu.Lock()
	if a.sseSubscribers == nil {
		a.sseSubscribers = make(map[*acpSSESubscriber]struct{})
	}
	a.sseSubscribers[sub] = struct{}{}
	a.mu.Unlock()
	return sub
}

func (a *ACPTunnelAgent) unsubscribeSSE(sub *acpSSESubscriber) {
	if sub == nil {
		return
	}
	a.mu.Lock()
	if _, ok := a.sseSubscribers[sub]; ok {
		delete(a.sseSubscribers, sub)
		close(sub.ch)
	}
	a.mu.Unlock()
}

func (a *ACPTunnelAgent) broadcastSSE(payload []byte) bool {
	var rpc acpJSONRPCMessage
	_ = json.Unmarshal(payload, &rpc)
	sessionID := frameSessionID(rpc)
	a.mu.Lock()
	subs := make([]*acpSSESubscriber, 0, len(a.sseSubscribers))
	for sub := range a.sseSubscribers {
		if sub.sessionID == "" || sessionID == "" || sub.sessionID == sessionID {
			subs = append(subs, sub)
		}
	}
	a.mu.Unlock()
	for _, sub := range subs {
		select {
		case sub.ch <- append([]byte(nil), payload...):
		default:
		}
	}
	return len(subs) > 0
}

func (a *ACPTunnelAgent) hasAsyncReceivers() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.responseWaiters) > 0 || len(a.sseSubscribers) > 0
}

func (a *ACPTunnelAgent) withSessionContext(sessionID string) func() {
	if sessionID == "" {
		return func() {}
	}
	a.mu.Lock()
	previous := a.sessionID
	a.sessionID = sessionID
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		a.sessionID = previous
		a.mu.Unlock()
	}
}

func (a *ACPTunnelAgent) ensureManagerSessionID() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionID != "" {
		return a.sessionID, nil
	}
	sessionID, err := newManagerSessionID()
	if err != nil {
		return "", err
	}
	a.sessionID = sessionID
	return sessionID, nil
}

func newManagerSessionID() (string, error) {
	return auth.Secrets{}.New("sess")
}

func (a *ACPTunnelAgent) trackSessionNew(requestID string, managerSessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingSessionNew.track(requestID, managerSessionID)
}

func (a *ACPTunnelAgent) takeSessionNew(requestID string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pendingSessionNew.take(requestID)
}

func (a *ACPTunnelAgent) nextManagerRequestID() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.managerRequestSeq++
	return a.managerRequestSeq
}

// Tunnel reliability is a small durable outbox/inbox layered over WebSocket:
// outbound user->manager->paxd frames are journaled as pending, sent with a
// monotonically increasing per-agent stream seq, and advanced to acked when
// paxd durably records them. Inbound paxd->manager frames are journaled as
// received before the manager ACKs paxd; duplicate inbound seqs are ACKed again
// but are not forwarded to the user twice. The journal is transport state, not
// business history; future message/message_part projectors can consume received
// frames and mark them applied when the business write is complete.
func (a *ACPTunnelAgent) writeToAgent(ctx context.Context, messageType int, payload []byte) error {
	var raw json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return fmt.Errorf("wrap acp frame: payload must be JSON: %w", err)
	}
	engine := reliablemq.NewEngine(reliablemq.Config{}, a.store, a.reliableSender(messageType), nil)
	_, err := engine.Send(ctx, reliablemq.OutboundMessage{
		QueueID: a.queueID(),
		Stream:  reliablemq.StreamACP,
		Payload: append(json.RawMessage(nil), raw...),
		Metadata: reliablemq.Metadata{
			"agent_id": a.agentID,
			"node_id":  a.nodeID,
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func (a *ACPTunnelAgent) actorAttrs() []slog.Attr {
	return []slog.Attr{
		slog.String("agent_id", a.agentID),
		slog.String("connection_id", a.queueID()),
		slog.String("node_id", a.nodeID),
		slog.String("owner_user_id", a.ownerUserID),
		slog.String("session_id", a.sessionID),
	}
}

func (s *Service) agentACPFramePipeline() acpFramePipeline {
	return newACPFramePipeline(
		acpSessionLifecycleMiddleware{store: s.store},
		acpSessionIDMiddleware{store: s.store},
		acpApprovalMiddleware{store: s.store},
		acpRuntimeStateMiddleware{projector: s.acpRuntime},
	)
}

func (s *Service) userACPFramePipeline() acpFramePipeline {
	return newACPFramePipeline(
		acpSessionLifecycleMiddleware{store: s.store},
		acpSessionIDMiddleware{store: s.store},
		acpRuntimeStateMiddleware{projector: s.acpRuntime},
	)
}

func (a *ACPTunnelAgent) forwardAgentFrames(
	ctx context.Context,
	pipeline acpFramePipeline,
) error {
	engine := reliablemq.NewEngine(
		reliablemq.Config{},
		a.store,
		a.reliableSender(websocket.TextMessage),
		reliablemq.DispatcherFunc(func(dispatchCtx context.Context, frame reliablemq.Frame) error {
			return a.dispatchReliableACPFrame(dispatchCtx, pipeline, frame)
		}),
	)
	for {
		messageType, payload, err := a.ws.ReadMessage()
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		env, err := reliablemq.UnmarshalEnvelope(payload)
		if err != nil {
			return err
		}
		if env.QueueID != a.queueID() {
			return fmt.Errorf("unexpected reliablemq queue_id %q", env.QueueID)
		}
		if env.Metadata == nil {
			env.Metadata = reliablemq.Metadata{}
		}
		if env.Metadata["agent_id"] == "" {
			env.Metadata["agent_id"] = a.agentID
		}
		if env.Metadata["node_id"] == "" {
			env.Metadata["node_id"] = a.nodeID
		}
		if err := engine.Receive(ctx, env); err != nil {
			return err
		}
	}
}

func (s *Server) handleAgentACPTunnel(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	logging.Info(
		ctx,
		"agent acp tunnel request",
		slog.String("query_agent_id", websocketAgentID(r)),
	)
	initial, authMode, err := s.authenticateAgentACPTunnel(r)
	if err != nil {
		status, message := endpointErrorStatus(err)
		logging.Warn(
			ctx,
			"agent acp tunnel rejected",
			slog.String("query_agent_id", websocketAgentID(r)),
			slog.Int("status", status),
			slog.String("reason", message),
			logging.Err(err),
		)
		writeHTTPEndpointError(w, err)
		return
	}
	ctx = logging.With(
		ctx,
		slog.String("agent_id", initial.AgentID),
		slog.String("node_id", initial.NodeID),
		slog.String("owner_user_id", initial.OwnerUserID),
		slog.String("session_id", initial.SessionID),
		slog.String("auth_mode", authMode),
	)
	r = r.WithContext(ctx)

	ws, err := upgrader.Upgrade(w, r, websocketResponseHeader(ctx))
	if err != nil {
		logging.Error(
			ctx,
			"agent acp tunnel upgrade failed",
			logging.Err(err),
		)
		return
	}

	conn := &ACPTunnelAgent{
		agentID:      initial.AgentID,
		connectionID: firstNonEmpty(initial.ConnectionID, initial.AgentID),
		nodeID:       initial.NodeID,
		ownerUserID:  initial.OwnerUserID,
		sessionID:    initial.SessionID,
		ws:           ws,
		store:        s.store,
	}
	s.acpTunnels.add(initial.AgentID, initial.SessionID, conn)
	logging.Info(ctx, "agent acp tunnel connected")
	defer func() {
		s.acpTunnels.remove(initial.AgentID, initial.SessionID, conn)
		conn.closeUser()
		_ = ws.Close()
		logging.Info(ctx, "agent acp tunnel disconnected")
	}()

	if err := conn.replayUnackedToAgent(r.Context()); err != nil {
		logging.Warn(ctx, "agent acp tunnel replay failed", logging.Err(err))
		return
	}
	err = runACPActors(r.Context(), acpActor{
		name:  "agent_tunnel",
		attrs: conn.actorAttrs(),
		run: func(actorCtx context.Context) error {
			return conn.forwardAgentFrames(actorCtx, s.agentACPFramePipeline())
		},
	})
	if err != nil && !isWebSocketCloseError(err) {
		logging.Warn(
			ctx,
			"agent acp tunnel read ended",
			logging.Err(err),
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
			AgentID:      agent.AgentID,
			ConnectionID: websocketConnectionID(r, agent.AgentID),
			NodeID:       agent.NodeID,
			OwnerUserID:  agent.OwnerUserID,
			SessionID:    sessionID,
		}, "agent_key", nil
	}

	node, nodeErr := s.store.AuthenticateNode(r.Context(), keyHash)
	if nodeErr != nil {
		logging.Warn(
			r.Context(),
			"agent acp tunnel auth failed",
			slog.String("query_agent_id", requestAgentID),
			slog.String("key_prefix", s.secrets.Prefix(paxKey)),
			slog.String("agent_error", agentErr.Error()),
			slog.String("node_error", nodeErr.Error()),
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
		logging.Warn(
			r.Context(),
			"agent acp tunnel node auth accepted but agent not found",
			slog.String("node_id", node.NodeID),
			slog.String("query_agent_id", requestAgentID),
			logging.Err(err),
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
		AgentID:      agent.AgentID,
		ConnectionID: websocketConnectionID(r, agent.AgentID),
		NodeID:       agent.NodeID,
		OwnerUserID:  agent.OwnerUserID,
		SessionID:    sessionID,
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

func (s *Server) nativeACPSessionID(
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
		if session.SessionID == sessionID {
			return firstNonEmpty(session.NativeID, session.SessionID)
		}
		if session.NativeID == sessionID {
			return session.NativeID
		}
	}
	return sessionID
}

func (s *Server) handleUserACPTunnel(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	agentID := userTunnelAgentID(r)
	requestSessionID := userTunnelSessionID(r)
	userTunnel := userTunnelMetadata(r)
	ctx = logging.With(
		ctx,
		slog.String("agent_id", agentID),
		slog.String("requested_session_id", requestSessionID),
		slog.String("client_id", userTunnel.ClientID),
		slog.String("device_id", userTunnel.DeviceID),
		slog.String("tunnel_id", userTunnel.TunnelID),
	)
	r = r.WithContext(ctx)
	logging.Info(
		ctx,
		"user acp tunnel request",
	)
	if agentID == "" {
		logging.Warn(ctx, "user acp tunnel rejected", slog.String("reason", "missing_agent_id"))
		writeHTTPError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	principal, err := s.authorizeUserTunnel(r, agentID)
	if err != nil {
		status, message := endpointErrorStatus(err)
		logging.Warn(
			ctx,
			"user acp tunnel rejected",
			slog.Int("status", status),
			slog.String("reason", message),
			logging.Err(err),
		)
		writeHTTPEndpointError(w, err)
		return
	}
	sessionID := s.virtualACPSessionID(
		r.Context(),
		principal.User.UserID,
		agentID,
		requestSessionID,
	)
	ctx = logging.With(
		ctx,
		slog.String("owner_user_id", principal.User.UserID),
		slog.String("session_id", sessionID),
	)
	r = r.WithContext(ctx)

	nativeSessionID := s.nativeACPSessionID(
		r.Context(),
		principal.User.UserID,
		agentID,
		sessionID,
	)
	agentConn, err := s.acpTunnels.claimAny(agentID, sessionID, nativeSessionID, "")
	if err != nil {
		status, message := endpointErrorStatus(err)
		logging.Warn(
			ctx,
			"user acp tunnel claim failed",
			slog.Int("status", status),
			slog.String("reason", message),
			logging.Err(err),
		)
		writeHTTPEndpointError(w, err)
		return
	}
	defer s.acpTunnels.release(agentConn)

	userWS, err := upgrader.Upgrade(w, r, websocketResponseHeader(ctx))
	if err != nil {
		logging.Error(ctx, "user acp tunnel upgrade failed", logging.Err(err))
		return
	}
	restoreSessionContext := agentConn.withSessionContext(sessionID)
	defer restoreSessionContext()
	agentConn.attachUser(userWS)
	defer func() {
		agentConn.closeUser()
	}()

	logging.Info(ctx, "user acp tunnel connected")
	defer logging.Info(ctx, "user acp tunnel disconnected")

	err = runACPActors(r.Context(), acpActor{
		name: "user_tunnel",
		attrs: append(
			agentConn.actorAttrs(),
			slog.String("client_id", userTunnel.ClientID),
			slog.String("device_id", userTunnel.DeviceID),
			slog.String("tunnel_id", userTunnel.TunnelID),
		),
		run: func(actorCtx context.Context) error {
			return relayUserFramesToAgent(actorCtx, userWS, agentConn, s.userACPFramePipeline())
		},
	})
	if err != nil && !isWebSocketCloseError(err) {
		logging.Warn(
			ctx,
			"user acp tunnel relay ended",
			logging.Err(err),
		)
	}
}

func userTunnelMetadata(r *http.Request) acpUserTunnelMetadata {
	return acpUserTunnelMetadata{
		ClientID: firstNonEmpty(
			r.Header.Get("X-Pax-Client-ID"),
			r.URL.Query().Get("client_id"),
			r.URL.Query().Get("clientId"),
		),
		DeviceID: firstNonEmpty(
			r.Header.Get("X-Pax-Device-ID"),
			r.URL.Query().Get("device_id"),
			r.URL.Query().Get("deviceId"),
		),
		TunnelID: firstNonEmpty(
			r.Header.Get("X-Pax-Tunnel-ID"),
			r.URL.Query().Get("tunnel_id"),
			r.URL.Query().Get("tunnelId"),
			logging.NewLogID(),
		),
	}
}

func (s *Server) authorizeUserTunnel(r *http.Request, agentID string) (UserPrincipal, error) {
	principal, err := s.auth.Principal(r.Context(), httpRequestMetadata(r))
	if err != nil {
		return UserPrincipal{}, err
	}
	_, err = s.store.GetAgent(r.Context(), principal, agentID)
	return principal, err
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
			if ok && (suffix == "tunnel" || strings.HasPrefix(suffix, "sessions/")) {
				return agentID
			}
		}
	}
	if agentID := r.URL.Query().Get("agent_id"); agentID != "" {
		return agentID
	}
	return r.URL.Query().Get("agentId")
}

func userTunnelSessionID(r *http.Request) string {
	if sessionID := r.PathValue("sessionID"); sessionID != "" {
		return sessionID
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/user/"); ok {
		_, rest, ok := strings.Cut(rest, "/agents/")
		if ok {
			_, suffix, ok := strings.Cut(rest, "/")
			if ok {
				sessionID, suffix, ok := strings.Cut(strings.TrimPrefix(suffix, "sessions/"), "/")
				if ok && suffix == "tunnel" {
					return sessionID
				}
			}
		}
	}
	return websocketSessionID(r)
}

type acpJSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
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

func relayUserFramesToAgent(
	ctx context.Context,
	userWS *websocket.Conn,
	agentConn *ACPTunnelAgent,
	pipeline acpFramePipeline,
) error {
	for {
		messageType, payload, err := userWS.ReadMessage()
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		originalPayload := append([]byte(nil), payload...)
		frame := newACPFrameContext(agentConn, acpUserToAgent, messageType, payload)
		if err := pipeline.Handle(
			ctx,
			frame,
			func(_ context.Context, frame *acpFrameContext) error {
				if err := agentConn.writeToAgent(ctx, frame.messageType, frame.payload); err != nil {
					return err
				}
				return projectACPUserPrompt(ctx, agentConn, originalPayload)
			},
		); err != nil {
			return err
		}
	}
}

func (a *ACPTunnelAgent) historyGroupID(seq int64, payload json.RawMessage) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.historyGroups.groupID(seq, a.sessionID, payload)
}

func (a *ACPTunnelAgent) observeHistoryBoundary(payload json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.historyGroups.observeBoundary(payload)
}

func (a *ACPTunnelAgent) replayUnackedToAgent(ctx context.Context) error {
	engine := reliablemq.NewEngine(
		reliablemq.Config{},
		a.store,
		a.reliableSender(websocket.TextMessage),
		nil,
	)
	return engine.ReplayOutbound(ctx, a.queueID(), reliablemq.StreamACP, 1000)
}

func (a *ACPTunnelAgent) dispatchReliableACPFrame(
	ctx context.Context,
	pipeline acpFramePipeline,
	reliableFrame reliablemq.Frame,
) error {
	payload := reliableFrame.Payload
	if err := projectACPTransportMessage(
		ctx,
		a.store,
		a.agentID,
		a.ownerUserID,
		a.nodeID,
		domain.TransportStreamPaxdToManager,
		reliableFrame.Key.Seq,
		a.historyGroupID(reliableFrame.Key.Seq, payload),
		payload,
	); err != nil {
		return err
	}

	frame := newACPFrameContext(a, acpAgentToUser, websocket.TextMessage, []byte(payload))
	err := pipeline.Handle(ctx, frame, func(_ context.Context, frame *acpFrameContext) error {
		requestID := acpJSONRPCID(frame.frame)
		deliveredWaiter := a.notifyResponseWaiter(requestID, frame.payload)
		deliveredSSE := a.broadcastSSE(frame.payload)
		userWS := a.currentUser()
		if userWS == nil {
			if !deliveredWaiter && !deliveredSSE && !a.hasAsyncReceivers() {
				logging.Warn(
					ctx,
					"agent acp tunnel dropped frame without user",
					slog.String("agent_id", a.agentID),
					slog.String("connection_id", a.queueID()),
					slog.String("session_id", a.sessionID),
				)
			}
			return nil
		}
		a.userWriteMu.Lock()
		defer a.userWriteMu.Unlock()
		return userWS.WriteMessage(frame.messageType, frame.payload)
	})
	if err != nil {
		logging.Error(
			ctx,
			"agent acp tunnel frame handling failed",
			slog.String("agent_id", a.agentID),
			slog.String("connection_id", a.queueID()),
			slog.String("session_id", a.sessionID),
			logging.Err(err),
		)
	}
	if err != nil && !frame.handled {
		logging.Error(
			ctx,
			"agent acp tunnel failed to write user frame",
			slog.String("agent_id", a.agentID),
			slog.String("connection_id", a.queueID()),
			slog.String("session_id", a.sessionID),
			logging.Err(err),
		)
		a.closeUser()
		return nil
	}
	a.observeHistoryBoundary(payload)
	return nil
}

func (a *ACPTunnelAgent) reliableSender(messageType int) reliablemq.Sender {
	return reliablemq.SenderFunc(func(ctx context.Context, env reliablemq.Envelope) error {
		_ = ctx
		data, err := reliablemq.MarshalEnvelope(env)
		if err != nil {
			return err
		}
		a.agentWriteMu.Lock()
		defer a.agentWriteMu.Unlock()
		return a.ws.WriteMessage(messageType, data)
	})
}

func (a *ACPTunnelAgent) queueID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return firstNonEmpty(a.connectionID, a.agentID)
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
