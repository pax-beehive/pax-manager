package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	e2eeProtocolVersion = 1
	e2eeCipherVersion   = 1
	e2eeDrainLimit      = 100
	e2eeMaxCiphertext   = 1 << 20
)

type e2eeEnvelope struct {
	ProtocolVersion int    `json:"protocol_version"`
	CipherVersion   int    `json:"cipher_version"`
	KeyEpoch        int64  `json:"key_epoch"`
	RecordID        string `json:"record_id"`
	AgentID         string `json:"agent_id"`
	SessionID       string `json:"session_id"`
	Kind            string `json:"kind"`
	Nonce           string `json:"nonce"`
	Payload         string `json:"payload"`
}

type e2eeCommandAck struct {
	Type            string `json:"type"`
	CommandID       string `json:"command_id"`
	ConnectionEpoch int64  `json:"connection_epoch"`
}

type e2eeWakeRegistry struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newE2EEWakeRegistry() *e2eeWakeRegistry {
	return &e2eeWakeRegistry{waiters: make(map[string]map[chan struct{}]struct{})}
}

func (h *e2eeWakeRegistry) subscribe(key string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.waiters[key] == nil {
		h.waiters[key] = make(map[chan struct{}]struct{})
	}
	h.waiters[key][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.waiters[key], ch)
		if len(h.waiters[key]) == 0 {
			delete(h.waiters, key)
		}
		h.mu.Unlock()
	}
}

func (h *e2eeWakeRegistry) wake(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.waiters[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *e2eeWakeRegistry) wakeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, waiters := range h.waiters {
		for ch := range waiters {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

func (s *Service) handleE2EECommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	control, err := s.resolveConversationTurnControlSession(r)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	var envelope e2eeEnvelope
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, e2eeMaxCiphertext*2))
	if err := decoder.Decode(&envelope); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid encrypted command")
		return
	}
	if envelope.AgentID != control.agent.AgentID ||
		envelope.SessionID != control.session.SessionID {
		writeHTTPError(
			w,
			http.StatusBadRequest,
			"encrypted command route does not match request path",
		)
		return
	}
	record, err := decodeE2EERecord(envelope)
	if err != nil || envelope.Kind != "acp_command" {
		writeHTTPError(
			w,
			http.StatusBadRequest,
			firstNonEmpty(errorString(err), "invalid encrypted command kind"),
		)
		return
	}
	command, created, err := s.store.CreateAgentCommand(r.Context(), domain.AgentCommand{
		E2EERecord: domain.E2EERecord{
			RecordID: record.RecordID, OwnerUserID: control.principal.User.UserID,
			NodeID: control.agent.NodeID, AgentID: record.AgentID,
			SessionID: record.SessionID, Kind: record.Kind,
			ProtocolVersion: record.ProtocolVersion, CipherVersion: record.CipherVersion,
			KeyEpoch: record.KeyEpoch, Nonce: record.Nonce,
			Ciphertext: record.Ciphertext, CreatedAt: s.clock().UTC(),
		},
	})
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	s.acpTunnels.wakeAgent(command.AgentID)
	writeHTTPData(w, http.StatusAccepted, map[string]any{
		"command_id": command.RecordID,
		"created":    created,
		"status":     commandStatus(command),
	})
}

func (s *Service) handleE2EEEvents(w http.ResponseWriter, r *http.Request) {
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
	cursor, err := e2eeEventCursor(r)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid event cursor")
		return
	}
	wake, unsubscribe := s.e2eeEventWakes.subscribe(control.session.SessionID)
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		events, err := s.store.ListAgentEvents(
			r.Context(),
			control.principal.User.UserID,
			control.session.SessionID,
			cursor,
			e2eeDrainLimit,
		)
		if err != nil {
			return
		}
		for _, event := range events {
			if err := writeE2EEEvent(w, flusher, event); err != nil {
				return
			}
			cursor = event.Cursor
		}
		if len(events) == e2eeDrainLimit {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (a *ACPTunnelAgent) runE2EECommandSender(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.e2eeReady:
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := a.drainE2EECommands(ctx); err != nil && !errors.Is(err, domain.ErrConflict) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.commandWake:
		case <-ticker.C:
		}
	}
}

func (a *ACPTunnelAgent) drainE2EECommands(ctx context.Context) error {
	currentEpoch, err := a.store.CurrentAgentConnectionEpoch(ctx, a.agentID)
	if err != nil {
		return err
	}
	if currentEpoch != a.connectionEpoch {
		return domain.ErrConflict
	}
	for {
		commands, err := a.store.ListPendingAgentCommands(
			ctx,
			a.agentID,
			a.connectionEpoch,
			e2eeDrainLimit,
		)
		if err != nil {
			return err
		}
		for _, command := range commands {
			payload, err := json.Marshal(encodeE2EERecord(command.E2EERecord))
			if err != nil {
				return err
			}
			if err := a.writeToAgentWithMetadata(
				ctx,
				websocket.TextMessage,
				payload,
				map[string]string{
					"e2ee_kind":          "command",
					"command_id":         command.RecordID,
					"manager_session_id": command.SessionID,
					"connection_epoch":   strconv.FormatInt(a.connectionEpoch, 10),
				},
			); err != nil {
				return err
			}
			if err := a.store.MarkAgentCommandDelivered(
				ctx,
				command.RecordID,
				a.connectionEpoch,
			); err != nil {
				return err
			}
		}
		if len(commands) < e2eeDrainLimit {
			return nil
		}
	}
}

