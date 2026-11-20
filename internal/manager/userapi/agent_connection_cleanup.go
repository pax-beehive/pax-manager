package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	// deletedAgentConnectionRetryInterval bounds how often paxd is asked to
	// remove the same connection while it keeps reporting it.
	deletedAgentConnectionRetryInterval  = time.Minute
	deletedAgentConnectionCommandTimeout = 15 * time.Second
)

// RemoveDeletedAgentConnections asks paxd, in the background, to remove
// connections that are still bound to deleted agents. Deleting an agent only
// tombstones the cloud record; the connection, its harness process and its
// tunnel redials live on the node until paxd drops them.
//
// Delivery is best effort. A node that is offline now keeps reporting the
// connection in its runtime snapshots after it reconnects, and each snapshot
// calls this again.
func (s *Service) RemoveDeletedAgentConnections(
	ctx context.Context,
	nodeID string,
	connections []domain.NodeAgentConnectionRef,
) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" || s.nodeControl == nil {
		return
	}
	due := s.claimDeletedAgentConnections(nodeID, connections)
	if len(due) == 0 {
		return
	}
	s.backgroundRunner(ctx, func(taskCtx context.Context) {
		for _, connection := range due {
			s.removeDeletedAgentConnection(taskCtx, nodeID, connection)
		}
	})
}

// removeDeletedAgentRuntimeConnection removes the connection that paxd last
// reported for a just deleted agent.
func (s *Service) removeDeletedAgentRuntimeConnection(ctx context.Context, agent domain.Agent) {
	connectionID := agentRuntimeConnectionID(agent)
	if agent.NodeID == "" || connectionID == "" {
		return
	}
	s.RemoveDeletedAgentConnections(ctx, agent.NodeID, []domain.NodeAgentConnectionRef{{
		AgentID:      agent.AgentID,
		ConnectionID: connectionID,
	}})
}

// agentRuntimeConnectionID reads the paxd connection id that runtime snapshots
// record in the agent metadata.
func agentRuntimeConnectionID(agent domain.Agent) string {
	var metadata struct {
		Runtime struct {
			ConnectionID string `json:"connection_id"`
		} `json:"runtime"`
	}
	if len(agent.Metadata) == 0 || json.Unmarshal(agent.Metadata, &metadata) != nil {
		return ""
	}
	return strings.TrimSpace(metadata.Runtime.ConnectionID)
}

// claimDeletedAgentConnections returns the connections that are due for a
// removal attempt and marks them as attempted.
func (s *Service) claimDeletedAgentConnections(
	nodeID string,
	connections []domain.NodeAgentConnectionRef,
) []domain.NodeAgentConnectionRef {
	now := s.clock()
	s.connectionCleanupMu.Lock()
	defer s.connectionCleanupMu.Unlock()
	if s.connectionCleanupAt == nil {
		s.connectionCleanupAt = make(map[string]time.Time)
	}
	for key, attemptedAt := range s.connectionCleanupAt {
		if now.Sub(attemptedAt) >= deletedAgentConnectionRetryInterval {
			delete(s.connectionCleanupAt, key)
		}
	}
	due := make([]domain.NodeAgentConnectionRef, 0, len(connections))
	for _, connection := range connections {
		connection.AgentID = strings.TrimSpace(connection.AgentID)
		connection.ConnectionID = strings.TrimSpace(connection.ConnectionID)
		if connection.AgentID == "" || connection.ConnectionID == "" {
			continue
		}
		key := deletedAgentConnectionKey(nodeID, connection)
		if _, attempted := s.connectionCleanupAt[key]; attempted {
			continue
		}
		s.connectionCleanupAt[key] = now
		due = append(due, connection)
	}
	return due
}

func (s *Service) releaseDeletedAgentConnection(
	nodeID string,
	connection domain.NodeAgentConnectionRef,
) {
	s.connectionCleanupMu.Lock()
	delete(s.connectionCleanupAt, deletedAgentConnectionKey(nodeID, connection))
	s.connectionCleanupMu.Unlock()
}

func deletedAgentConnectionKey(nodeID string, connection domain.NodeAgentConnectionRef) string {
	return nodeID + "\n" + connection.ConnectionID
}

func (s *Service) removeDeletedAgentConnection(
	ctx context.Context,
	nodeID string,
	connection domain.NodeAgentConnectionRef,
) {
	ctx = logging.With(
		ctx,
		slog.String("node_id", nodeID),
		slog.String("agent_id", connection.AgentID),
		slog.String("connection_id", connection.ConnectionID),
	)
	commandCtx, cancel := context.WithTimeout(ctx, deletedAgentConnectionCommandTimeout)
	defer cancel()
	commandID := domain.DeletedAgentConnectionCommandID(connection)
	raw, err := s.nodeControl.Command(
		commandCtx,
		nodeID,
		commandID,
		domain.DeleteNodeAgentConnectionCommand(commandID, connection.ConnectionID),
	)
	if err != nil {
		// A command that did not reach paxd may be retried by the next runtime
		// snapshot. One that timed out may still be running, so it waits for
		// the retry interval.
		if !errors.Is(err, context.DeadlineExceeded) {
			s.releaseDeletedAgentConnection(nodeID, connection)
		}
		logging.Warn(ctx, "deleted agent connection removal failed", logging.Err(err))
		return
	}
	var ack nodeDaemonCommandAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		logging.Warn(
			ctx,
			"deleted agent connection removal returned an invalid ack",
			logging.Err(err),
		)
		return
	}
	if !ack.OK {
		attrs := []slog.Attr{slog.String("command_status", ack.Status)}
		if ack.Error != nil {
			attrs = append(
				attrs,
				slog.String("error_code", ack.Error.Code),
				slog.String("error_message", ack.Error.Message),
			)
		}
		logging.Warn(ctx, "paxd rejected deleted agent connection removal", attrs...)
		return
	}
	logging.Info(ctx, "removed paxd connection of deleted agent")
}
