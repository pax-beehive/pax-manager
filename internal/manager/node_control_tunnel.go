package manager

import (
	"context"
	"crypto/sha256"
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

var sessionRuntimeIdleInterruptGrace = 5 * time.Second

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
	runtimeStore, ok := s.store.(domain.SessionRuntimeSnapshotStore)
	if !ok {
		_ = ws.Close()
		logging.Error(ctx, "node control tunnel runtime store is unavailable")
		return
	}
	connectionFence, err := s.secrets.New("fence")
	if err != nil {
		_ = ws.Close()
		logging.Error(ctx, "node control tunnel fence allocation failed", logging.Err(err))
		return
	}
	if err := runtimeStore.ActivateNodeRuntimeFence(ctx, node, connectionFence); err != nil {
		_ = ws.Close()
		logging.Error(ctx, "node control tunnel fence activation failed", logging.Err(err))
		return
	}
	conn := newNodeControlConnection(node.NodeID, ws)
	s.nodeControls.Add(node.NodeID, conn)
	defer func() {
		conn.Fail(ErrNodeControlDisconnected)
		s.nodeControls.Remove(node.NodeID, conn)
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
		handled, err := conn.HandleIncoming(payload)
		if handled {
			if err != nil {
				logging.Warn(
					ctx,
					"node control tunnel ignored response",
					slog.Int("bytes", len(payload)),
					logging.Err(err),
				)
			}
			continue
		}
		if err != nil {
			logging.Warn(
				ctx,
				"node control tunnel ignored frame",
				slog.Int("bytes", len(payload)),
				logging.Err(err),
			)
			continue
		}
		if err := s.handleNodeControlTunnelFrameWithFence(ctx, node, connectionFence, payload); err != nil {
			logging.Warn(
				ctx,
				"node control tunnel ignored frame",
				slog.Int("bytes", len(payload)),
				logging.Err(err),
			)
			continue
		}
		if err := conn.ObserveReport(payload); err != nil {
			logging.Warn(ctx, "node control tunnel ignored report identity", logging.Err(err))
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
	Type                   string                             `json:"type"`
	RemoteID               string                             `json:"remote_id"`
	NodeID                 string                             `json:"node_id"`
	SentAt                 time.Time                          `json:"sent_at"`
	Heartbeat              json.RawMessage                    `json:"heartbeat,omitempty"`
	RuntimeSnapshot        *nodeControlRuntimeSnapshot        `json:"runtime_snapshot,omitempty"`
	AttachmentLocalState   *nodeControlAttachmentLocalState   `json:"attachment_local_state,omitempty"`
	SessionRuntimeSnapshot *nodeControlSessionRuntimeSnapshot `json:"session_runtime_snapshot,omitempty"`
}

type nodeControlSessionRuntimeSnapshot struct {
	AgentID       string                                `json:"agent_id"`
	ConnectionID  string                                `json:"connection_id"`
	Sequence      int64                                 `json:"sequence"`
	GeneratedAt   time.Time                             `json:"generated_at"`
	SchemaVersion int                                   `json:"schema_version"`
	ActiveTurns   []nodeControlSessionRuntimeActiveTurn `json:"active_turns"`
}

type nodeControlSessionRuntimeActiveTurn struct {
	TurnID            string          `json:"turn_id,omitempty"`
	NativeSessionID   string          `json:"native_session_id"`
	TurnInstanceID    string          `json:"turn_instance_id"`
	PromptRequestID   json.RawMessage `json:"prompt_request_id"`
	RuntimeStatus     string          `json:"runtime_status"`
	PendingApprovalID json.RawMessage `json:"pending_approval_id,omitempty"`
}

type nodeControlRuntimeSnapshot struct {
	SnapshotID string                    `json:"snapshot_id"`
	Host       json.RawMessage           `json:"host,omitempty"`
	Agents     []nodeControlAgentRuntime `json:"agents,omitempty"`
}

type nodeControlHostMetrics struct {
	MachineName string `json:"machine_name"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
}

type nodeControlAgentRuntime struct {
	ConnectionID            string          `json:"connection_id"`
	CloudAgentID            string          `json:"cloud_agent_id"`
	RemoteID                string          `json:"remote_id"`
	NodeID                  string          `json:"node_id"`
	Name                    string          `json:"name"`
	AgentType               string          `json:"agent_type"`
	DesiredState            string          `json:"desired_state"`
	RuntimePhase            string          `json:"runtime_phase"`
	ObservedGeneration      int64           `json:"observed_generation"`
	ObservedRestartNonce    int64           `json:"observed_restart_nonce"`
	StatusUpdatedAt         string          `json:"status_updated_at"`
	FailureClass            string          `json:"failure_class"`
	LastErrorCode           string          `json:"last_error_code"`
	LastErrorMessage        string          `json:"last_error_message"`
	ACPPoolCapabilityReport json.RawMessage `json:"acp_pool_capability_report,omitempty"`
}

type nodeControlACPPoolCapabilityReport struct {
	SchemaVersion      int    `json:"schema_version"`
	ConnectionID       string `json:"connection_id"`
	ReportGeneration   int64  `json:"report_generation"`
	ProtocolVersion    int    `json:"protocol_version"`
	PaxdVersion        string `json:"paxd_version"`
	CommandFingerprint string `json:"command_fingerprint"`
	ClientProfileHash  string `json:"client_profile_hash"`
	WorkerResultHash   string `json:"worker_result_hash"`
	PoolConsistency    string `json:"pool_consistency"`
	Implementation     struct {
		ACPAgent struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"acp_agent"`
		Runtime *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Build   string `json:"build"`
			Channel string `json:"channel"`
		} `json:"runtime,omitempty"`
		IdentityFingerprint string `json:"identity_fingerprint"`
	} `json:"implementation"`
	// Accept the early v2 draft location while paxd installations roll over.
	IdentityFingerprint string `json:"identity_fingerprint"`
}

