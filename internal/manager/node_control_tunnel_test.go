package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeControlHeartbeatReportRefreshesNodeLease(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()

	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_heartbeat_1",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"sent_at":"2026-06-24T12:00:00Z",
			"heartbeat":{}
		}
	}`)); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	node := waitNode(t, srv, registered.APIKey, func(node Node) bool {
		return node.Online && node.LastHeartbeat != nil
	})
	if !node.LastHeartbeat.Equal(srv.clock().UTC()) {
		t.Fatalf("last heartbeat = %v, want server clock", node.LastHeartbeat)
	}
}

func TestNodeControlHeartbeatPersistsPaxdVersionWithoutRuntimeSnapshotRollback(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_heartbeat_version",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"heartbeat":{
				"boot_id":"boot_current",
				"paxd_version":"0.1.31",
 "paxl":{"status":"installed","version":"0.1.50","path":"/opt/paxl","checked_at":"2026-10-07T12:00:00Z"},
				"daemon_phase":"running"
			}
		}
	}`)))

	node := waitNode(t, srv, registered.APIKey, func(node Node) bool {
		return node.PaxdVersion == "0.1.31"
	})
	require.Equal(t, "0.1.31", node.PaxdVersion)
	require.Contains(t, string(node.Metadata), `"version":"0.1.50"`)

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_snapshot_after_heartbeat",
		"report":{
			"type":"runtime.snapshot",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"runtime_snapshot":{
				"snapshot_id":"snap_after_heartbeat",
				"host":{"machine_name":"MacBook Pro","os":"darwin","arch":"amd64"},
				"agents":[]
			}
		}
	}`)))

	node = waitNode(t, srv, registered.APIKey, func(node Node) bool {
		return strings.Contains(string(node.Metadata), "snap_after_heartbeat")
	})
	require.Equal(t, "0.1.31", node.PaxdVersion)
	require.Contains(t, string(node.Metadata), `"version":"0.1.50"`)
}

func TestNodeControlTunnelRoutesQueryResponseWhileProcessingReport(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)

	resultCh := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := srv.nodeControls.Query(
			context.Background(),
			registered.NodeID,
			"req_status_1",
			map[string]any{"type": "status.get", "get_status": map[string]any{}},
		)
		resultCh <- result
		errCh <- err
	}()

	messageType, payload, err := ws.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	require.JSONEq(t, `{
		"kind":"query",
		"request_id":"req_status_1",
		"query":{"type":"status.get","get_status":{}}
	}`, string(payload))

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_during_query",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"sent_at":"2026-06-24T12:00:00Z",
			"heartbeat":{}
		}
	}`)))
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"response",
		"request_id":"req_status_1",
		"query_result":{"type":"status.get","status":{"phase":"running"}}
	}`)))

	require.NoError(t, <-errCh)
	require.JSONEq(
		t,
		`{"type":"status.get","status":{"phase":"running"}}`,
		string(<-resultCh),
	)
	waitNode(t, srv, registered.APIKey, func(node Node) bool {
		return node.Online && node.LastHeartbeat != nil
	})
}

func TestNodeDaemonReadAPIsForwardQueriesOverControlTunnel(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_read_api_identity",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"heartbeat":{}
		}
	}`)))
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")

	tests := []struct {
		path        string
		wantQuery   string
		queryResult string
	}{
		{
			path:        "daemon/status",
			wantQuery:   `{"type":"status.get","get_status":{}}`,
			queryResult: `{"type":"status.get","status":{"phase":"running"}}`,
		},
		{
			path:        "daemon/harnesses?include_missing=true",
			wantQuery:   `{"type":"harnesses.list","list_harnesses":{"include_missing":true}}`,
			queryResult: `{"type":"harnesses.list","harnesses":{"items":[]}}`,
		},
		{
			path:        "daemon/agent-connections?include_disabled=true",
			wantQuery:   `{"type":"agent_connections.list","list_agent_connections":{"remote_id":"remote_prod","include_disabled":true}}`,
			queryResult: `{"type":"agent_connections.list","agent_connections":{"items":[]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/user/self/nodes/"+registered.NodeID+"/"+tt.path,
				nil,
			)
			req.Header.Set("X-User-Email", "todd@example.com")
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				srv.routes().ServeHTTP(rec, req)
				close(done)
			}()

			messageType, payload, err := ws.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, websocket.TextMessage, messageType)
			var frame struct {
				Kind      string          `json:"kind"`
				RequestID string          `json:"request_id"`
				Query     json.RawMessage `json:"query"`
			}
			require.NoError(t, json.Unmarshal(payload, &frame))
			require.Equal(t, "query", frame.Kind)
			require.NotEmpty(t, frame.RequestID)
			require.JSONEq(t, tt.wantQuery, string(frame.Query))

			response := `{"kind":"response","request_id":` +
				strconv.Quote(frame.RequestID) +
				`,"query_result":` + tt.queryResult + `}`
			require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(response)))
			<-done

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			result := decodeData[json.RawMessage](t, rec.Body.Bytes())
			require.JSONEq(t, tt.queryResult, string(result))
		})
	}
}

func TestDiscoverNodeDaemonHarnessesForwardsQueryOverControlTunnel(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/nodes/"+registered.NodeID+"/daemon/harnesses/discover",
		strings.NewReader(`{"probe":true,"names":[" codex ",""]}`),
	)
	setJSON(req)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.routes().ServeHTTP(rec, req)
		close(done)
	}()

	messageType, payload, err := ws.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	var frame struct {
		Kind      string          `json:"kind"`
		RequestID string          `json:"request_id"`
		Query     json.RawMessage `json:"query"`
	}
	require.NoError(t, json.Unmarshal(payload, &frame))
	require.Equal(t, "query", frame.Kind)
	require.JSONEq(t, `{
		"type":"harnesses.discover",
		"discover_harnesses":{"probe":true,"names":["codex"]}
	}`, string(frame.Query))

	response := `{"kind":"response","request_id":` + strconv.Quote(frame.RequestID) +
		`,"query_result":{"type":"harnesses.discover","harnesses":{"items":[{` +
		`"harness":"codex","state":"available","command":["codex","--acp"]}]}}}`
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(response)))
	<-done

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	result := decodeData[json.RawMessage](t, rec.Body.Bytes())
	require.JSONEq(t, `{
		"type":"harnesses.discover",
		"harnesses":{"items":[{
			"harness":"codex",
			"state":"available",
			"command":["codex","--acp"]
		}]}
	}`, string(result))
}

func TestCreateNodeDaemonAgentConnectionAPI(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_remote_identity",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"heartbeat":{}
		}
	}`)))
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/nodes/"+registered.NodeID+"/daemon/agent-connections",
		bytes.NewReader([]byte(`{
			"command_id":"cmd_create_work_1",
			"name":"work",
			"agent_type":"codex",
			"harness":"codex",
			"instance_id":"work",
			"working_dir":"/workspace"
		}`)),
	)
	setJSON(req)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.routes().ServeHTTP(rec, req)
		close(done)
	}()

	_, payload, err := ws.ReadMessage()
	require.NoError(t, err)
	var queryFrame struct {
		Kind      string          `json:"kind"`
		RequestID string          `json:"request_id"`
		Query     json.RawMessage `json:"query"`
	}
	require.NoError(t, json.Unmarshal(payload, &queryFrame))
	require.Equal(t, "query", queryFrame.Kind)
	require.JSONEq(t, `{
		"type":"harnesses.list",
		"list_harnesses":{"include_missing":true}
	}`, string(queryFrame.Query))
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(
		`{"kind":"response","request_id":`+strconv.Quote(queryFrame.RequestID)+`,
		"query_result":{
			"type":"harnesses.list",
			"harnesses":{"items":[{
				"harness":"codex",
				"state":"available",
				"command":["codex","--acp"]
			}]}
		}}`,
	)))

	_, payload, err = ws.ReadMessage()
	require.NoError(t, err)
	var commandFrame struct {
		Kind      string `json:"kind"`
		CommandID string `json:"command_id"`
		Command   struct {
			CommandID string `json:"command_id"`
			Type      string `json:"type"`
			Create    struct {
				RemoteID     string   `json:"remote_id"`
				Name         string   `json:"name"`
				CloudAgentID string   `json:"cloud_agent_id"`
				InstanceID   string   `json:"instance_id"`
				AgentType    string   `json:"agent_type"`
				Harness      string   `json:"harness"`
				Command      []string `json:"command"`
				WorkingDir   string   `json:"working_dir"`
				DesiredState string   `json:"desired_state"`
			} `json:"create_agent_connection"`
		} `json:"command"`
	}
	require.NoError(t, json.Unmarshal(payload, &commandFrame))
	require.Equal(t, "command", commandFrame.Kind)
	require.Equal(t, "cmd_create_work_1", commandFrame.CommandID)
	require.Equal(t, commandFrame.CommandID, commandFrame.Command.CommandID)
	require.Equal(t, "agent_connection.create", commandFrame.Command.Type)
	create := commandFrame.Command.Create
	require.Equal(t, "remote_prod", create.RemoteID)
	require.Equal(t, "work", create.Name)
	require.NotEmpty(t, create.CloudAgentID)
	require.Equal(t, "work", create.InstanceID)
	require.Equal(t, "codex", create.AgentType)
	require.Equal(t, "codex", create.Harness)
	require.Equal(t, []string{"codex", "--acp"}, create.Command)
	require.Equal(t, "/workspace", create.WorkingDir)
	require.Equal(t, "running", create.DesiredState)

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"ack",
		"command_id":"cmd_create_work_1",
		"command_ack":{
			"command_id":"cmd_create_work_1",
			"ok":true,
			"status":"received",
			"target_type":"agent_connection",
			"target_id":"conn_work_1",
			"desired_generation":1
		}
	}`)))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("create daemon agent connection API did not complete")
	}

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	data := decodeData[struct {
		AgentID        string `json:"agent_id"`
		ConnectionID   string `json:"connection_id"`
		CommandID      string `json:"command_id"`
		CommandStatus  string `json:"command_status"`
		DispatchStatus string `json:"dispatch_status"`
	}](t, rec.Body.Bytes())
	require.Equal(t, create.CloudAgentID, data.AgentID)
	require.Equal(t, "conn_work_1", data.ConnectionID)
	require.Equal(t, "cmd_create_work_1", data.CommandID)
	require.Equal(t, "received", data.CommandStatus)
	require.Equal(t, "acknowledged", data.DispatchStatus)

	agents := listNodeAgents(t, srv, registered.APIKey)
	require.Len(t, agents, 2)
	var createdAgent Agent
	for _, agent := range agents {
		if agent.AgentID == create.CloudAgentID {
			createdAgent = agent
		}
	}
	require.Equal(t, "work", createdAgent.Name)
	require.Equal(t, "codex", createdAgent.AgentType)
}

func TestNodeDaemonAgentConnectionCommandAPIs(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_command_api_identity",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"heartbeat":{}
		}
	}`)))
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")

	connectionPath := "/api/v1/user/self/nodes/" + registered.NodeID +
		"/daemon/agent-connections/conn_1"
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		commandID   string
		wantCommand string
	}{
		{
			name:      "update",
			method:    http.MethodPatch,
			path:      connectionPath,
			body:      `{"command_id":"cmd_update_e2e","name":"renamed"}`,
			commandID: "cmd_update_e2e",
			wantCommand: `{
				"command_id":"cmd_update_e2e",
				"type":"agent_connection.update",
				"update_agent_connection":{
					"connection_id":"conn_1",
					"name":"renamed"
				}
			}`,
		},
		{
			name:      "stop",
			method:    http.MethodPost,
			path:      connectionPath + "/stop",
			body:      `{"command_id":"cmd_stop_e2e"}`,
			commandID: "cmd_stop_e2e",
			wantCommand: `{
				"command_id":"cmd_stop_e2e",
				"type":"agent_connection.update",
				"update_agent_connection":{
					"connection_id":"conn_1",
					"desired_state":"stopped"
				}
			}`,
		},
		{
			name:      "restart",
			method:    http.MethodPost,
			path:      connectionPath + "/restart",
			body:      `{"command_id":"cmd_restart_e2e"}`,
			commandID: "cmd_restart_e2e",
			wantCommand: `{
				"command_id":"cmd_restart_e2e",
				"type":"agent_connection.restart",
				"restart_agent_connection":{"connection_id":"conn_1"}
			}`,
		},
		{
			name:      "remove",
			method:    http.MethodDelete,
			path:      connectionPath,
			body:      `{"command_id":"cmd_remove_e2e"}`,
			commandID: "cmd_remove_e2e",
			wantCommand: `{
				"command_id":"cmd_remove_e2e",
				"type":"agent_connection.delete",
				"delete_agent_connection":{"connection_id":"conn_1"}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			setJSON(req)
			req.Header.Set("X-User-Email", "todd@example.com")
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				srv.routes().ServeHTTP(rec, req)
				close(done)
			}()

			_, payload, err := ws.ReadMessage()
			require.NoError(t, err)
			var frame struct {
				Kind      string          `json:"kind"`
				CommandID string          `json:"command_id"`
				Command   json.RawMessage `json:"command"`
			}
			require.NoError(t, json.Unmarshal(payload, &frame))
			require.Equal(t, "command", frame.Kind)
			require.Equal(t, tt.commandID, frame.CommandID)
			require.JSONEq(t, tt.wantCommand, string(frame.Command))

			ack := `{"kind":"ack","command_id":` + strconv.Quote(tt.commandID) +
				`,"command_ack":{"command_id":` + strconv.Quote(tt.commandID) +
				`,"ok":true,"status":"received","target_id":"conn_1",` +
				`"desired_generation":2}}`
			require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(ack)))
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("daemon command API did not complete")
			}
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			data := decodeData[struct {
				CommandID      string `json:"command_id"`
				CommandStatus  string `json:"command_status"`
				DispatchStatus string `json:"dispatch_status"`
			}](t, rec.Body.Bytes())
			require.Equal(t, tt.commandID, data.CommandID)
			require.Equal(t, "received", data.CommandStatus)
			require.Equal(t, "acknowledged", data.DispatchStatus)
		})
	}

	commandID := "cmd_restart_e2e"
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/nodes/"+registered.NodeID+"/daemon/commands/"+commandID,
		nil,
	)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.routes().ServeHTTP(rec, req)
		close(done)
	}()

	_, payload, err := ws.ReadMessage()
	require.NoError(t, err)
	var queryFrame struct {
		Kind      string          `json:"kind"`
		RequestID string          `json:"request_id"`
		Query     json.RawMessage `json:"query"`
	}
	require.NoError(t, json.Unmarshal(payload, &queryFrame))
	require.Equal(t, "query", queryFrame.Kind)
	require.JSONEq(t, `{
		"type":"command.get",
		"get_command":{"command_id":"cmd_restart_e2e"}
	}`, string(queryFrame.Query))
	response := `{"kind":"response","request_id":` + strconv.Quote(queryFrame.RequestID) +
		`,"query_result":{"type":"command.get","command":{` +
		`"command_id":"cmd_restart_e2e","status":"received"}}}`
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(response)))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon command query API did not complete")
	}
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	result := decodeData[json.RawMessage](t, rec.Body.Bytes())
	require.JSONEq(t, `{
		"type":"command.get",
		"command":{"command_id":"cmd_restart_e2e","status":"received"}
	}`, string(result))
}

func TestRestartNodeDaemonForwardsCommandOverControlTunnel(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_restart_daemon_identity",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"heartbeat":{}
		}
	}`)))
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")

	const commandID = "cmd_restart_paxd_e2e"
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/nodes/"+registered.NodeID+"/daemon/restart",
		strings.NewReader(`{
			"command_id":"cmd_restart_paxd_e2e",
			"shutdown_grace_seconds":12,
			"reason":"operator requested"
		}`),
	)
	setJSON(req)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.routes().ServeHTTP(rec, req)
		close(done)
	}()

	messageType, payload, err := ws.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	var frame struct {
		Kind      string          `json:"kind"`
		CommandID string          `json:"command_id"`
		Command   json.RawMessage `json:"command"`
	}
	require.NoError(t, json.Unmarshal(payload, &frame))
	require.Equal(t, "command", frame.Kind)
	require.Equal(t, commandID, frame.CommandID)
	require.JSONEq(t, `{
		"command_id":"cmd_restart_paxd_e2e",
		"type":"paxd.restart",
		"restart_paxd":{
			"mode":"immediate",
			"shutdown_grace_seconds":12,
			"reason":"operator requested"
		}
	}`, string(frame.Command))

	ack := `{"kind":"ack","command_id":"cmd_restart_paxd_e2e",` +
		`"command_ack":{"command_id":"cmd_restart_paxd_e2e",` +
		`"ok":true,"status":"received","target_type":"paxd"}}`
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(ack)))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("restart daemon API did not complete")
	}
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	data := decodeData[struct {
		CommandID      string `json:"command_id"`
		CommandStatus  string `json:"command_status"`
		DispatchStatus string `json:"dispatch_status"`
	}](t, rec.Body.Bytes())
	require.Equal(t, commandID, data.CommandID)
	require.Equal(t, "received", data.CommandStatus)
	require.Equal(t, "acknowledged", data.DispatchStatus)
}

