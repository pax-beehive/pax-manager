package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

func (s *Server) handleNodeControlTunnel(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	logging.Info(
		ctx,
		"node control tunnel request",
		slog.String("query_node_id", websocketNodeID(r)),
	)

	node, err := s.authenticateNodeControlTunnel(r)
	if err != nil {
		status, message := endpointErrorStatus(err)
		logging.Warn(
			ctx,
			"node control tunnel rejected",
			slog.String("query_node_id", websocketNodeID(r)),
			slog.Int("status", status),
			slog.String("reason", message),
			logging.Err(err),
		)
		writeHTTPEndpointError(w, err)
		return
	}

	ctx = logging.With(
		ctx,
		slog.String("node_id", node.NodeID),
		slog.String("owner_user_id", node.OwnerUserID),
	)
	r = r.WithContext(ctx)

	ws, err := upgrader.Upgrade(w, r, websocketResponseHeader(ctx))
	if err != nil {
		logging.Error(ctx, "node control tunnel upgrade failed", logging.Err(err))
		return
	}
	defer func() {
		_ = ws.Close()
		logging.Info(ctx, "node control tunnel disconnected")
	}()

	logging.Info(ctx, "node control tunnel connected")
	for {
		messageType, payload, err := ws.ReadMessage()
		if err != nil {
			if !isWebSocketCloseError(err) {
				logging.Warn(ctx, "node control tunnel read ended", logging.Err(err))
			}
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		logging.Info(
			ctx,
			"node control tunnel received frame",
			slog.Int("bytes", len(payload)),
		)
		if err := s.handleNodeControlTunnelFrame(ctx, node, payload); err != nil {
			logging.Warn(
				ctx,
				"node control tunnel ignored frame",
				slog.Int("bytes", len(payload)),
				logging.Err(err),
			)
		}
	}
}

type nodeControlFrame struct {
	Kind     string            `json:"kind"`
	Version  int               `json:"version"`
	ReportID string            `json:"report_id"`
	Report   nodeControlReport `json:"report"`
}

type nodeControlReport struct {
	Type            string                      `json:"type"`
	RemoteID        string                      `json:"remote_id"`
	NodeID          string                      `json:"node_id"`
	SentAt          time.Time                   `json:"sent_at"`
	Heartbeat       json.RawMessage             `json:"heartbeat,omitempty"`
	RuntimeSnapshot *nodeControlRuntimeSnapshot `json:"runtime_snapshot,omitempty"`
}

type nodeControlRuntimeSnapshot struct {
	SnapshotID string                    `json:"snapshot_id"`
	Host       json.RawMessage           `json:"host,omitempty"`
	Agents     []nodeControlAgentRuntime `json:"agents,omitempty"`
}

type nodeControlAgentRuntime struct {
	ConnectionID string `json:"connection_id"`
	CloudAgentID string `json:"cloud_agent_id"`
	RemoteID     string `json:"remote_id"`
	NodeID       string `json:"node_id"`
	Name         string `json:"name"`
	AgentType    string `json:"agent_type"`
	DesiredState string `json:"desired_state"`
	RuntimePhase string `json:"runtime_phase"`
	StatusReason string `json:"status_reason"`
	LastError    string `json:"last_error"`
	UpdatedAt    string `json:"updated_at"`
}

func (s *Server) handleNodeControlTunnelFrame(ctx context.Context, node Node, payload []byte) error {
	var frame nodeControlFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return fmt.Errorf("decode node control frame: %w", err)
	}
	if frame.Kind != "report" {
		return fmt.Errorf("unsupported node control frame kind %q", frame.Kind)
	}
	if frame.Version != 1 {
		return fmt.Errorf("unsupported node control report version %d", frame.Version)
	}
	if frame.Report.NodeID != "" && frame.Report.NodeID != node.NodeID {
		return errors.New("report node_id does not match authenticated node")
	}

	switch frame.Report.Type {
	case "heartbeat":
		return s.store.UpsertNodeStatus(ctx, node, domain.NodeStatusReport{
			NodeID:    node.NodeID,
			Timestamp: s.clock().UTC(),
		})
	case "runtime.snapshot":
		return s.upsertRuntimeSnapshotReport(ctx, node, frame.Report)
	default:
		return fmt.Errorf("unsupported node control report type %q", frame.Report.Type)
	}
}