func (s *Server) handleNodeControlTunnelFrame(
	ctx context.Context,
	node Node,
	payload []byte,
) error {
	return s.handleNodeControlTunnelFrameWithFence(ctx, node, "", payload)
}

func (s *Server) handleNodeControlTunnelFrameWithFence(
	ctx context.Context,
	node Node,
	connectionFence string,
	payload []byte,
) error {
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
		observedAt := s.clock().UTC()
		heartbeat := domain.NodeDaemonHeartbeat{ObservedAt: observedAt}
		if len(frame.Report.Heartbeat) > 0 {
			if err := json.Unmarshal(frame.Report.Heartbeat, &heartbeat); err != nil {
				return fmt.Errorf("decode node control heartbeat: %w", err)
			}
			heartbeat.ObservedAt = observedAt
		}
		s.nodeControls.ObserveHeartbeat(node.NodeID, heartbeat)
		return s.store.UpsertNodeStatus(ctx, node, domain.NodeStatusReport{
			NodeID:      node.NodeID,
			PaxdVersion: heartbeat.PaxdVersion,
			Timestamp:   observedAt,
		})
	case "runtime.snapshot":
		return s.upsertRuntimeSnapshotReport(ctx, node, connectionFence, frame.Report)
	case "session_runtime.snapshot":
		return s.replaceSessionRuntimeSnapshot(ctx, node, connectionFence, frame.Report)
	case "attachment.local_state":
		if frame.Report.AttachmentLocalState == nil {
			return errors.New("attachment.local_state report missing attachment_local_state")
		}
		if strings.TrimSpace(frame.Report.AttachmentLocalState.AttachmentID) == "" {
			return errors.New("attachment.local_state report missing attachment_id")
		}
		s.nodeControls.ObserveAttachmentState(node.NodeID, *frame.Report.AttachmentLocalState)
		return nil
	default:
		return fmt.Errorf("unsupported node control report type %q", frame.Report.Type)
	}
}

