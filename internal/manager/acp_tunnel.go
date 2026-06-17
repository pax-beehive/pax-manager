package manager

import (
	"context"
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
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type ACPTunnelHub struct {
	mu     sync.RWMutex
	agents map[string]*ACPTunnelAgent
}

type ACPTunnelAgent struct {
	agentID      string
	ws           *websocket.Conn
	mu           sync.Mutex
	agentWriteMu sync.Mutex
	userWriteMu  sync.Mutex
	paired       bool
	userWS       *websocket.Conn
	store        domain.Store
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

func (a *ACPTunnelAgent) forwardAgentFrames() error {
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
				context.Background(),
				a.agentID,
				domain.TransportStreamManagerToPaxd,
				env.Seq,
			); err != nil {
				return err
			}
			continue
		}
		rawPayload := []byte(env.Payload)
		inserted, err := a.store.SaveTransportFrameIfAbsent(context.Background(), &domain.TransportFrame{
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
		if err := projectACPTransportMessage(
			context.Background(),
			a.store,
			a.agentID,
			domain.TransportStreamPaxdToManager,
			env.Seq,
			env.Payload,
		); err != nil {
			return err
		}
		if err := a.writeAckToAgent(messageType, domain.TransportStreamPaxdToManager, env.Seq); err != nil {
			return err
		}
		if !inserted {
			continue
		}
		userWS := a.currentUser()
		if userWS == nil {
			log.Printf("agent acp tunnel dropped frame without user: agent_id=%s", a.agentID)
			continue
		}
		a.userWriteMu.Lock()
		err = userWS.WriteMessage(messageType, rawPayload)
		a.userWriteMu.Unlock()
		if err != nil {
			log.Printf("agent acp tunnel failed to write user frame: agent_id=%s err=%v", a.agentID, err)
			a.closeUser()
			continue
		}
		if err := a.store.UpdateTransportFrameStatus(
			context.Background(),
			a.agentID,
			domain.TransportStreamPaxdToManager,
			env.Seq,
			domain.TransportDirectionInbound,
			domain.TransportStatusApplied,
			"",
		); err != nil {
			return err
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

	conn := &ACPTunnelAgent{agentID: initial.AgentID, ws: ws, store: s.store}
	s.acpTunnels.add(initial.AgentID, conn)
	log.Printf("agent acp tunnel connected: agent_id=%s auth_mode=%s", initial.AgentID, authMode)
	defer func() {
		s.acpTunnels.remove(initial.AgentID, conn)
		conn.closeUser()
		_ = ws.Close()
		log.Printf("agent acp tunnel disconnected: agent_id=%s auth_mode=%s", initial.AgentID, authMode)
	}()

	if err := conn.replayUnackedToAgent(r.Context()); err != nil {
		log.Printf("agent acp tunnel replay failed: agent_id=%s auth_mode=%s err=%v", initial.AgentID, authMode, err)
		return
	}
	err = conn.forwardAgentFrames()
	if err != nil && !isWebSocketCloseError(err) {
		log.Printf("agent acp tunnel read ended: agent_id=%s auth_mode=%s err=%v", initial.AgentID, authMode, err)
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

func relayUserFramesToAgent(userWS *websocket.Conn, agentConn *ACPTunnelAgent) error {
	for {
		messageType, payload, err := userWS.ReadMessage()
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		if err := agentConn.writeToAgent(context.Background(), messageType, payload); err != nil {
			return err
		}
	}
}

func (a *ACPTunnelAgent) wrapManagerToPaxd(ctx context.Context, payload []byte) ([]byte, int64, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, 0, fmt.Errorf("wrap acp frame: payload must be JSON: %w", err)
	}
	seq, err := a.store.NextTransportSeq(ctx, a.agentID, domain.TransportStreamManagerToPaxd, domain.TransportDirectionOutbound)
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
	if err := projectACPTransportMessage(
		ctx,
		a.store,
		a.agentID,
		domain.TransportStreamManagerToPaxd,
		seq,
		raw,
	); err != nil {
		return nil, 0, err
	}
	enveloped, err := marshalACPTunnelData(domain.TransportStreamManagerToPaxd, seq, raw)
	return enveloped, seq, err
}

func (a *ACPTunnelAgent) replayUnackedToAgent(ctx context.Context) error {
	frames, err := a.store.ListTransportFrames(
		ctx,
		a.agentID,
		domain.TransportStreamManagerToPaxd,
		domain.TransportDirectionOutbound,
		[]string{domain.TransportStatusPending, domain.TransportStatusSent, domain.TransportStatusFailed},
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
		return acpTunnelEnvelope{}, false, fmt.Errorf("unexpected acp tunnel ack stream %q", env.Stream)
	}
	if env.Seq <= 0 {
		return acpTunnelEnvelope{}, false, fmt.Errorf("invalid acp tunnel seq %d", env.Seq)
	}
	if env.Type == acpTunnelTypeData && len(env.Payload) == 0 {
		return acpTunnelEnvelope{}, false, fmt.Errorf("missing acp tunnel payload")
	}
	return env, true, nil
}

func unwrapPaxdToManager(payload []byte) ([]byte, bool, error) {
	env, ok, err := decodePaxdToManager(payload)
	if err != nil || !ok || env.Type != acpTunnelTypeData {
		return nil, ok, err
	}
	return env.Payload, true, nil
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
