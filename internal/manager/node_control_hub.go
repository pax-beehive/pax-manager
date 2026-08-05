package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var (
	ErrNodeControlNotConnected = errors.New("node control tunnel is not connected")
	ErrNodeControlDisconnected = errors.New("node control tunnel disconnected")
	ErrNodeControlReplaced     = errors.New("node control tunnel replaced")
)

type nodeControlWebSocket interface {
	WriteMessage(messageType int, payload []byte) error
	Close() error
}

type nodeControlPendingRequest struct {
	requestID string
	commandID string
	result    chan nodeControlResult
}

type nodeControlResult struct {
	payload json.RawMessage
	err     error
}

type nodeControlIncomingFrame struct {
	Kind        string                `json:"kind"`
	RequestID   string                `json:"request_id,omitempty"`
	CommandID   string                `json:"command_id,omitempty"`
	QueryResult json.RawMessage       `json:"query_result,omitempty"`
	CommandAck  json.RawMessage       `json:"command_ack,omitempty"`
	Error       *nodeControlWireError `json:"error,omitempty"`
}

type nodeControlWireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Target  string `json:"target,omitempty"`
}

func (e nodeControlWireError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type nodeControlConnection struct {
	nodeID string
	ws     nodeControlWebSocket

	requestMu sync.Mutex
	stateMu   sync.Mutex
	pending   *nodeControlPendingRequest
	failure   error
	remoteID  string
}

func newNodeControlConnection(
	nodeID string,
	ws nodeControlWebSocket,
) *nodeControlConnection {
	return &nodeControlConnection{nodeID: nodeID, ws: ws}
}

func (c *nodeControlConnection) Query(
	ctx context.Context,
	requestID string,
	query any,
) (json.RawMessage, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, errors.New("node control request id is required")
	}
	return c.send(ctx, nodeControlPendingRequest{requestID: requestID}, struct {
		Kind      string `json:"kind"`
		RequestID string `json:"request_id"`
		Query     any    `json:"query"`
	}{
		Kind:      "query",
		RequestID: requestID,
		Query:     query,
	})
}

func (c *nodeControlConnection) Command(
	ctx context.Context,
	commandID string,
	command any,
) (json.RawMessage, error) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return nil, errors.New("node control command id is required")
	}
	return c.send(ctx, nodeControlPendingRequest{commandID: commandID}, struct {
		Kind      string `json:"kind"`
		CommandID string `json:"command_id"`
		Command   any    `json:"command"`
	}{
		Kind:      "command",
		CommandID: commandID,
		Command:   command,
	})
}

func (c *nodeControlConnection) send(
	ctx context.Context,
	pending nodeControlPendingRequest,
	frame any,
) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	payload, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("encode node control request: %w", err)
	}
	pending.result = make(chan nodeControlResult, 1)
	if err := c.beginRequest(&pending); err != nil {
		return nil, err
	}
	if c.ws == nil {
		c.clearPending(&pending)
		return nil, ErrNodeControlNotConnected
	}
	if err := c.ws.WriteMessage(websocket.TextMessage, payload); err != nil {
		c.clearPending(&pending)
		return nil, fmt.Errorf("write node control request: %w", err)
	}

	select {
	case <-ctx.Done():
		c.clearPending(&pending)
		return nil, ctx.Err()
	case result := <-pending.result:
		return result.payload, result.err
	}
}

func (c *nodeControlConnection) beginRequest(pending *nodeControlPendingRequest) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.failure != nil {
		return c.failure
	}
	if c.pending != nil {
		return errors.New("node control request already pending")
	}
	c.pending = pending
	return nil
}

func (c *nodeControlConnection) clearPending(pending *nodeControlPendingRequest) {
	c.stateMu.Lock()
	if c.pending == pending {
		c.pending = nil
	}
	c.stateMu.Unlock()
}

func (c *nodeControlConnection) HandleIncoming(payload []byte) (bool, error) {
	var frame nodeControlIncomingFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return false, fmt.Errorf("decode node control frame: %w", err)
	}
	switch frame.Kind {
	case "ack", "command_result", "response", "error":
		return true, c.deliver(frame)
	default:
		return false, nil
	}
}