func (s *Server) replaceSessionRuntimeSnapshot(
	ctx context.Context,
	node Node,
	connectionFence string,
	report nodeControlReport,
) error {
	if report.SessionRuntimeSnapshot == nil {
		return errors.New("session_runtime.snapshot report missing session_runtime_snapshot")
	}
	if report.SessionRuntimeSnapshot.SchemaVersion != 1 {
		return fmt.Errorf(
			"unsupported session runtime schema version %d",
			report.SessionRuntimeSnapshot.SchemaVersion,
		)
	}
	turns := make([]domain.ActiveTurnSnapshot, 0, len(report.SessionRuntimeSnapshot.ActiveTurns))
	for _, turn := range report.SessionRuntimeSnapshot.ActiveTurns {
		if turn.TurnID != "" && turn.TurnInstanceID != "" && turn.TurnID != turn.TurnInstanceID {
			return fmt.Errorf("snapshot turn id aliases disagree")
		}
		turns = append(turns, domain.ActiveTurnSnapshot{
			NativeSessionID:   turn.NativeSessionID,
			TurnInstanceID:    firstNonEmpty(turn.TurnID, turn.TurnInstanceID),
			PromptRequestID:   cloneRawJSON(turn.PromptRequestID),
			RuntimeStatus:     turn.RuntimeStatus,
			PendingApprovalID: domain.RuntimePromptRequestID(turn.PendingApprovalID),
		})
	}
	snapshot := domain.AgentRuntimeSnapshot{
		AgentID:         report.SessionRuntimeSnapshot.AgentID,
		ConnectionFence: connectionFence,
		Sequence:        report.SessionRuntimeSnapshot.Sequence,
		GeneratedAt:     report.SessionRuntimeSnapshot.GeneratedAt,
		ActiveTurns:     turns,
	}
	runtimeStore, ok := s.store.(domain.SessionRuntimeSnapshotStore)
	if !ok {
		return errors.New("session runtime snapshot store is unavailable")
	}
	result, err := runtimeStore.ReplaceAgentActiveTurns(ctx, node, snapshot)
	if err != nil {
		return err
	}
	if result.Status == domain.RuntimeSnapshotFenced {
		return errors.New("session runtime snapshot came from a fenced connection")
	}
	if result.Status == domain.RuntimeSnapshotApplied && s.acpTunnels != nil {
		for _, change := range s.snapshotReceiverChanges(snapshot, result.Changes) {
			if change.RuntimeStatus != domain.RuntimeStatusIdle {
				s.acpTunnels.cancelIdleSessionInterrupt(snapshot.AgentID, change.SessionID)
				continue
			}
			deferred := s.acpTunnels.deferIdleSessionInterrupt(
				snapshot.AgentID,
				change.SessionID,
				apperr.Error{
					Status:  http.StatusConflict,
					Message: "agent runtime stopped before the active prompt completed; send another prompt to continue the session",
				},
				sessionRuntimeIdleInterruptGrace,
			)
			if deferred > 0 {
				logging.Info(
					ctx,
					"session runtime snapshot deferred stale receiver interruption",
					slog.String("agent_id", snapshot.AgentID),
					slog.String("session_id", change.SessionID),
					slog.Duration("grace", sessionRuntimeIdleInterruptGrace),
				)
			}
		}
	}
	return nil
}

// Reconcile in-flight readers even when no earlier running snapshot was received.
func (s *Server) snapshotReceiverChanges(
	snapshot domain.AgentRuntimeSnapshot,
	changes []domain.RuntimeStatusChange,
) []domain.RuntimeStatusChange {
	activeRequests := make(map[string]bool, len(snapshot.ActiveTurns))
	for _, turn := range snapshot.ActiveTurns {
		activeRequests[domain.RuntimePromptRequestID(turn.PromptRequestID)] = true
	}
	for _, local := range s.acpRuntime.ActiveCorrelations(snapshot.AgentID) {
		status := domain.RuntimeStatusIdle
		if activeRequests[local.ActivePromptRequestID] {
			status = domain.RuntimeStatusRunning
		}
		changes = append(
			changes,
			domain.RuntimeStatusChange{SessionID: local.SessionID, RuntimeStatus: status},
		)
	}
	return changes
}