func TestNodeControlRuntimeSnapshotUpdatesBoundAgentAndSkipsUnbound(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()

	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_snapshot_1",
		"report":{
			"type":"runtime.snapshot",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"sent_at":"2026-06-24T12:00:00Z",
			"runtime_snapshot":{
				"snapshot_id":"snap_1",
				"host":{"machine_name":"MacBook Pro","os":"darwin","arch":"amd64","cpu_percent":12.5,"memory_percent":34.5},
				"agents":[{
					"connection_id":"conn_bound",
					"cloud_agent_id":"`+registered.AgentID+`",
					"remote_id":"remote_prod",
					"node_id":"node_local",
					"name":"codex-main",
					"agent_type":"codex",
					"desired_state":"enabled",
					"runtime_phase":"running",
					"observed_generation":7,
					"observed_restart_nonce":3,
					"status_updated_at":"2026-06-24T12:00:00Z",
					"failure_class":"transient",
					"last_error_code":"reconnecting",
					"last_error_message":"retrying"
				},{
					"connection_id":"conn_unbound",
					"cloud_agent_id":"",
					"remote_id":"remote_prod",
					"node_id":"node_local",
					"name":"local-only",
					"agent_type":"codex",
					"desired_state":"enabled",
					"runtime_phase":"running"
				}]
			}
		}
	}`)); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	agents := waitNodeAgents(t, srv, registered.APIKey, func(agents []Agent) bool {
		return len(agents) == 1 && agents[0].Online && agents[0].Name == "codex-main"
	})
	if agents[0].AgentID != registered.AgentID {
		t.Fatalf("agent id = %q, want registered agent", agents[0].AgentID)
	}
	if agents[0].Status != "online" {
		t.Fatalf("agent status = %q, want online", agents[0].Status)
	}
	var agentMetadata struct {
		Runtime struct {
			RuntimePhase         string `json:"runtime_phase"`
			DesiredState         string `json:"desired_state"`
			ObservedGeneration   int64  `json:"observed_generation"`
			ObservedRestartNonce int64  `json:"observed_restart_nonce"`
			StatusUpdatedAt      string `json:"status_updated_at"`
			FailureClass         string `json:"failure_class"`
			LastErrorCode        string `json:"last_error_code"`
			LastErrorMessage     string `json:"last_error_message"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(agents[0].Metadata, &agentMetadata); err != nil {
		t.Fatalf("decode agent metadata: %v", err)
	}
	if agentMetadata.Runtime.RuntimePhase != "running" ||
		agentMetadata.Runtime.DesiredState != "enabled" ||
		agentMetadata.Runtime.ObservedGeneration != 7 ||
		agentMetadata.Runtime.ObservedRestartNonce != 3 ||
		agentMetadata.Runtime.StatusUpdatedAt != "2026-06-24T12:00:00Z" ||
		agentMetadata.Runtime.FailureClass != "transient" ||
		agentMetadata.Runtime.LastErrorCode != "reconnecting" ||
		agentMetadata.Runtime.LastErrorMessage != "retrying" {
		t.Fatalf("agent metadata = %s", agents[0].Metadata)
	}

	node := getNode(t, srv, registered.APIKey)
	requireNodeIdentity(t, node, "MacBook Pro", "darwin", "amd64")
	var nodeMetadata struct {
		RuntimeSnapshot struct {
			SnapshotID  string `json:"snapshot_id"`
			HostMetrics struct {
				CPUPercent    float64 `json:"cpu_percent"`
				MemoryPercent float64 `json:"memory_percent"`
			} `json:"host_metrics"`
		} `json:"runtime_snapshot"`
	}
	if err := json.Unmarshal(node.Metadata, &nodeMetadata); err != nil {
		t.Fatalf("decode node metadata: %v", err)
	}
	if nodeMetadata.RuntimeSnapshot.SnapshotID != "snap_1" ||
		nodeMetadata.RuntimeSnapshot.HostMetrics.CPUPercent != 12.5 ||
		nodeMetadata.RuntimeSnapshot.HostMetrics.MemoryPercent != 34.5 {
		t.Fatalf("node metadata = %s", node.Metadata)
	}
}