func (s *Server) upsertRuntimeSnapshotReport(
	ctx context.Context,
	node Node,
	report nodeControlReport,
) error {
	if report.RuntimeSnapshot == nil {
		return errors.New("runtime.snapshot report missing runtime_snapshot")
	}
	status := domain.NodeStatusReport{
		NodeID:    node.NodeID,
		Timestamp: s.clock().UTC(),
		System:    cloneRawJSON(report.RuntimeSnapshot.Host),
		Metadata:  runtimeSnapshotNodeMetadata(report.RemoteID, report.RuntimeSnapshot),
	}
	for _, agent := range report.RuntimeSnapshot.Agents {
		if agent.CloudAgentID == "" {
			continue
		}
		status.Agents = append(status.Agents, domain.AgentStatusInput{
			AgentID:   agent.CloudAgentID,
			Name:      agent.Name,
			AgentType: agent.AgentType,
			Status:    runtimePhaseAgentStatus(agent.RuntimePhase),
			Online:    runtimePhaseAgentOnline(agent.RuntimePhase),
			Metadata:  runtimeSnapshotAgentMetadata(agent),
		})
	}
	return s.store.UpsertNodeStatus(ctx, node, status)
}

func runtimePhaseAgentStatus(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "running", "starting", "ready":
		return "online"
	case "failed", "error", "errored":
		return "error"
	case "stopped", "stopping", "disabled", "offline", "exited", "exit":
		return "offline"
	case "":
		return "offline"
	default:
		return phase
	}
}

func runtimePhaseAgentOnline(phase string) bool {
	return runtimePhaseAgentStatus(phase) == "online"
}

func runtimeSnapshotNodeMetadata(
	remoteID string,
	snapshot *nodeControlRuntimeSnapshot,
) json.RawMessage {
	if snapshot == nil || len(snapshot.Host) == 0 {
		return nil
	}
	metadata := map[string]any{
		"runtime_snapshot": map[string]any{
			"remote_id":    remoteID,
			"snapshot_id":  snapshot.SnapshotID,
			"host_metrics": json.RawMessage(cloneRawJSON(snapshot.Host)),
		},
	}
	return mustMarshalRawJSON(metadata)
}

func runtimeSnapshotAgentMetadata(agent nodeControlAgentRuntime) json.RawMessage {
	metadata := map[string]any{
		"runtime": map[string]string{
			"connection_id": agent.ConnectionID,
			"remote_id":     agent.RemoteID,
			"node_id":       agent.NodeID,
			"desired_state": agent.DesiredState,
			"runtime_phase": agent.RuntimePhase,
			"status_reason": agent.StatusReason,
			"last_error":    agent.LastError,
			"updated_at":    agent.UpdatedAt,
		},
	}
	return mustMarshalRawJSON(metadata)
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}

func mustMarshalRawJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

func (s *Server) authenticateNodeControlTunnel(r *http.Request) (Node, error) {
	paxKey := websocketPaxKey(r)
	if paxKey == "" {
		return Node{}, apperr.Error{
			Status:  http.StatusUnauthorized,
			Message: "missing pax key",
		}
	}

	node, err := s.store.AuthenticateNode(r.Context(), s.secrets.Hash(paxKey))
	if err != nil {
		return Node{}, err
	}
	if requestNodeID := websocketNodeID(r); requestNodeID != "" && requestNodeID != node.NodeID {
		return Node{}, apperr.Error{
			Status:  http.StatusForbidden,
			Message: "node_id does not match pax key",
		}
	}
	return node, nil
}

func websocketNodeID(r *http.Request) string {
	if nodeID := r.URL.Query().Get("node_id"); nodeID != "" {
		return nodeID
	}
	return r.URL.Query().Get("nodeId")
}