func (s *Server) upsertRuntimeSnapshotReport(
	ctx context.Context,
	node Node,
	connectionFence string,
	report nodeControlReport,
) error {
	if report.RuntimeSnapshot == nil {
		return errors.New("runtime.snapshot report missing runtime_snapshot")
	}
	var host nodeControlHostMetrics
	if len(report.RuntimeSnapshot.Host) > 0 {
		_ = json.Unmarshal(report.RuntimeSnapshot.Host, &host)
	}
	status := domain.NodeStatusReport{
		NodeID:       node.NodeID,
		RuntimeFence: connectionFence,
		MachineType:  host.MachineName,
		OS:           host.OS,
		Arch:         host.Arch,
		Timestamp:    s.clock().UTC(),
		System:       cloneRawJSON(report.RuntimeSnapshot.Host),
		Metadata:     runtimeSnapshotNodeMetadata(report.RemoteID, report.RuntimeSnapshot),
	}
	observedAt := s.clock().UTC()
	for _, agent := range report.RuntimeSnapshot.Agents {
		if agent.CloudAgentID == "" {
			continue
		}
		identity, err := parseAgentRuntimeIdentity(
			agent.CloudAgentID,
			agent.AgentType,
			agent.ConnectionID,
			agent.ACPPoolCapabilityReport,
			observedAt,
		)
		if err != nil {
			logging.Warn(
				ctx,
				"runtime snapshot ignored invalid ACP identity",
				slog.String("agent_id", agent.CloudAgentID),
				logging.Err(err),
			)
			identity = nil
		}
		if identity == nil {
			identity = unknownAgentRuntimeIdentity(
				agent.CloudAgentID,
				agent.ConnectionID,
				agent.ObservedGeneration,
				connectionFence,
				observedAt,
			)
		}
		identity.ReportEpoch = connectionFence
		status.Agents = append(status.Agents, domain.AgentStatusInput{
			AgentID:         agent.CloudAgentID,
			Name:            agent.Name,
			AgentType:       agent.AgentType,
			Status:          runtimePhaseAgentStatus(agent.RuntimePhase),
			Online:          runtimePhaseAgentOnline(agent.RuntimePhase),
			Metadata:        runtimeSnapshotAgentMetadata(agent),
			RuntimeIdentity: identity,
		})
	}
	return s.store.UpsertNodeStatus(ctx, node, status)
}

func unknownAgentRuntimeIdentity(
	agentID string,
	connectionID string,
	reportGeneration int64,
	reportEpoch string,
	observedAt time.Time,
) *domain.AgentRuntimeIdentity {
	value := strings.Join([]string{
		"unknown",
		strings.TrimSpace(agentID),
		strings.TrimSpace(connectionID),
		strings.TrimSpace(reportEpoch),
	}, "\n")
	return &domain.AgentRuntimeIdentity{
		AgentID:             agentID,
		ReportEpoch:         reportEpoch,
		ConnectionID:        connectionID,
		ReportGeneration:    reportGeneration,
		IdentityFingerprint: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value))),
		PoolConsistency:     "unknown",
		ObservedAt:          observedAt.UTC(),
	}
}

