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
	nodeID            string
	ownerUserID       string
	sessionID         string
	ws                *websocket.Conn
	mu                sync.Mutex
	agentWriteMu      sync.Mutex
	userWriteMu       sync.Mutex
	paired            bool
	userWS            *websocket.Conn
	store             domain.Store
	historyGroups     map[string]string
	pendingSessionNew map[string]string
}

type acpTunnelEnvelope struct {
	Type    string          `json:"type"`
	Stream  string          `json:"stream"`
	Seq     int64           `json:"seq"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

const (
	acpTunnelTypeData            = "data"
	acpTunnelTypeAck             = "ack"
	acpTunnelStreamManagerToPaxd = "manager_to_paxd"
	acpTunnelStreamPaxdToManager = "paxd_to_manager"
)

type acpUserTunnelMetadata struct {
	ClientID string
	DeviceID string
	TunnelID string
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
	if requestID == "" || managerSessionID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingSessionNew == nil {
		a.pendingSessionNew = make(map[string]string)
	}
	a.pendingSessionNew[requestID] = managerSessionID
}

func (a *ACPTunnelAgent) takeSessionNew(requestID string) (string, bool) {
	if requestID == "" {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingSessionNew == nil {
		return "", false
	}
	managerSessionID, ok := a.pendingSessionNew[requestID]
	delete(a.pendingSessionNew, requestID)
	return managerSessionID, ok
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
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	enveloped, seq, err := a.wrapManagerToPaxd(ctx, payload)
	if err != nil {
		return err
	}
	if err := a.ws.WriteMessage(messageType, enveloped); err != nil {
		return err
	}
	return a.store.UpdateTransportFrameStatus(
		ctx,
		a.agentID,
		domain.TransportStreamManagerToPaxd,
		seq,
		domain.TransportDirectionOutbound,
		domain.TransportStatusSent,
		"",
	)
}

func (a *ACPTunnelAgent) actorAttrs() []slog.Attr {
	return []slog.Attr{
		slog.String("agent_id", a.agentID),
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
	for {
		messageType, payload, err := a.ws.ReadMessage()
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		env, ok, err := decodePaxdToManager(payload)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if env.Type == acpTunnelTypeAck {
			if err := a.store.AckOutboundTransportFrames(
				ctx,
				a.agentID,
				domain.TransportStreamManagerToPaxd,
				env.Seq,
			); err != nil {
				return err
			}
			continue
		}
		inserted, err := a.store.SaveTransportFrameIfAbsent(ctx, &domain.TransportFrame{
			AgentID:        a.agentID,
			Stream:         domain.TransportStreamPaxdToManager,
			Seq:            env.Seq,
			LocalDirection: domain.TransportDirectionInbound,
			PayloadJSON:    append(json.RawMessage(nil), env.Payload...),
			Status:         domain.TransportStatusReceived,
		})
		if err != nil {
			return err
		}
		if !inserted {
			if err := a.writeAckToAgent(messageType, domain.TransportStreamPaxdToManager, env.Seq); err != nil {
				return err
			}
			continue
		}
		if err := projectACPTransportMessage(
			ctx,
			a.store,
			a.agentID,
			a.ownerUserID,
			a.nodeID,
			domain.TransportStreamPaxdToManager,
			env.Seq,
			a.historyGroupID(env.Seq, env.Payload),
			env.Payload,
		); err != nil {
			return err
		}
		if err := a.writeAckToAgent(messageType, domain.TransportStreamPaxdToManager, env.Seq); err != nil {
			return err
		}

		frame := newACPFrameContext(a, acpAgentToUser, messageType, []byte(env.Payload))
		err = pipeline.Handle(ctx, frame, func(_ context.Context, frame *acpFrameContext) error {
			userWS := a.currentUser()
			if userWS == nil {
				logging.Warn(
					ctx,
					"agent acp tunnel dropped frame without user",
					slog.String("agent_id", a.agentID),
					slog.String("session_id", a.sessionID),
				)
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
				slog.String("session_id", a.sessionID),
				logging.Err(err),
			)
		}
		if err != nil && !frame.handled {
			logging.Error(
				ctx,
				"agent acp tunnel failed to write user frame",
				slog.String("agent_id", a.agentID),
				slog.String("session_id", a.sessionID),
				logging.Err(err),
			)
			a.closeUser()
			continue
		}
		if err := a.store.UpdateTransportFrameStatus(
			ctx,
			a.agentID,
			domain.TransportStreamPaxdToManager,
			env.Seq,
			domain.TransportDirectionInbound,
			domain.TransportStatusApplied,
			"",
		); err != nil {
			return err
		}
		a.observeHistoryBoundary(env.Payload)
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
		agentID:     initial.AgentID,
		nodeID:      initial.NodeID,
		ownerUserID: initial.OwnerUserID,
		sessionID:   initial.SessionID,
		ws:          ws,
		store:       s.store,
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
			AgentID:     agent.AgentID,
			NodeID:      agent.NodeID,
			OwnerUserID: agent.OwnerUserID,
			SessionID:   sessionID,
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

func (a *ACPTunnelAgent) wrapManagerToPaxd(
	ctx context.Context,
	payload []byte,
) ([]byte, int64, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, 0, fmt.Errorf("wrap acp frame: payload must be JSON: %w", err)
	}
	seq, err := a.store.NextTransportSeq(
		ctx,
		a.agentID,
		domain.TransportStreamManagerToPaxd,
		domain.TransportDirectionOutbound,
	)
	if err != nil {
		return nil, 0, err
	}
	frame := &domain.TransportFrame{
		AgentID:        a.agentID,
		Stream:         domain.TransportStreamManagerToPaxd,
		Seq:            seq,
		LocalDirection: domain.TransportDirectionOutbound,
		PayloadJSON:    append(json.RawMessage(nil), raw...),
		Status:         domain.TransportStatusPending,
	}
	if err := a.store.SaveTransportFrame(ctx, frame); err != nil {
		return nil, 0, err
	}
	enveloped, err := marshalACPTunnelData(domain.TransportStreamManagerToPaxd, seq, raw)
	return enveloped, seq, err
}

func (a *ACPTunnelAgent) historyGroupID(seq int64, payload json.RawMessage) string {
	var rpc acpHistoryRPC
	_ = json.Unmarshal(payload, &rpc)
	fields := extractACPHistoryFields(payload, rpc)
	fields, projection := classifyACPHistoryProjection(rpc, fields)
	if projection != acpHistoryProjectionText {
		return ""
	}
	key := firstNonEmpty(fields.SessionID, a.sessionID) + "\x00" +
		firstNonEmpty(fields.SessionUpdate, "_") + "\x00" +
		firstNonEmpty(fields.Role, "_")
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.historyGroups == nil {
		a.historyGroups = make(map[string]string)
	}
	if groupID, ok := a.historyGroups[key]; ok {
		return groupID
	}
	groupID := fmt.Sprintf("seq:%d", seq)
	a.historyGroups[key] = groupID
	return groupID
}

func (a *ACPTunnelAgent) observeHistoryBoundary(payload json.RawMessage) {
	var rpc acpJSONRPCMessage
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return
	}
	if len(rpc.Result) == 0 && len(rpc.Error) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.historyGroups = nil
}

func (a *ACPTunnelAgent) replayUnackedToAgent(ctx context.Context) error {
	frames, err := a.store.ListTransportFrames(
		ctx,
		a.agentID,
		domain.TransportStreamManagerToPaxd,
		domain.TransportDirectionOutbound,
		[]string{
			domain.TransportStatusPending,
			domain.TransportStatusSent,
			domain.TransportStatusFailed,
		},
		1000,
	)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		payload, err := marshalACPTunnelData(frame.Stream, frame.Seq, frame.PayloadJSON)
		if err != nil {
			return err
		}
		a.agentWriteMu.Lock()
		err = a.ws.WriteMessage(websocket.TextMessage, payload)
		a.agentWriteMu.Unlock()
		if err != nil {
			return err
		}
		if err := a.store.UpdateTransportFrameStatus(
			ctx,
			a.agentID,
			frame.Stream,
			frame.Seq,
			domain.TransportDirectionOutbound,
			domain.TransportStatusSent,
			"",
		); err != nil {
			return err
		}
	}
	return nil
}

func marshalACPTunnelData(stream string, seq int64, payload json.RawMessage) ([]byte, error) {
	return json.Marshal(acpTunnelEnvelope{
		Type:    acpTunnelTypeData,
		Stream:  stream,
		Seq:     seq,
		Payload: payload,
	})
}

func (a *ACPTunnelAgent) writeAckToAgent(messageType int, stream string, seq int64) error {
	ack, err := json.Marshal(acpTunnelEnvelope{
		Type:   acpTunnelTypeAck,
		Stream: stream,
		Seq:    seq,
	})
	if err != nil {
		return err
	}
	a.agentWriteMu.Lock()
	defer a.agentWriteMu.Unlock()
	return a.ws.WriteMessage(messageType, ack)
}

func decodePaxdToManager(payload []byte) (acpTunnelEnvelope, bool, error) {
	var env acpTunnelEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return acpTunnelEnvelope{}, false, fmt.Errorf("decode acp tunnel envelope: %w", err)
	}
	if env.Type != acpTunnelTypeData && env.Type != acpTunnelTypeAck {
		return acpTunnelEnvelope{}, false, nil
	}
	if env.Type == acpTunnelTypeData && env.Stream != acpTunnelStreamPaxdToManager {
		return acpTunnelEnvelope{}, false, fmt.Errorf("unexpected acp tunnel stream %q", env.Stream)
	}
	if env.Type == acpTunnelTypeAck && env.Stream != acpTunnelStreamManagerToPaxd {
		return acpTunnelEnvelope{}, false, fmt.Errorf(
			"unexpected acp tunnel ack stream %q",
			env.Stream,
		)
	}
	if env.Seq <= 0 {
		return acpTunnelEnvelope{}, false, fmt.Errorf("invalid acp tunnel seq %d", env.Seq)
	}
	if env.Type == acpTunnelTypeData && len(env.Payload) == 0 {
		return acpTunnelEnvelope{}, false, fmt.Errorf("missing acp tunnel payload")
	}
	return env, true, nil
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
