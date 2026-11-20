package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type snapshotConnection struct {
	connectionID string
	agentID      string
	desiredState string
	phase        string
}

func writeRuntimeSnapshot(
	t *testing.T,
	ws *websocket.Conn,
	nodeID string,
	snapshotID string,
	connections ...snapshotConnection,
) {
	t.Helper()
	agents := make([]map[string]any, 0, len(connections))
	for _, connection := range connections {
		agents = append(agents, map[string]any{
			"connection_id":  connection.connectionID,
			"cloud_agent_id": connection.agentID,
			"remote_id":      "remote_prod",
			"name":           "codex-main",
			"agent_type":     "codex",
			"desired_state":  connection.desiredState,
			"runtime_phase":  connection.phase,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"kind":      "report",
		"version":   1,
		"report_id": "rpt_" + snapshotID,
		"report": map[string]any{
			"type":      "runtime.snapshot",
			"remote_id": "remote_prod",
			"node_id":   nodeID,
			"runtime_snapshot": map[string]any{
				"snapshot_id": snapshotID,
				"host":        map[string]any{"machine_name": "node-control"},
				"agents":      agents,
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, payload))
}

// waitRuntimeSnapshot blocks until the node reflects the snapshot. Reports are
// handled in order, so everything sent before it was handled as well.
func waitRuntimeSnapshot(t *testing.T, srv *Server, apiKey string, snapshotID string) {
	t.Helper()
	waitNode(t, srv, apiKey, func(node Node) bool {
		var metadata struct {
			RuntimeSnapshot struct {
				SnapshotID string `json:"snapshot_id"`
			} `json:"runtime_snapshot"`
		}
		return json.Unmarshal(node.Metadata, &metadata) == nil &&
			metadata.RuntimeSnapshot.SnapshotID == snapshotID
	})
}

type nodeControlRequestFrame struct {
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id"`
	CommandID string          `json:"command_id"`
	Command   json.RawMessage `json:"command"`
}

func readNodeControlRequest(t *testing.T, ws *websocket.Conn) nodeControlRequestFrame {
	t.Helper()
	require.NoError(t, ws.SetReadDeadline(time.Now().Add(2*time.Second)))
	defer func() { _ = ws.SetReadDeadline(time.Time{}) }()
	_, payload, err := ws.ReadMessage()
	require.NoError(t, err)
	var frame nodeControlRequestFrame
	require.NoError(t, json.Unmarshal(payload, &frame))
	return frame
}

// expectConnectionRemoval reads the command that removes the connection of a
// deleted agent and acknowledges it the way paxd does.
func expectConnectionRemoval(
	t *testing.T,
	ws *websocket.Conn,
	agentID string,
	connectionID string,
) {
	t.Helper()
	frame := readNodeControlRequest(t, ws)
	require.Equal(t, "command", frame.Kind)
	require.Equal(
		t,
		domain.DeletedAgentConnectionCommandID(
			domain.NodeAgentConnectionRef{AgentID: agentID, ConnectionID: connectionID},
		),
		frame.CommandID,
	)
	require.JSONEq(t, `{
		"command_id":`+strconv.Quote(frame.CommandID)+`,
		"type":"agent_connection.delete",
		"delete_agent_connection":{"connection_id":`+strconv.Quote(connectionID)+`}
	}`, string(frame.Command))
	ack := `{"kind":"ack","command_id":` + strconv.Quote(frame.CommandID) +
		`,"command_ack":{"command_id":` + strconv.Quote(frame.CommandID) +
		`,"ok":true,"status":"received","target_id":` + strconv.Quote(connectionID) + `}}`
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(ack)))
}

// expectNoPendingNodeControlRequest proves that nothing else was sent to paxd:
// the next frame on the tunnel is the status query issued here.
func expectNoPendingNodeControlRequest(
	t *testing.T,
	srv *Server,
	ws *websocket.Conn,
	nodeID string,
	ownerEmail string,
) {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/nodes/"+nodeID+"/daemon/status",
		nil,
	)
	req.Header.Set("X-User-Email", ownerEmail)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.routes().ServeHTTP(rec, req)
		close(done)
	}()
	frame := readNodeControlRequest(t, ws)
	require.Equal(t, "query", frame.Kind, "unexpected request sent to paxd: %s", frame.Command)
	response := `{"kind":"response","request_id":` + strconv.Quote(frame.RequestID) +
		`,"query_result":{"type":"status.get","status":{"phase":"running"}}}`
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(response)))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon status query did not complete")
	}
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func deleteAgentThroughUserAPI(t *testing.T, srv *Server, ownerEmail string, agentID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/user/self/agents/"+agentID, nil)
	req.Header.Set("X-User-Email", ownerEmail)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func requireDeletedNodeAgent(t *testing.T, srv *Server, nodeID string, agentID string) {
	t.Helper()
	deleted, err := srv.store.ListDeletedNodeAgentIDs(
		context.Background(),
		nodeID,
		[]string{agentID},
	)
	require.NoError(t, err)
	require.Equal(t, []string{agentID}, deleted)
}

func TestRuntimeSnapshotDeletesAgentWhoseConnectionPaxdRemoved(t *testing.T) {
	const owner = "todd@example.com"
	srv, registered := testNodeControlServer(t, owner)
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()

	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_running", snapshotConnection{
		connectionID: "conn_main", agentID: registered.AgentID,
		desiredState: "running", phase: "running",
	})
	waitNodeAgents(t, srv, registered.APIKey, func(agents []Agent) bool {
		return len(agents) == 1 && agents[0].Online
	})

	// paxd keeps reporting a removed connection, marked as deleted.
	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_removed", snapshotConnection{
		connectionID: "conn_main", agentID: registered.AgentID,
		desiredState: "deleted", phase: "stopped",
	})
	waitNodeAgents(t, srv, registered.APIKey, func(agents []Agent) bool {
		return len(agents) == 0
	})
	requireDeletedNodeAgent(t, srv, registered.NodeID, registered.AgentID)

	// Repeating the report changes nothing and asks nothing of paxd.
	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_removed_again", snapshotConnection{
		connectionID: "conn_main", agentID: registered.AgentID,
		desiredState: "deleted", phase: "stopped",
	})
	waitRuntimeSnapshot(t, srv, registered.APIKey, "snap_removed_again")
	require.Empty(t, listNodeAgents(t, srv, registered.APIKey))
	expectNoPendingNodeControlRequest(t, srv, ws, registered.NodeID, owner)
}