func parseAgentRuntimeIdentity(
	agentID string,
	agentType string,
	runtimeConnectionID string,
	raw json.RawMessage,
	observedAt time.Time,
) (*domain.AgentRuntimeIdentity, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var report nodeControlACPPoolCapabilityReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if report.SchemaVersion != 1 && report.SchemaVersion != 2 {
		return nil, fmt.Errorf("unsupported schema_version %d", report.SchemaVersion)
	}
	if report.ConnectionID != "" && runtimeConnectionID != "" &&
		report.ConnectionID != runtimeConnectionID {
		return nil, errors.New("connection_id does not match runtime agent")
	}
	identity := &domain.AgentRuntimeIdentity{
		AgentID:            agentID,
		SchemaVersion:      report.SchemaVersion,
		ConnectionID:       firstNonEmpty(report.ConnectionID, runtimeConnectionID),
		ReportGeneration:   report.ReportGeneration,
		ProtocolVersion:    report.ProtocolVersion,
		CommandFingerprint: strings.TrimSpace(report.CommandFingerprint),
		ClientProfileHash:  strings.TrimSpace(report.ClientProfileHash),
		WorkerResultHash:   strings.TrimSpace(report.WorkerResultHash),
		PoolConsistency:    strings.ToLower(strings.TrimSpace(report.PoolConsistency)),
		ObservedAt:         observedAt.UTC(),
	}
	if report.SchemaVersion == 1 {
		identity.IdentityFingerprint = legacyRuntimeIdentityFingerprint(agentType, report)
		return identity, nil
	}
	identity.ACPAgentName = strings.TrimSpace(report.Implementation.ACPAgent.Name)
	identity.ACPAgentTitle = strings.TrimSpace(report.Implementation.ACPAgent.Title)
	identity.ACPAgentVersion = strings.TrimSpace(report.Implementation.ACPAgent.Version)
	if report.Implementation.Runtime != nil {
		identity.RuntimeName = strings.TrimSpace(report.Implementation.Runtime.Name)
		identity.RuntimeVersion = strings.TrimSpace(report.Implementation.Runtime.Version)
		identity.RuntimeBuild = strings.TrimSpace(report.Implementation.Runtime.Build)
		identity.RuntimeChannel = strings.TrimSpace(report.Implementation.Runtime.Channel)
	}
	identity.IdentityFingerprint = strings.TrimSpace(firstNonEmpty(
		report.Implementation.IdentityFingerprint,
		report.IdentityFingerprint,
	))
	if identity.IdentityFingerprint == "" {
		identity.IdentityFingerprint = fallbackRuntimeIdentityFingerprint(agentType, report)
	} else {
		identity.IdentityFingerprint = effectiveObservationFingerprint(
			identity.IdentityFingerprint,
			report,
		)
	}
	return identity, nil
}

func effectiveObservationFingerprint(
	implementationFingerprint string,
	report nodeControlACPPoolCapabilityReport,
) string {
	value := strings.Join([]string{
		strings.TrimSpace(implementationFingerprint),
		strings.TrimSpace(report.ClientProfileHash),
		strings.TrimSpace(report.CommandFingerprint),
		strings.TrimSpace(report.WorkerResultHash),
	}, "\n")
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
}

func legacyRuntimeIdentityFingerprint(
	agentType string,
	report nodeControlACPPoolCapabilityReport,
) string {
	return fallbackRuntimeIdentityFingerprint(agentType, report)
}

func fallbackRuntimeIdentityFingerprint(
	agentType string,
	report nodeControlACPPoolCapabilityReport,
) string {
	runtimeName := ""
	runtimeVersion := ""
	if report.Implementation.Runtime != nil {
		runtimeName = report.Implementation.Runtime.Name
		runtimeVersion = report.Implementation.Runtime.Version
	}
	value := strings.Join([]string{
		fmt.Sprintf("v%d", report.SchemaVersion),
		strings.ToLower(strings.TrimSpace(agentType)),
		strings.TrimSpace(report.PaxdVersion),
		strings.TrimSpace(report.CommandFingerprint),
		strings.TrimSpace(report.ClientProfileHash),
		strings.TrimSpace(report.WorkerResultHash),
		fmt.Sprint(report.ProtocolVersion),
		strings.TrimSpace(report.Implementation.ACPAgent.Name),
		strings.TrimSpace(report.Implementation.ACPAgent.Version),
		strings.TrimSpace(runtimeName),
		strings.TrimSpace(runtimeVersion),
	}, "\n")
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
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
		"runtime": map[string]any{
			"connection_id":          agent.ConnectionID,
			"remote_id":              agent.RemoteID,
			"node_id":                agent.NodeID,
			"desired_state":          agent.DesiredState,
			"runtime_phase":          agent.RuntimePhase,
			"observed_generation":    agent.ObservedGeneration,
			"observed_restart_nonce": agent.ObservedRestartNonce,
			"status_updated_at":      agent.StatusUpdatedAt,
			"failure_class":          agent.FailureClass,
			"last_error_code":        agent.LastErrorCode,
			"last_error_message":     agent.LastErrorMessage,
		},
	}
	if len(agent.ACPPoolCapabilityReport) > 0 && string(agent.ACPPoolCapabilityReport) != "null" {
		metadata["acp_pool_capability_report"] = json.RawMessage(
			cloneRawJSON(agent.ACPPoolCapabilityReport),
		)
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
