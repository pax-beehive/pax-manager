package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
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
			wantQuery:   `{"type":"agent_connections.list","list_agent_connections":{"include_disabled":true}}`,
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
				"host":{"hostname":"node-control","cpu_percent":12.5,"memory_percent":34.5},
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
			"node":{"name":"node-control","hostname":"node-control","os":"linux","arch":"arm64"},
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