func TestRuntimeSnapshotKeepsAgentReboundToANewConnection(t *testing.T) {
	const owner = "todd@example.com"
	srv, registered := testNodeControlServer(t, owner)
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()

	// The removed connection is listed last, where it used to win.
	writeRuntimeSnapshot(
		t, ws, registered.NodeID, "snap_rebound",
		snapshotConnection{
			connectionID: "conn_new", agentID: registered.AgentID,
			desiredState: "running", phase: "running",
		},
		snapshotConnection{
			connectionID: "conn_old", agentID: registered.AgentID,
			desiredState: "deleted", phase: "stopped",
		},
	)
	waitRuntimeSnapshot(t, srv, registered.APIKey, "snap_rebound")

	agents := listNodeAgents(t, srv, registered.APIKey)
	require.Len(t, agents, 1)
	require.Equal(t, registered.AgentID, agents[0].AgentID)
	require.True(t, agents[0].Online)
	var metadata struct {
		Runtime struct {
			ConnectionID string `json:"connection_id"`
		} `json:"runtime"`
	}
	require.NoError(t, json.Unmarshal(agents[0].Metadata, &metadata))
	require.Equal(t, "conn_new", metadata.Runtime.ConnectionID)
	deleted, err := srv.store.ListDeletedNodeAgentIDs(
		context.Background(),
		registered.NodeID,
		[]string{registered.AgentID},
	)
	require.NoError(t, err)
	require.Empty(t, deleted)
	expectNoPendingNodeControlRequest(t, srv, ws, registered.NodeID, owner)
}