func (a *ACPTunnelAgent) handleE2EEAgentPayload(ctx context.Context, payload []byte) (bool, error) {
	var ack e2eeCommandAck
	if err := json.Unmarshal(payload, &ack); err == nil && ack.Type == "e2ee_command_ack" {
		if ack.CommandID == "" {
			return true, errors.New("invalid encrypted command acknowledgement")
		}
		if ack.ConnectionEpoch != a.connectionEpoch {
			return true, nil
		}
		err := a.store.AcknowledgeAgentCommand(ctx, ack.CommandID, ack.ConnectionEpoch)
		if errors.Is(err, domain.ErrConflict) {
			return true, nil
		}
		return true, err
	}
	var envelope e2eeEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.ProtocolVersion == 0 {
		return false, nil
	}
	if envelope.Kind != "acp_event" || envelope.AgentID != a.agentID {
		return true, errors.New("invalid encrypted agent event route")
	}
	currentEpoch, err := a.store.CurrentAgentConnectionEpoch(ctx, a.agentID)
	if err != nil {
		return true, err
	}
	if currentEpoch != a.connectionEpoch {
		return true, nil
	}
	record, err := decodeE2EERecord(envelope)
	if err != nil {
		return true, err
	}
	event, inserted, err := a.store.InsertAgentEvent(
		ctx,
		domain.AgentEvent{E2EERecord: domain.E2EERecord{
			RecordID: record.RecordID, OwnerUserID: a.ownerUserID, NodeID: a.nodeID,
			AgentID: record.AgentID, SessionID: record.SessionID, Kind: record.Kind,
			ProtocolVersion: record.ProtocolVersion, CipherVersion: record.CipherVersion,
			KeyEpoch: record.KeyEpoch, Nonce: record.Nonce, Ciphertext: record.Ciphertext,
			CreatedAt: time.Now().UTC(),
		}},
	)
	if err == nil && inserted && a.eventWakes != nil {
		a.eventWakes.wake(event.SessionID)
	}
	return true, err
}

func decodeE2EERecord(envelope e2eeEnvelope) (domain.E2EERecord, error) {
	if envelope.ProtocolVersion != e2eeProtocolVersion ||
		envelope.CipherVersion != e2eeCipherVersion {
		return domain.E2EERecord{}, errors.New("unsupported E2EE envelope version")
	}
	if envelope.KeyEpoch < 1 ||
		!validE2EEIdentifier(envelope.RecordID) ||
		!validE2EEIdentifier(envelope.AgentID) ||
		!validE2EEIdentifier(envelope.SessionID) ||
		!validE2EEIdentifier(envelope.Kind) {
		return domain.E2EERecord{}, errors.New("incomplete E2EE envelope metadata")
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != 12 {
		return domain.E2EERecord{}, errors.New("invalid E2EE nonce")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil || len(ciphertext) < 16 || len(ciphertext) > e2eeMaxCiphertext {
		return domain.E2EERecord{}, errors.New("invalid E2EE ciphertext")
	}
	return domain.E2EERecord{
		RecordID: envelope.RecordID, AgentID: envelope.AgentID,
		SessionID: envelope.SessionID, Kind: envelope.Kind,
		ProtocolVersion: envelope.ProtocolVersion, CipherVersion: envelope.CipherVersion,
		KeyEpoch: envelope.KeyEpoch, Nonce: nonce, Ciphertext: ciphertext,
	}, nil
}

func validE2EEIdentifier(value string) bool {
	return value != "" && len(value) <= 512 && !strings.ContainsRune(value, '\x00')
}

func encodeE2EERecord(record domain.E2EERecord) e2eeEnvelope {
	return e2eeEnvelope{
		ProtocolVersion: record.ProtocolVersion, CipherVersion: record.CipherVersion,
		KeyEpoch: record.KeyEpoch, RecordID: record.RecordID, AgentID: record.AgentID,
		SessionID: record.SessionID, Kind: record.Kind,
		Nonce:   base64.StdEncoding.EncodeToString(record.Nonce),
		Payload: base64.StdEncoding.EncodeToString(record.Ciphertext),
	}
}

func writeE2EEEvent(w http.ResponseWriter, flusher http.Flusher, event domain.AgentEvent) error {
	data, err := json.Marshal(encodeE2EERecord(event.E2EERecord))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.Cursor, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func e2eeEventCursor(r *http.Request) (int64, error) {
	raw := firstNonEmpty(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("after_cursor"))
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cursor < 0 {
		return 0, errors.New("invalid cursor")
	}
	return cursor, nil
}

func commandStatus(command domain.AgentCommand) string {
	if command.AcknowledgedAt != nil {
		return "acknowledged"
	}
	if command.DeliveredAt != nil {
		return "delivered"
	}
	return "pending"
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
