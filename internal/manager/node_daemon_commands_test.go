package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestNodeDaemonCommandRecordFollowsPaxdThroughHeartbeatSync(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	// Advance the clock on every read so the heartbeat sync throttle never
	// suppresses the sweep under test.
	base := srv.clock()
	var ticks atomic.Int64
	srv.clock = func() time.Time {
		return base.Add(time.Duration(ticks.Add(1)) * time.Minute)
	}
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
	writeNodeControlHeartbeat(t, ws, registered.NodeID, "rpt_identity")
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")

	const commandID = "cmd_paxl_sync"
	rec := serveAsync(t, srv, http.MethodPost,
		"/api/v1/user/self/nodes/"+registered.NodeID+"/paxl/upgrade",
		`{"command_id":"`+commandID+`","version":"1.2.3"}`,
		func() {
			frame := readNodeControlFrame(t, ws)
			require.Equal(t, "command", frame.Kind)
			require.Equal(t, commandID, frame.CommandID)
			require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(
				`{"kind":"ack","command_id":"`+commandID+`","command_ack":`+
					`{"command_id":"`+commandID+`","ok":true,"status":"received"}}`,
			)))
		},
	)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	accepted := decodeData[struct {
		DaemonCommand struct {
			State  string `json:"state"`
			Status string `json:"status"`
		} `json:"daemon_command"`
	}](t, rec.Body.Bytes())
	require.Equal(t, "in_progress", accepted.DaemonCommand.State)
	require.Equal(t, "received", accepted.DaemonCommand.Status)

	// The next heartbeat sweeps in-progress commands with command.get.
	writeNodeControlHeartbeat(t, ws, registered.NodeID, "rpt_after_upgrade")
	frame := readNodeControlFrame(t, ws)
	require.Equal(t, "query", frame.Kind)
	require.JSONEq(
		t,
		`{"type":"command.get","get_command":{"command_id":"`+commandID+`"}}`,
		string(frame.Query),
	)
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(
		`{"kind":"response","request_id":`+strconv.Quote(frame.RequestID)+`,"query_result":{
			"type":"command.get",
			"command":{
				"command_id":"`+commandID+`",
				"type":"paxl.upgrade",
				"status":"applied",
				"result":{"phase":"verified"}
			}
		}}`,
	)))

	type listedCommand struct {
		CommandID string `json:"command_id"`
		Type      string `json:"type"`
		State     string `json:"state"`
		Phase     string `json:"phase"`
	}
	require.Eventually(t, func() bool {
		rec := serve(srv, http.MethodGet,
			"/api/v1/user/self/nodes/"+registered.NodeID+"/daemon/commands?state=succeeded", "")
		if rec.Code != http.StatusOK {
			return false
		}
		listed := decodeData[struct {
			Commands []listedCommand `json:"commands"`
		}](t, rec.Body.Bytes())
		return len(listed.Commands) == 1 &&
			listed.Commands[0].CommandID == commandID &&
			listed.Commands[0].Phase == "verified"
	}, 2*time.Second, 10*time.Millisecond)

	rec = serve(srv, http.MethodGet, "/api/v1/user/self/nodes/"+registered.NodeID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	node := decodeData[struct {
		LatestDaemonCommands []listedCommand `json:"latest_daemon_commands"`
	}](t, rec.Body.Bytes())
	require.Equal(t, []listedCommand{{
		CommandID: commandID, Type: "paxl.upgrade", State: "succeeded", Phase: "verified",
	}}, node.LatestDaemonCommands)
}

func TestNodeDaemonCommandStoredRecordAnswersWhileNodeIsOffline(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
	writeNodeControlHeartbeat(t, ws, registered.NodeID, "rpt_identity")
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")

	const commandID = "cmd_paxl_offline"
	rec := serveAsync(t, srv, http.MethodPost,
		"/api/v1/user/self/nodes/"+registered.NodeID+"/paxl/upgrade",
		`{"command_id":"`+commandID+`","version":"1.2.3"}`,
		func() {
			readNodeControlFrame(t, ws)
			require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(
				`{"kind":"ack","command_id":"`+commandID+`","command_ack":`+
					`{"command_id":"`+commandID+`","ok":true,"status":"received"}}`,
			)))
		},
	)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	_ = ws.Close()
	closeServer()
	require.Eventually(t, func() bool {
		_, err := srv.nodeControls.Connection(registered.NodeID)
		return err != nil
	}, time.Second, 10*time.Millisecond)

	rec = serve(srv, http.MethodGet,
		"/api/v1/user/self/nodes/"+registered.NodeID+"/daemon/commands/"+commandID, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	result := decodeData[struct {
		Stale   bool `json:"stale"`
		Command struct {
			Status string `json:"status"`
		} `json:"command"`
	}](t, rec.Body.Bytes())
	require.True(t, result.Stale)
	require.Equal(t, "received", result.Command.Status)
}

type nodeControlTestFrame struct {
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id"`
	CommandID string          `json:"command_id"`
	Query     json.RawMessage `json:"query"`
	Command   json.RawMessage `json:"command"`
}

func readNodeControlFrame(t *testing.T, ws *websocket.Conn) nodeControlTestFrame {
	t.Helper()
	require.NoError(t, ws.SetReadDeadline(time.Now().Add(2*time.Second)))
	messageType, payload, err := ws.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	var frame nodeControlTestFrame
	require.NoError(t, json.Unmarshal(payload, &frame))
	return frame
}

func writeNodeControlHeartbeat(t *testing.T, ws *websocket.Conn, nodeID, reportID string) {
	t.Helper()
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{
		"kind":"report",
		"version":1,
		"report_id":"`+reportID+`",
		"report":{
			"type":"heartbeat",
			"remote_id":"remote_prod",
			"node_id":"`+nodeID+`",
			"heartbeat":{}
		}
	}`)))
}

func serve(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		setJSON(req)
	}
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

// serveAsync runs a request that blocks on paxd while peer plays paxd.
func serveAsync(
	t *testing.T,
	srv *Server,
	method, path, body string,
	peer func(),
) *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serve(srv, method, path, body) }()
	peer()
	select {
	case rec := <-done:
		return rec
	case <-time.After(2 * time.Second):
		t.Fatal("request did not complete")
		return nil
	}
}