func TestRuntimeSnapshotRemovesConnectionOfAgentDeletedInCloud(t *testing.T) {
	const owner = "todd@example.com"
	srv, registered := testNodeControlServer(t, owner)
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)

	// paxd never reported the connection, so the delete cannot name it yet.
	deleteAgentThroughUserAPI(t, srv, owner, registered.AgentID)
	requireDeletedNodeAgent(t, srv, registered.NodeID, registered.AgentID)

	stale := snapshotConnection{
		connectionID: "conn_stale", agentID: registered.AgentID,
		desiredState: "running", phase: "running",
	}
	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_stale", stale)
	expectConnectionRemoval(t, ws, registered.AgentID, "conn_stale")
	require.Empty(
		t,
		listNodeAgents(t, srv, registered.APIKey),
		"the report must not revive the agent",
	)

	// paxd has not applied the removal yet; the retry interval keeps the
	// manager from asking again on every snapshot.
	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_stale_again", stale)
	waitRuntimeSnapshot(t, srv, registered.APIKey, "snap_stale_again")
	expectNoPendingNodeControlRequest(t, srv, ws, registered.NodeID, owner)
	require.Empty(t, listNodeAgents(t, srv, registered.APIKey))
}

func TestDeleteAgentRemovesItsConnectionOnAConnectedNode(t *testing.T) {
	const owner = "todd@example.com"
	srv, registered := testNodeControlServer(t, owner)
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()

	live := snapshotConnection{
		connectionID: "conn_live", agentID: registered.AgentID,
		desiredState: "running", phase: "running",
	}
	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_live", live)
	waitNodeAgents(t, srv, registered.APIKey, func(agents []Agent) bool {
		return len(agents) == 1 && agents[0].Online
	})

	deleteAgentThroughUserAPI(t, srv, owner, registered.AgentID)
	expectConnectionRemoval(t, ws, registered.AgentID, "conn_live")
	requireDeletedNodeAgent(t, srv, registered.NodeID, registered.AgentID)

	// paxd applies the removal and reports the connection as deleted.
	live.desiredState, live.phase = "deleted", "stopped"
	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_applied", live)
	waitRuntimeSnapshot(t, srv, registered.APIKey, "snap_applied")
	expectNoPendingNodeControlRequest(t, srv, ws, registered.NodeID, owner)
	require.Empty(t, listNodeAgents(t, srv, registered.APIKey))
}

func TestDeleteAgentOnAnOfflineNodeRemovesItsConnectionAfterReconnect(t *testing.T) {
	const owner = "todd@example.com"
	srv, registered := testNodeControlServer(t, owner)
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()

	live := snapshotConnection{
		connectionID: "conn_live", agentID: registered.AgentID,
		desiredState: "running", phase: "running",
	}
	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_live", live)
	waitNodeAgents(t, srv, registered.APIKey, func(agents []Agent) bool {
		return len(agents) == 1 && agents[0].Online
	})

	require.NoError(t, ws.Close())
	require.Eventually(t, func() bool {
		_, err := srv.nodeControls.Connection(registered.NodeID)
		return err != nil
	}, time.Second, 10*time.Millisecond)

	// The cloud record goes away at once; the node cannot be told yet.
	deleteAgentThroughUserAPI(t, srv, owner, registered.AgentID)
	requireDeletedNodeAgent(t, srv, registered.NodeID, registered.AgentID)

	// The node comes back still running the connection of the deleted agent.
	ws, closeReconnected := dialNodeControlTunnel(t, srv, registered)
	defer closeReconnected()
	defer func() { _ = ws.Close() }()
	writeRuntimeSnapshot(t, ws, registered.NodeID, "snap_reconnected", live)
	expectConnectionRemoval(t, ws, registered.AgentID, "conn_live")
	require.Empty(t, listNodeAgents(t, srv, registered.APIKey))
}
