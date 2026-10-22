package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	// nodeDaemonCommandSyncInterval bounds how often heartbeats trigger a
	// command.get sweep over a node's in-progress commands.
	nodeDaemonCommandSyncInterval = 10 * time.Second
	nodeDaemonCommandSyncBatch    = 20
	// A record stuck in "dispatching" longer than this, which paxd does not
	// know, was never delivered.
	nodeDaemonCommandDispatchTimeout = 2 * time.Minute
)

// nodeDaemonCommandIntent is the manager-owned part of a recorded command.
type nodeDaemonCommandIntent struct {
	CommandID     string
	Type          string
	Target        map[string]any
	TargetVersion string
}

type nodeDaemonCommandQueryResult struct {
	Command *nodeDaemonCommandView  `json:"command,omitempty"`
	Error   *nodeDaemonControlError `json:"error,omitempty"`
}

type nodeDaemonCommandView struct {
	CommandID    string          `json:"command_id"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
}

// dispatchRecordedNodeDaemonCommand records the command before sending it, so
// the intent survives browser reloads, node disconnects and manager restarts.
func (s *Service) dispatchRecordedNodeDaemonCommand(
	ctx context.Context,
	principal domain.UserPrincipal,
	node domain.Node,
	intent nodeDaemonCommandIntent,
	command map[string]any,
	data map[string]any,
) (int, any, error) {
	target, err := json.Marshal(intent.Target)
	if err != nil {
		return 0, nil, err
	}
	record, created, err := s.store.CreateNodeDaemonCommand(ctx, domain.NodeDaemonCommand{
		CommandID:         intent.CommandID,
		NodeID:            node.NodeID,
		OwnerUserID:       node.OwnerUserID,
		RequestedByUserID: principal.User.UserID,
		Type:              intent.Type,
		Target:            target,
		TargetVersion:     intent.TargetVersion,
		Status:            domain.NodeDaemonCommandStatusDispatching,
		State:             domain.NodeDaemonCommandStateInProgress,
		CreatedAt:         s.clock().UTC(),
	})
	if err != nil {
		return 0, nil, err
	}
	if !created && record.Type != intent.Type {
		return 0, nil, apperr.Error{
			Status:  http.StatusConflict,
			Message: "command_id is already used by a different command on this node",
		}
	}
	status, result, err := s.dispatchNodeDaemonCommand(
		ctx,
		node.NodeID,
		intent.CommandID,
		command,
		data,
	)
	if err != nil {
		return status, result, err
	}
	record = applyNodeDaemonCommandDispatch(record, data, s.clock().UTC())
	if err := s.store.SaveNodeDaemonCommand(ctx, record); err != nil {
		// The heartbeat sweep repairs the record from paxd later.
		logging.Warn(
			ctx,
			"record node daemon command dispatch failed",
			slog.String("node_id", node.NodeID),
			slog.String("command_id", intent.CommandID),
			logging.Err(err),
		)
	}
	data["daemon_command"] = record
	return status, data, nil
}

func applyNodeDaemonCommandDispatch(
	record domain.NodeDaemonCommand,
	data map[string]any,
	now time.Time,
) domain.NodeDaemonCommand {
	if dispatchErr, _ := data["dispatch_error"].(string); dispatchErr != "" {
		// A failed resend must not hide what paxd already reported.
		if record.Status == domain.NodeDaemonCommandStatusDispatching ||
			record.Status == domain.NodeDaemonCommandStatusDispatchFailed {
			record.Status = domain.NodeDaemonCommandStatusDispatchFailed
			record.ErrorCode = "dispatch_failed"
			record.ErrorMessage = dispatchErr
		}
		return finishNodeDaemonCommandUpdate(record, now)
	}
	raw, _ := data["command_ack"].(json.RawMessage)
	var ack nodeDaemonCommandAck
	if err := json.Unmarshal(raw, &ack); err == nil && strings.TrimSpace(ack.Status) != "" {
		record.Status = strings.TrimSpace(ack.Status)
		record.ErrorCode, record.ErrorMessage = "", ""
		if ack.Error != nil {
			record.ErrorCode = ack.Error.Code
			record.ErrorMessage = ack.Error.Message
		}
		record.ObservedAt = &now
	}
	if confirmation, ok := data["maintenance_confirmation"].(domain.NodeDaemonMaintenanceConfirmation); ok {
		record.MaintenanceConfirmation = &confirmation
	}
	return finishNodeDaemonCommandUpdate(record, now)
}

func finishNodeDaemonCommandUpdate(
	record domain.NodeDaemonCommand,
	now time.Time,
) domain.NodeDaemonCommand {
	record.State = domain.NodeDaemonCommandState(record)
	if record.State != domain.NodeDaemonCommandStateInProgress && record.CompletedAt == nil {
		record.CompletedAt = &now
	}
	return record
}

func (s *Service) GetNodeDaemonCommand(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	commandID string,
) (int, any, error) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "command_id is required",
		}
	}
	node, err := s.authorizedNode(c, meta, nodeID)
	if err != nil {
		return 0, nil, err
	}
	record, found, err := s.findNodeDaemonCommand(c, node.NodeID, commandID)
	if err != nil {
		return 0, nil, err
	}
	raw, queryErr := s.queryNodeControl(c, node.NodeID, nodeDaemonCommandGetQuery(commandID))
	if queryErr != nil {
		if !found {
			return 0, nil, queryErr
		}
		// paxd is unreachable; serve the last observation instead.
		return http.StatusOK, staleNodeDaemonCommandResult(record), nil
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return http.StatusOK, raw, nil
	}
	var parsed nodeDaemonCommandQueryResult
	_ = json.Unmarshal(raw, &parsed)
	confirmation, hasConfirmation := s.trackedMaintenanceConfirmation(node.NodeID, parsed.Command)
	if hasConfirmation {
		result["maintenance_confirmation"] = confirmation
	}
	if found {
		record = s.observeNodeDaemonCommand(c, record, parsed, confirmation, hasConfirmation)
		result["daemon_command"] = record
	}
	return http.StatusOK, result, nil
}

func (s *Service) ListNodeDaemonCommands(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
	filter domain.NodeDaemonCommandFilter,
) (int, any, error) {
	filter.Type = strings.TrimSpace(filter.Type)
	filter.State = strings.TrimSpace(filter.State)
	switch filter.State {
	case "",
		domain.NodeDaemonCommandStateInProgress,
		domain.NodeDaemonCommandStateSucceeded,
		domain.NodeDaemonCommandStateFailed:
	default:
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "state must be in_progress, succeeded, or failed",
		}
	}
	if filter.Limit < 0 || filter.Limit > 100 {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "limit must be between 1 and 100",
		}
	}
	node, err := s.authorizedNode(c, meta, nodeID)
	if err != nil {
		return 0, nil, err
	}
	commands, err := s.store.ListNodeDaemonCommands(c, node.NodeID, filter)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"commands": commands}, nil
}

// TriggerNodeDaemonCommandSync starts a background command.get sweep over the
// node's in-progress commands, at most once per sync interval. paxd does not
// push command progress, so heartbeats drive reconciliation.
func (s *Service) TriggerNodeDaemonCommandSync(ctx context.Context, nodeID string) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" || s.nodeControl == nil {
		return
	}
	now := s.clock()
	s.commandSyncMu.Lock()
	if s.commandSyncAt == nil {
		s.commandSyncAt = make(map[string]time.Time)
	}
	if last, ok := s.commandSyncAt[nodeID]; ok && now.Sub(last) < nodeDaemonCommandSyncInterval {
		s.commandSyncMu.Unlock()
		return
	}
	s.commandSyncAt[nodeID] = now
	s.commandSyncMu.Unlock()
	s.backgroundRunner(ctx, func(taskCtx context.Context) {
		if err := s.SyncNodeDaemonCommands(taskCtx, nodeID); err != nil {
			logging.Warn(
				taskCtx,
				"node daemon command sync failed",
				slog.String("node_id", nodeID),
				logging.Err(err),
			)
		}
	})
}

// SyncNodeDaemonCommands refreshes in-progress records from paxd.
func (s *Service) SyncNodeDaemonCommands(ctx context.Context, nodeID string) error {
	records, err := s.store.ListNodeDaemonCommands(ctx, nodeID, domain.NodeDaemonCommandFilter{
		State: domain.NodeDaemonCommandStateInProgress,
		Limit: nodeDaemonCommandSyncBatch,
	})
	if err != nil {
		return err
	}
	for _, record := range records {
		raw, err := s.queryNodeControl(ctx, nodeID, nodeDaemonCommandGetQuery(record.CommandID))
		if err != nil {
			return err
		}
		var parsed nodeDaemonCommandQueryResult
		if err := json.Unmarshal(raw, &parsed); err != nil {
			continue
		}
		confirmation, ok := s.trackedMaintenanceConfirmation(nodeID, parsed.Command)
		s.observeNodeDaemonCommand(ctx, record, parsed, confirmation, ok)
	}
	return nil
}

// observeNodeDaemonCommand mirrors a command.get result into the record and
// saves it when anything changed.
func (s *Service) observeNodeDaemonCommand(
	ctx context.Context,
	record domain.NodeDaemonCommand,
	parsed nodeDaemonCommandQueryResult,
	confirmation domain.NodeDaemonMaintenanceConfirmation,
	hasConfirmation bool,
) domain.NodeDaemonCommand {
	now := s.clock().UTC()
	next, changed := applyNodeDaemonCommandObservation(
		record, parsed, confirmation, hasConfirmation, now,
	)
	if !changed {
		return record
	}
	if err := s.store.SaveNodeDaemonCommand(ctx, next); err != nil {
		logging.Warn(
			ctx,
			"save node daemon command observation failed",
			slog.String("node_id", record.NodeID),
			slog.String("command_id", record.CommandID),
			logging.Err(err),
		)
		return record
	}
	return next
}

func applyNodeDaemonCommandObservation(
	record domain.NodeDaemonCommand,
	parsed nodeDaemonCommandQueryResult,
	confirmation domain.NodeDaemonMaintenanceConfirmation,
	hasConfirmation bool,
	now time.Time,
) (domain.NodeDaemonCommand, bool) {
	if parsed.Command == nil {
		if parsed.Error == nil || parsed.Error.Code != "not_found" {
			return record, false
		}
		switch record.Status {
		case domain.NodeDaemonCommandStatusDispatchFailed, domain.NodeDaemonCommandStatusLost:
			return record, false
		case domain.NodeDaemonCommandStatusDispatching:
			if now.Sub(record.CreatedAt) < nodeDaemonCommandDispatchTimeout {
				return record, false
			}
			record.Status = domain.NodeDaemonCommandStatusDispatchFailed
			record.ErrorCode = "dispatch_failed"
			record.ErrorMessage = "paxd never received the command"
		default:
			record.Status = domain.NodeDaemonCommandStatusLost
			record.ErrorCode = "not_found"
			record.ErrorMessage = "paxd no longer has a record of this command"
		}
		record.ObservedAt = &now
		return finishNodeDaemonCommandUpdate(record, now), true
	}
	view := parsed.Command
	record.Status = strings.TrimSpace(view.Status)
	record.ErrorCode = view.ErrorCode
	record.ErrorMessage = view.ErrorMessage
	record.Result = view.Result
	record.Phase = nodeDaemonCommandPhase(view.Result)
	if hasConfirmation {
		record.MaintenanceConfirmation = &confirmation
	}
	record.ObservedAt = &now
	return finishNodeDaemonCommandUpdate(record, now), true
}

// trackedMaintenanceConfirmation returns the heartbeat-based confirmation for
// paxd restart and upgrade commands, re-tracking them from the paxd result
// after a manager restart.
func (s *Service) trackedMaintenanceConfirmation(
	nodeID string,
	view *nodeDaemonCommandView,
) (domain.NodeDaemonMaintenanceConfirmation, bool) {
	tracker, ok := s.nodeControl.(nodeMaintenanceTracker)
	if !ok || view == nil {
		return domain.NodeDaemonMaintenanceConfirmation{}, false
	}
	confirmation, found := tracker.MaintenanceConfirmation(nodeID, view.CommandID)
	if found {
		return confirmation, true
	}
	if view.Type != domain.NodeDaemonCommandPaxdRestart &&
		view.Type != domain.NodeDaemonCommandPaxdUpgrade {
		return domain.NodeDaemonMaintenanceConfirmation{}, false
	}
	var result struct {
		RequestedBootID string `json:"requested_boot_id"`
		TargetVersion   string `json:"target_version"`
	}
	_ = json.Unmarshal(view.Result, &result)
	if result.RequestedBootID == "" {
		return domain.NodeDaemonMaintenanceConfirmation{}, false
	}
	expectedVersion := ""
	if view.Type == domain.NodeDaemonCommandPaxdUpgrade {
		expectedVersion = result.TargetVersion
	}
	tracker.TrackMaintenance(nodeID, view.CommandID, result.RequestedBootID, expectedVersion)
	return tracker.MaintenanceConfirmation(nodeID, view.CommandID)
}

// attachLatestDaemonCommands fills Node.LatestDaemonCommands. Failures only
// drop the extra field; the node list itself stays available.
func (s *Service) attachLatestDaemonCommands(ctx context.Context, nodes []domain.Node) {
	if len(nodes) == 0 {
		return
	}
	nodeIDs := make([]string, len(nodes))
	for i, node := range nodes {
		nodeIDs[i] = node.NodeID
	}
	latest, err := s.store.LatestNodeDaemonCommands(ctx, nodeIDs)
	if err != nil {
		logging.Warn(ctx, "load latest node daemon commands failed", logging.Err(err))
		return
	}
	for i := range nodes {
		nodes[i].LatestDaemonCommands = latest[nodes[i].NodeID]
	}
}

func (s *Service) authorizedNode(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID string,
) (domain.Node, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return domain.Node{}, err
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return domain.Node{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id is required",
		}
	}
	return s.store.GetNode(c, principal, nodeID)
}

func (s *Service) findNodeDaemonCommand(
	ctx context.Context,
	nodeID string,
	commandID string,
) (domain.NodeDaemonCommand, bool, error) {
	record, err := s.store.GetNodeDaemonCommand(ctx, nodeID, commandID)
	if err == nil {
		return record, true, nil
	}
	if errors.Is(err, domain.ErrNotFound) {
		return domain.NodeDaemonCommand{}, false, nil
	}
	return domain.NodeDaemonCommand{}, false, err
}

// staleNodeDaemonCommandResult mimics the command.get shape from the stored
// record so existing clients keep working while the node is offline.
func staleNodeDaemonCommandResult(record domain.NodeDaemonCommand) map[string]any {
	command := map[string]any{
		"command_id": record.CommandID,
		"type":       record.Type,
		"status":     record.Status,
		"updated_at": record.UpdatedAt,
	}
	if record.ErrorCode != "" {
		command["error_code"] = record.ErrorCode
	}
	if record.ErrorMessage != "" {
		command["error_message"] = record.ErrorMessage
	}
	if len(record.Result) > 0 {
		command["result"] = record.Result
	}
	result := map[string]any{
		"type":           "command.get",
		"command":        command,
		"daemon_command": record,
		"stale":          true,
	}
	if record.MaintenanceConfirmation != nil {
		result["maintenance_confirmation"] = *record.MaintenanceConfirmation
	}
	return result
}

func nodeDaemonCommandGetQuery(commandID string) map[string]any {
	return map[string]any{
		"type": "command.get",
		"get_command": map[string]any{
			"command_id": commandID,
		},
	}
}

func nodeDaemonCommandPhase(result json.RawMessage) string {
	var decoded struct {
		Phase string `json:"phase"`
	}
	if len(result) == 0 || json.Unmarshal(result, &decoded) != nil {
		return ""
	}
	return strings.TrimSpace(decoded.Phase)
}