func requireNodeIdentity(t *testing.T, node Node, machineType, osName, arch string) {
	t.Helper()
	if node.MachineType != machineType || node.OS != osName || node.Arch != arch {
		t.Fatalf(
			"node identity = machine_type:%q os:%q arch:%q",
			node.MachineType,
			node.OS,
			node.Arch,
		)
	}
}

func TestNodeControlRuntimeSnapshotPersistsACPPoolCapabilityReport(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()

	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_snapshot_acp_capability",
		"report":{
			"type":"runtime.snapshot",
			"remote_id":"remote_prod",
			"node_id":"`+registered.NodeID+`",
			"sent_at":"2026-06-24T12:00:00Z",
			"runtime_snapshot":{
				"snapshot_id":"snap_acp_capability",
				"agents":[{
					"connection_id":"conn_bound",
					"cloud_agent_id":"`+registered.AgentID+`",
					"remote_id":"remote_prod",
					"node_id":"node_local",
					"name":"codex-main",
					"agent_type":"codex",
					"desired_state":"enabled",
					"runtime_phase":"running",
					"acp_pool_capability_report":{
						"schema_version":1,
						"connection_id":"conn_bound",
						"report_generation":7,
						"paxd_version":"dev",
						"command_fingerprint":"fingerprint_1",
						"client_profile_hash":"profile_hash_1",
						"worker_result_hash":"worker_hash_1",
						"protocol_version":1,
						"worker_capability_keys":["prompt"],
						"init_phase":"ready"
					}
				}]
			}
		}
	}`)); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	agents := waitNodeAgents(t, srv, registered.APIKey, func(agents []Agent) bool {
		return len(agents) == 1 && agents[0].Online
	})
	require.Equal(t, registered.AgentID, agents[0].AgentID)
	var metadata struct {
		ACPPoolCapabilityReport struct {
			ConnectionID       string `json:"connection_id"`
			PaxdVersion        string `json:"paxd_version"`
			WorkerResultHash   string `json:"worker_result_hash"`
			InitPhase          string `json:"init_phase"`
			ReportGeneration   int64  `json:"report_generation"`
			ProtocolVersion    int    `json:"protocol_version"`
			CommandFingerprint string `json:"command_fingerprint"`
		} `json:"acp_pool_capability_report"`
	}
	require.NoError(t, json.Unmarshal(agents[0].Metadata, &metadata))
	report := metadata.ACPPoolCapabilityReport
	require.Equal(t, "conn_bound", report.ConnectionID)
	require.Equal(t, "dev", report.PaxdVersion)
	require.Equal(t, "worker_hash_1", report.WorkerResultHash)
	require.Equal(t, "ready", report.InitPhase)
	require.Equal(t, int64(7), report.ReportGeneration)
	require.Equal(t, 1, report.ProtocolVersion)
	require.Equal(t, "fingerprint_1", report.CommandFingerprint)
}

func TestNodeControlSessionRuntimeSnapshotDefersUnknownUntilCanonicalBindingThenReconcilesAbsent(
	t *testing.T,
) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	principal := UserPrincipal{User: User{UserID: registered.Agent.OwnerUserID}}
	canonical, err := srv.store.CreateNodeAgentSession(t.Context(), principal, CreateSessionRequest{
		NodeID: registered.NodeID, AgentID: registered.AgentID, SessionID: "sess_encrypted",
		Source: "console",
	})
	require.NoError(t, err)

	writeSessionRuntimeReport := func(sequence int, activeTurns string) {
		require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{
			"kind":"report","version":1,"report_id":"rpt_session_%d",
			"report":{
				"type":"session_runtime.snapshot","remote_id":"remote_prod",
				"node_id":%q,"sent_at":"2026-08-03T12:00:00Z",
				"session_runtime_snapshot":{
					"agent_id":%q,"connection_id":"conn_1","sequence":%d,
					"generated_at":"2026-08-03T12:00:00Z","schema_version":1,
					"active_turns":%s
				}
			}
		}`, sequence, registered.NodeID, registered.AgentID, sequence, activeTurns))))
	}

	writeSessionRuntimeReport(1, `[{
		"native_session_id":"native_1","turn_id":"turn_1",
		"prompt_request_id":1,"runtime_status":"running"
	}]`)
	require.Eventually(t, func() bool {
		sessions, err := srv.store.ListAgentSessions(t.Context(), principal, registered.AgentID)
		return err == nil && len(sessions) == 1 && sessions[0].SessionID == canonical.SessionID &&
			sessions[0].NativeID == "" && sessions[0].RuntimeStatus == domain.RuntimeStatusIdle
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, srv.store.UpsertAgentSessions(t.Context(), Node{
		NodeID: registered.NodeID, OwnerUserID: registered.Agent.OwnerUserID,
	}, registered.AgentID, []SessionStatusInput{{
		SessionID: canonical.SessionID, NativeID: "native_1", Status: "idle",
	}}))
	writeSessionRuntimeReport(2, `[{
		"native_session_id":"native_1","turn_id":"turn_1",
		"prompt_request_id":1,"runtime_status":"running"
	}]`)
	require.Eventually(t, func() bool {
		sessions, err := srv.store.ListAgentSessions(t.Context(), principal, registered.AgentID)
		return err == nil && len(sessions) == 1 && sessions[0].SessionID == canonical.SessionID &&
			sessions[0].NativeID == "native_1" &&
			sessions[0].RuntimeStatus == domain.RuntimeStatusRunning &&
			sessions[0].RuntimeTurnInstanceID == "turn_1"
	}, time.Second, 10*time.Millisecond)

	writeSessionRuntimeReport(3, `[]`)
	require.Eventually(t, func() bool {
		sessions, err := srv.store.ListAgentSessions(t.Context(), principal, registered.AgentID)
		return err == nil && len(sessions) == 1 &&
			sessions[0].RuntimeStatus == domain.RuntimeStatusIdle &&
			sessions[0].RuntimeTurnInstanceID == ""
	}, time.Second, 10*time.Millisecond)
	writeSessionRuntimeReport(4, `[{
  "native_session_id":"native_1","turn_id":"turn_2",
  "prompt_request_id":2,"runtime_status":"running"
 }]`)
	require.Eventually(t, func() bool {
		session, err := srv.store.GetSession(t.Context(), principal, canonical.SessionID)
		return err == nil && session.RuntimeTurnInstanceID == "turn_2"
	}, time.Second, 10*time.Millisecond)
	beforeDisconnect, err := srv.store.GetSession(t.Context(), principal, canonical.SessionID)
	require.NoError(t, err)
	require.NoError(t, ws.Close())
	require.Eventually(t, func() bool {
		_, err := srv.nodeControls.Connection(registered.NodeID)
		return err != nil
	}, time.Second, 10*time.Millisecond)
	afterDisconnect, err := srv.store.GetSession(t.Context(), principal, canonical.SessionID)
	require.NoError(t, err)
	require.Equal(t, beforeDisconnect.RuntimeState, afterDisconnect.RuntimeState)
	require.Equal(t, domain.RuntimeStatusRunning, afterDisconnect.RuntimeStatus)

}