func (c *nodeControlConnection) ObserveReport(payload []byte) error {
	var frame struct {
		Kind   string `json:"kind"`
		Report struct {
			RemoteID string `json:"remote_id"`
		} `json:"report"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		return fmt.Errorf("decode node control report identity: %w", err)
	}
	if frame.Kind != "report" {
		return nil
	}
	c.setRemoteID(frame.Report.RemoteID)
	return nil
}

func (c *nodeControlConnection) setRemoteID(remoteID string) {
	remoteID = strings.TrimSpace(remoteID)
	if remoteID == "" {
		return
	}
	c.stateMu.Lock()
	c.remoteID = remoteID
	c.stateMu.Unlock()
}

func (c *nodeControlConnection) RemoteID() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.remoteID
}

func (c *nodeControlConnection) deliver(frame nodeControlIncomingFrame) error {
	c.stateMu.Lock()
	pending := c.pending
	if pending == nil {
		c.stateMu.Unlock()
		return nil
	}
	if err := pending.matches(frame); err != nil {
		c.stateMu.Unlock()
		return err
	}
	c.pending = nil
	c.stateMu.Unlock()

	result := nodeControlResult{}
	switch frame.Kind {
	case "ack", "command_result":
		if len(frame.CommandAck) == 0 {
			result.err = errors.New("node control command response is missing command_ack")
		} else {
			result.payload = cloneRawJSON(frame.CommandAck)
		}
	case "response":
		if len(frame.QueryResult) == 0 {
			result.err = errors.New("node control query response is missing query_result")
		} else {
			result.payload = cloneRawJSON(frame.QueryResult)
		}
	case "error":
		if frame.Error == nil {
			result.err = errors.New("node control error response is missing error")
		} else {
			result.err = *frame.Error
		}
	}
	pending.result <- result
	return nil
}

func (p *nodeControlPendingRequest) matches(frame nodeControlIncomingFrame) error {
	switch frame.Kind {
	case "ack", "command_result":
		if p.commandID == "" || frame.CommandID != p.commandID {
			return fmt.Errorf(
				"unexpected node control command response %q",
				frame.CommandID,
			)
		}
	case "response":
		if p.requestID == "" || frame.RequestID != p.requestID {
			return fmt.Errorf(
				"unexpected node control query response %q",
				frame.RequestID,
			)
		}
	case "error":
		if frame.CommandID != "" && frame.CommandID != p.commandID {
			return fmt.Errorf("unexpected node control command error %q", frame.CommandID)
		}
		if frame.RequestID != "" && frame.RequestID != p.requestID {
			return fmt.Errorf("unexpected node control query error %q", frame.RequestID)
		}
	}
	return nil
}

func (c *nodeControlConnection) Fail(err error) {
	if err == nil {
		err = ErrNodeControlDisconnected
	}
	c.stateMu.Lock()
	if c.failure != nil {
		c.stateMu.Unlock()
		return
	}
	c.failure = err
	pending := c.pending
	c.pending = nil
	c.stateMu.Unlock()
	if pending != nil {
		pending.result <- nodeControlResult{err: err}
	}
}

func (c *nodeControlConnection) Err() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.failure
}

type NodeControlHub struct {
	mu               sync.RWMutex
	connections      map[string]*nodeControlConnection
	attachmentStates map[nodeAttachmentKey]nodeControlAttachmentLocalState
	heartbeats       map[string]domain.NodeDaemonHeartbeat
	maintenance      map[string]map[string]domain.NodeDaemonMaintenanceConfirmation
}

func NewNodeControlHub() *NodeControlHub {
	return &NodeControlHub{
		connections:      make(map[string]*nodeControlConnection),
		attachmentStates: make(map[nodeAttachmentKey]nodeControlAttachmentLocalState),
		heartbeats:       make(map[string]domain.NodeDaemonHeartbeat),
		maintenance:      make(map[string]map[string]domain.NodeDaemonMaintenanceConfirmation),
	}
}

func (h *NodeControlHub) Add(nodeID string, conn *nodeControlConnection) {
	if h == nil || conn == nil {
		return
	}
	h.mu.Lock()
	if h.connections == nil {
		h.connections = make(map[string]*nodeControlConnection)
	}
	previous := h.connections[nodeID]
	h.connections[nodeID] = conn
	h.clearAttachmentStatesLocked(nodeID)
	h.mu.Unlock()
	if previous != nil && previous != conn {
		previous.Fail(ErrNodeControlReplaced)
		if previous.ws != nil {
			_ = previous.ws.Close()
		}
	}
}

func (h *NodeControlHub) Remove(nodeID string, conn *nodeControlConnection) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.connections[nodeID] == conn {
		delete(h.connections, nodeID)
		h.clearAttachmentStatesLocked(nodeID)
	}
	h.mu.Unlock()
}

func (h *NodeControlHub) Connection(nodeID string) (*nodeControlConnection, error) {
	if h == nil {
		return nil, ErrNodeControlNotConnected
	}
	h.mu.RLock()
	conn := h.connections[nodeID]
	h.mu.RUnlock()
	if conn == nil || conn.Err() != nil {
		return nil, ErrNodeControlNotConnected
	}
	return conn, nil
}

func (h *NodeControlHub) Query(
	ctx context.Context,
	nodeID string,
	requestID string,
	query any,
) (json.RawMessage, error) {
	conn, err := h.Connection(nodeID)
	if err != nil {
		return nil, err
	}
	return conn.Query(ctx, requestID, query)
}

func (h *NodeControlHub) RemoteID(nodeID string) (string, error) {
	conn, err := h.Connection(nodeID)
	if err != nil {
		return "", err
	}
	remoteID := conn.RemoteID()
	if remoteID == "" {
		return "", errors.New("node control tunnel has not reported a remote id")
	}
	return remoteID, nil
}

func (h *NodeControlHub) Command(
	ctx context.Context,
	nodeID string,
	commandID string,
	command any,
) (json.RawMessage, error) {
	conn, err := h.Connection(nodeID)
	if err != nil {
		return nil, err
	}
	return conn.Command(ctx, commandID, command)
}
func (h *NodeControlHub) ObserveHeartbeat(nodeID string, heartbeat domain.NodeDaemonHeartbeat) {
	if h == nil || strings.TrimSpace(nodeID) == "" {
		return
	}
	heartbeat.BootID = strings.TrimSpace(heartbeat.BootID)
	heartbeat.PaxdVersion = strings.TrimSpace(heartbeat.PaxdVersion)
	heartbeat.DaemonPhase = strings.TrimSpace(heartbeat.DaemonPhase)
	if heartbeat.ObservedAt.IsZero() {
		heartbeat.ObservedAt = time.Now().UTC()
	}
	h.mu.Lock()
	if h.heartbeats == nil {
		h.heartbeats = make(map[string]domain.NodeDaemonHeartbeat)
	}
	h.heartbeats[nodeID] = heartbeat
	for commandID, confirmation := range h.maintenance[nodeID] {
		h.maintenance[nodeID][commandID] = evaluateMaintenanceConfirmation(confirmation, heartbeat)
	}
	h.mu.Unlock()
}

func (h *NodeControlHub) TrackMaintenance(
	nodeID string,
	commandID string,
	requestedBootID string,
	expectedVersion string,
) {
	if h == nil {
		return
	}
	nodeID = strings.TrimSpace(nodeID)
	commandID = strings.TrimSpace(commandID)
	if nodeID == "" || commandID == "" {
		return
	}
	confirmation := domain.NodeDaemonMaintenanceConfirmation{
		CommandID:       commandID,
		RequestedBootID: strings.TrimSpace(requestedBootID),
		ExpectedVersion: strings.TrimSpace(expectedVersion),
		Status:          "awaiting_new_boot",
		UpdatedAt:       time.Now().UTC(),
	}
	h.mu.Lock()
	if h.maintenance == nil {
		h.maintenance = make(map[string]map[string]domain.NodeDaemonMaintenanceConfirmation)
	}
	if h.maintenance[nodeID] == nil {
		h.maintenance[nodeID] = make(map[string]domain.NodeDaemonMaintenanceConfirmation)
	}
	if heartbeat, ok := h.heartbeats[nodeID]; ok {
		confirmation = evaluateMaintenanceConfirmation(confirmation, heartbeat)
	}
	h.maintenance[nodeID][commandID] = confirmation
	h.mu.Unlock()
}

func (h *NodeControlHub) MaintenanceConfirmation(
	nodeID string,
	commandID string,
) (domain.NodeDaemonMaintenanceConfirmation, bool) {
	if h == nil {
		return domain.NodeDaemonMaintenanceConfirmation{}, false
	}
	h.mu.RLock()
	confirmation, ok := h.maintenance[strings.TrimSpace(nodeID)][strings.TrimSpace(commandID)]
	h.mu.RUnlock()
	return confirmation, ok
}

func evaluateMaintenanceConfirmation(
	confirmation domain.NodeDaemonMaintenanceConfirmation,
	heartbeat domain.NodeDaemonHeartbeat,
) domain.NodeDaemonMaintenanceConfirmation {
	if heartbeat.BootID == "" || heartbeat.BootID == confirmation.RequestedBootID {
		return confirmation
	}
	confirmation.ObservedBootID = heartbeat.BootID
	confirmation.ObservedVersion = heartbeat.PaxdVersion
	confirmation.UpdatedAt = heartbeat.ObservedAt
	if heartbeat.DaemonPhase != "running" {
		confirmation.Status = "awaiting_running"
		return confirmation
	}
	if confirmation.ExpectedVersion != "" && heartbeat.PaxdVersion != confirmation.ExpectedVersion {
		confirmation.Status = "version_mismatch"
		return confirmation
	}
	confirmation.Status = "confirmed"
	return confirmation
}