func TestSessionRuntimeResetEndpointForwardsCanonicalCompareIdentity(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	principal := UserPrincipal{User: User{UserID: registered.Agent.OwnerUserID}}
	session, err := srv.store.CreateNodeAgentSession(t.Context(), principal, CreateSessionRequest{
		NodeID: registered.NodeID, AgentID: registered.AgentID,
		SessionID: "sess_encrypted", NativeID: "native_1", Source: "console",
	})
	require.NoError(t, err)

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{
		"kind":"report","version":1,"report_id":"rpt_inventory",
		"report":{"type":"runtime.snapshot","remote_id":"remote_prod","node_id":%q,
		"sent_at":"2026-08-03T12:00:00Z","runtime_snapshot":{"snapshot_id":"snap_1","agents":[{
			"connection_id":"conn_1","cloud_agent_id":%q,"remote_id":"remote_prod",
			"name":"codex-main","agent_type":"codex","runtime_phase":"running"
		}]}}
	}`, registered.NodeID, registered.AgentID))))
	require.Eventually(t, func() bool {
		remoteID, err := srv.nodeControls.RemoteID(registered.NodeID)
		return err == nil && remoteID == "remote_prod"
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{
		"kind":"report","version":1,"report_id":"rpt_session_1",
		"report":{"type":"session_runtime.snapshot","remote_id":"remote_prod","node_id":%q,
		"sent_at":"2026-08-03T12:00:00Z","session_runtime_snapshot":{
			"agent_id":%q,"connection_id":"conn_1","sequence":1,
			"generated_at":"2026-08-03T12:00:00Z","schema_version":1,"active_turns":[{
				"native_session_id":"native_1","turn_instance_id":"turn_1",
				"prompt_request_id":1,"runtime_status":"running"
			}]}}
	}`, registered.NodeID, registered.AgentID))))
	require.Eventually(t, func() bool {
		sessions, err := srv.store.ListAgentSessions(t.Context(), principal, registered.AgentID)
		return err == nil && len(sessions) == 1 && sessions[0].SessionID == session.SessionID &&
			sessions[0].RuntimeTurnInstanceID == "turn_1"
	}, time.Second, 10*time.Millisecond)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/agents/"+registered.AgentID+"/sessions/"+session.SessionID+"/runtime/reset",
		strings.NewReader(`{"expected_turn_instance_id":"turn_1"}`),
	)
	setJSON(req)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleSessionRuntimeReset(rec, req)
	}()

	_, commandPayload, err := ws.ReadMessage()
	require.NoError(t, err)
	var commandFrame struct {
		CommandID string `json:"command_id"`
		Command   struct {
			Type  string `json:"type"`
			Reset struct {
				AgentID                string `json:"agent_id"`
				ConnectionID           string `json:"connection_id"`
				NativeSessionID        string `json:"native_session_id"`
				ExpectedTurnInstanceID string `json:"expected_turn_instance_id"`
			} `json:"reset_session_runtime"`
		} `json:"command"`
	}
	require.NoError(t, json.Unmarshal(commandPayload, &commandFrame))
	require.Equal(t, "session_runtime.reset", commandFrame.Command.Type)
	require.Equal(t, registered.AgentID, commandFrame.Command.Reset.AgentID)
	require.Equal(t, "conn_1", commandFrame.Command.Reset.ConnectionID)
	require.Equal(t, "native_1", commandFrame.Command.Reset.NativeSessionID)
	require.Equal(t, "turn_1", commandFrame.Command.Reset.ExpectedTurnInstanceID)
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{
		"kind":"ack","command_id":%q,"command_ack":{
			"command_id":%q,"ok":true,"status":"applied",
			"result":{"session_runtime_reset":{"status":"suppressed","projection_revision":2}}
		}
	}`, commandFrame.CommandID, commandFrame.CommandID))))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime reset endpoint did not complete")
	}
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	response := decodeData[map[string]any](t, rec.Body.Bytes())
	require.Equal(t, "accepted_pending", response["status"])
}

func TestNodeControlSessionRuntimeSnapshotRejectsInvalidOrFencedReports(t *testing.T) {
	t.Run("Given the report payload is missing then it is rejected", func(t *testing.T) {
		err := new(Server).replaceSessionRuntimeSnapshot(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			"fence_1",
			nodeControlReport{},
		)

		require.EqualError(
			t,
			err,
			"session_runtime.snapshot report missing session_runtime_snapshot",
		)
	})

	t.Run("Given the schema version is unsupported then it is rejected", func(t *testing.T) {
		err := new(Server).replaceSessionRuntimeSnapshot(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			"fence_1",
			nodeControlReport{
				SessionRuntimeSnapshot: &nodeControlSessionRuntimeSnapshot{SchemaVersion: 2},
			},
		)

		require.EqualError(t, err, "unsupported session runtime schema version 2")
	})

	t.Run("Given runtime snapshot storage is unavailable then it is rejected", func(t *testing.T) {
		err := new(Server).replaceSessionRuntimeSnapshot(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			"fence_1",
			validNodeControlSessionRuntimeReport("agent_1"),
		)

		require.EqualError(t, err, "session runtime snapshot store is unavailable")
	})

	t.Run("Given a stale connection fence then it cannot mutate runtime state", func(t *testing.T) {
		srv, registered := testNodeControlServer(t, "todd@example.com")

		err := srv.replaceSessionRuntimeSnapshot(
			context.Background(),
			Node{NodeID: registered.NodeID, OwnerUserID: registered.Agent.OwnerUserID},
			"stale_fence",
			validNodeControlSessionRuntimeReport(registered.AgentID),
		)

		require.EqualError(t, err, "session runtime snapshot came from a fenced connection")
	})
}

func validNodeControlSessionRuntimeReport(agentID string) nodeControlReport {
	return nodeControlReport{SessionRuntimeSnapshot: &nodeControlSessionRuntimeSnapshot{
		AgentID: agentID, Sequence: 1, SchemaVersion: 1,
		GeneratedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
		ActiveTurns: []nodeControlSessionRuntimeActiveTurn{{
			NativeSessionID: "native_1", TurnInstanceID: "turn_1",
			PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
		}},
	}}
}

func TestNodeControlRejectsMismatchedReportNodeIDWithoutRefreshingLease(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	nodeBefore := getNode(t, srv, registered.APIKey)

	err := srv.handleNodeControlTunnelFrame(context.Background(), Node{
		NodeID:      registered.NodeID,
		OwnerUserID: "todd@example.com",
	}, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"rpt_bad_node",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"node_other",
			"sent_at":"2026-06-24T12:00:00Z",
			"heartbeat":{}
		}
	}`))
	if err == nil {
		t.Fatal("mismatched node_id report error = nil")
	}

	nodeAfter := getNode(t, srv, registered.APIKey)
	if !sameOptionalTime(nodeAfter.LastHeartbeat, nodeBefore.LastHeartbeat) ||
		nodeAfter.Online != nodeBefore.Online {
		t.Fatalf("node after invalid report = %+v, want unchanged from %+v", nodeAfter, nodeBefore)
	}
}

func TestNodeControlFrameValidationRejectsMalformedReports(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	node := getNode(t, srv, registered.APIKey)
	for _, tt := range []struct {
		name    string
		payload string
	}{
		{name: "invalid json", payload: `{`},
		{name: "unsupported kind", payload: `{"kind":"command","version":1,"report":{"type":"heartbeat"}}`},
		{name: "unsupported version", payload: `{"kind":"report","version":2,"report":{"type":"heartbeat"}}`},
		{name: "unsupported type", payload: `{"kind":"report","version":1,"report":{"type":"command.result"}}`},
		{name: "missing runtime snapshot", payload: `{"kind":"report","version":1,"report":{"type":"runtime.snapshot"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := srv.handleNodeControlTunnelFrame(
				context.Background(),
				node,
				[]byte(tt.payload),
			); err == nil {
				t.Fatal("error = nil")
			}
		})
	}
}

func TestRuntimePhaseAgentStatusMapping(t *testing.T) {
	tests := map[string]struct {
		phase  string
		status string
		online bool
	}{
		"running":  {phase: "running", status: "online", online: true},
		"starting": {phase: "STARTING", status: "online", online: true},
		"failed":   {phase: "failed", status: "error", online: false},
		"stopped":  {phase: "stopped", status: "offline", online: false},
		"empty":    {phase: "", status: "offline", online: false},
		"custom":   {phase: "degraded", status: "degraded", online: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := runtimePhaseAgentStatus(tt.phase); got != tt.status {
				t.Fatalf("status = %q, want %q", got, tt.status)
			}
			if got := runtimePhaseAgentOnline(tt.phase); got != tt.online {
				t.Fatalf("online = %v, want %v", got, tt.online)
			}
		})
	}
}

func TestRuntimeSnapshotMetadataHelpersHandleEmptyInputs(t *testing.T) {
	if got := runtimeSnapshotNodeMetadata("remote_prod", nil); got != nil {
		t.Fatalf("nil snapshot metadata = %s, want nil", got)
	}
	if got := runtimeSnapshotNodeMetadata("remote_prod", &nodeControlRuntimeSnapshot{}); got != nil {
		t.Fatalf("empty host metadata = %s, want nil", got)
	}
	if got := cloneRawJSON(nil); got != nil {
		t.Fatalf("clone nil raw json = %s, want nil", got)
	}
	raw := json.RawMessage(`{"ok":true}`)
	cloned := cloneRawJSON(raw)
	raw[1] = 'x'
	if string(cloned) != `{"ok":true}` {
		t.Fatalf("cloned raw json = %s", cloned)
	}
}

func TestAuthenticateNodeControlTunnelValidation(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")

	missingKeyReq := httptest.NewRequest(http.MethodGet, "/api/v1/node/control", nil)
	if _, err := srv.authenticateNodeControlTunnel(missingKeyReq); err == nil {
		t.Fatal("missing pax key error = nil")
	}

	mismatchReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/node/control?node_id=node_other",
		nil,
	)
	mismatchReq.Header.Set("X-Pax-Key", registered.APIKey)
	if _, err := srv.authenticateNodeControlTunnel(mismatchReq); err == nil {
		t.Fatal("mismatched node_id error = nil")
	}

	nodeIDAliasReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/node/control?nodeId="+registered.NodeID,
		nil,
	)
	nodeIDAliasReq.Header.Set("X-Pax-Key", registered.APIKey)
	node, err := srv.authenticateNodeControlTunnel(nodeIDAliasReq)
	if err != nil {
		t.Fatalf("authenticate with nodeId alias: %v", err)
	}
	if node.NodeID != registered.NodeID {
		t.Fatalf("node id = %q, want %q", node.NodeID, registered.NodeID)
	}
}

func TestNodeControlTunnelRejectsMissingPaxKey(t *testing.T) {
	srv, _ := testNodeControlServer(t, "todd@example.com")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/node/control", nil)
	rec := httptest.NewRecorder()
	srv.handleNodeControlTunnel(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func testNodeControlServer(t *testing.T, ownerEmail string) (*Server, RegisterNodeAgentResponse) {
	t.Helper()
	srv, _ := testServer(t, ownerEmail)
	tokenReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/node-registration-tokens",
		bytes.NewReader([]byte(`{}`)),
	)
	setJSON(tokenReq)
	tokenReq.Header.Set("X-User-Email", ownerEmail)
	tokenRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("node token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	tokenResp := decodeData[CreateRegistrationTokenResponse](t, tokenRec.Body.Bytes())

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/agents/register",
		bytes.NewReader([]byte(`{
			"node":{"name":"node-control","hostname":"node-control","os":"linux","arch":"arm64","paxd_version":"0.1.29"},
			"agent":{"name":"pending-main","agent_type":"codex"}
		}`)),
	)
	setJSON(registerReq)
	registerReq.Header.Set("X-Registration-Token", tokenResp.Token)
	registerRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf(
			"register node agent code = %d, body = %s",
			registerRec.Code,
			registerRec.Body.String(),
		)
	}
	registered := decodeData[RegisterNodeAgentResponse](t, registerRec.Body.Bytes())
	if registered.NodeID == "" || registered.AgentID == "" || registered.APIKey == "" {
		t.Fatalf("bad node agent register response: %+v", registered)
	}
	return srv, registered
}

func dialNodeControlTunnel(
	t *testing.T,
	srv *Server,
	registered RegisterNodeAgentResponse,
) (*websocket.Conn, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/node/control", srv.handleNodeControlTunnel)
	httpServer := httptest.NewServer(mux)
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/node/control?node_id="+registered.NodeID,
		http.Header{"X-Pax-Key": []string{registered.APIKey}},
	)
	if err != nil {
		httpServer.Close()
		t.Fatalf("dial node control tunnel: %v", err)
	}
	return ws, httpServer.Close
}

func waitNode(
	t *testing.T,
	srv *Server,
	apiKey string,
	ready func(Node) bool,
) Node {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		node := getNode(t, srv, apiKey)
		if ready(node) {
			return node
		}
		time.Sleep(10 * time.Millisecond)
	}
	node := getNode(t, srv, apiKey)
	t.Fatalf("node did not reach expected state: %+v", node)
	return Node{}
}

func waitNodeControlConnection(
	t *testing.T,
	hub *NodeControlHub,
	nodeID string,
) *nodeControlConnection {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := hub.Connection(nodeID)
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := hub.Connection(nodeID)
	if err != nil {
		t.Fatalf("node control connection %q was not registered: %v", nodeID, err)
	}
	return conn
}

func waitNodeControlRemoteID(
	t *testing.T,
	hub *NodeControlHub,
	nodeID string,
	want string,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		remoteID, err := hub.RemoteID(nodeID)
		return err == nil && remoteID == want
	}, time.Second, 10*time.Millisecond)
}

func waitNodeAgents(
	t *testing.T,
	srv *Server,
	apiKey string,
	ready func([]Agent) bool,
) []Agent {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		agents := listNodeAgents(t, srv, apiKey)
		if ready(agents) {
			return agents
		}
		time.Sleep(10 * time.Millisecond)
	}
	agents := listNodeAgents(t, srv, apiKey)
	t.Fatalf("agents did not reach expected state: %+v", agents)
	return nil
}

func getNode(t *testing.T, srv *Server, apiKey string) Node {
	t.Helper()
	node, err := srv.store.AuthenticateNode(context.Background(), srv.secrets.Hash(apiKey))
	if err != nil {
		t.Fatalf("authenticate node: %v", err)
	}
	return node
}

func listNodeAgents(t *testing.T, srv *Server, apiKey string) []Agent {
	t.Helper()
	node := getNode(t, srv, apiKey)
	agents, err := srv.store.ListNodeAgents(
		context.Background(),
		userPrincipal(node.OwnerUserID),
		node.NodeID,
	)
	if err != nil {
		t.Fatalf("list node agents %q: %v", node.NodeID, err)
	}
	return agents
}

func userPrincipal(ownerUserID string) UserPrincipal {
	return UserPrincipal{
		User: User{
			UserID: ownerUserID,
			Email:  ownerUserID,
		},
	}
}

func sameOptionalTime(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}
