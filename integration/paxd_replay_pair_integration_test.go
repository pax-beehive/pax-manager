//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// This opt-in test runs the actual paxd daemon, local control API, SQLite store,
// ACP worker and websocket reconnect path against the running integration Manager.
// The image contains /app/paxd and the tools/replay-pair fixture binary.
func TestActualPaxdReplayAcrossReconnectIntegration(t *testing.T) {
	image := os.Getenv("PAXD_REPLAY_IMAGE")
	if image == "" {
		t.Skip("set PAXD_REPLAY_IMAGE, PAXD_REPLAY_NETWORK and PAXD_REPLAY_MANAGER_URL")
	}
	network := os.Getenv("PAXD_REPLAY_NETWORK")
	managerURL := os.Getenv("PAXD_REPLAY_MANAGER_URL")
	require.NotEmpty(t, network)
	require.NotEmpty(t, managerURL)
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)
	headers := fixture.userHeaders()
	node := createBoundaryNode(t, fixture, headers, "real-paxd-replay")
	agent := createBoundaryAgent(t, fixture, headers, node.NodeID, "real-paxd-worker")
	sessionID := fmt.Sprintf("sess-real-paxd-%d", time.Now().UnixNano())
	base := "/api/v1/user/self/nodes/" + node.NodeID + "/agents/" + agent.Agent.AgentID + "/sessions"
	postJSON[session](
		t,
		fixture,
		base,
		map[string]any{"session_id": sessionID},
		headers,
		http.StatusOK,
	)
	stateDir := t.TempDir()
	workerDir := t.TempDir()
	keyDir := filepath.Join(stateDir, "secrets", "remotes", "pair")
	require.NoError(t, os.MkdirAll(keyDir, 0700))
	require.NoError(
		t,
		os.WriteFile(filepath.Join(keyDir, "node_key"), []byte(node.APIKey+"\n"), 0600),
	)
	name := fmt.Sprintf("paxd-replay-pair-%d", time.Now().UnixNano())
	proxy := name + "-proxy"
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name, proxy).Run() })
	dockerInput := func(input []byte, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.WaitDelay = time.Second
		if input != nil {
			cmd.Stdin = bytes.NewReader(input)
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "docker %s: %s", strings.Join(args, " "), out)
		return strings.TrimSpace(string(out))
	}
	docker := func(args ...string) string { return dockerInput(nil, args...) }
	docker(
		"run",
		"--pull=never",
		"-d",
		"--name",
		proxy,
		"--network",
		network,
		"--entrypoint",
		"/app/pair-fixture",
		image,
		"proxy",
		"--upstream",
		managerURL,
	)
	proxyIP := docker(
		"inspect",
		"--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
		proxy,
	)
	proxyURL := "http://" + proxyIP + ":9880"
	docker("run", "--pull=never", "-d", "--name", name, "--network", network,
		"-v", stateDir+":/root/.paxd", "-v", workerDir+":/fixture",
		"-e", "PAXD_DB_PATH=/root/.paxd/paxd.db",
		"--entrypoint", "/app/paxd", image, "run")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", "--tail", "70", name).CombinedOutput()
			t.Logf("isolated paxd logs: %s", out)
			for _, dir := range []string{stateDir, workerDir} {
				_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
					if err == nil && !entry.IsDir() &&
						(strings.HasSuffix(path, ".log") || entry.Name() == "events.jsonl") {
						data, _ := os.ReadFile(path)
						if len(data) > 12000 {
							data = data[len(data)-12000:]
						}
						t.Logf("isolated fixture %s: %s", entry.Name(), data)
					}
					return nil
				})
			}
		}
	})
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(stateDir, "paxd.sock"))
		return err == nil
	}, 20*time.Second, 100*time.Millisecond)
	control := func(path string, body any) {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		response := dockerInput(
			data,
			"exec",
			"-i",
			name,
			"/app/pair-fixture",
			"control",
			"POST",
			path,
			"-",
		)
		var ack struct {
			OK     bool   `json:"ok"`
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal([]byte(response), &ack))
		require.True(t, ack.OK, response)
		require.Contains(t, []string{"received", "applied"}, ack.Status)
	}
	control("/v1/remotes", map[string]any{
		"remote": map[string]any{
			"id":            "pair",
			"name":          "pair",
			"cloud_api_url": proxyURL,
			"node_id":       node.NodeID,
			"enabled":       true,
		},
		"cloud_api_key_ref": "file:/root/.paxd/secrets/remotes/pair/node_key",
	})
	control("/v1/agent-connections", map[string]any{
		"id": "pair-connection", "remote_id": "pair", "name": "pair-worker", "cloud_agent_id": agent.Agent.AgentID,
		"instance_id": "pair-instance", "agent_type": "hermes", "harness": "hermes",
		"command": []string{"/app/pair-fixture", "mock-acp"}, "working_dir": "/fixture",
		"enabled": true, "desired_state": "running", "desired_slots": 1, "report_local_sessions": false,
	})
	// Query the fixture through its own container because the Docker network is
	// intentionally private and its bridge IP is not routable on macOS.
	proxyStatus := func() map[string]int {
		t.Helper()
		response := docker(
			"exec",
			proxy,
			"/app/pair-fixture",
			"http",
			"GET",
			"http://127.0.0.1:9880/_fixture/status",
		)
		var result map[string]int
		require.NoError(t, json.Unmarshal([]byte(response), &result))
		return result
	}
	require.Eventually(
		t,
		func() bool { return proxyStatus()["active_websockets"] >= 2 },
		30*time.Second,
		200*time.Millisecond,
	)
	userWS := fixture.connectACPUserTunnel(t, agent.Agent.AgentID, sessionID, headers)
	t.Cleanup(func() { _ = userWS.Close() })
	writeRawWS(t, userWS, mustMarshalString(t, map[string]any{
		"jsonrpc": "2.0", "id": "create-1", "method": "session/new",
		"params": map[string]any{"sessionId": sessionID, "cwd": "/fixture", "mcpServers": []any{}},
	}))
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(workerDir, "new-started"))
		return err == nil
	}, 15*time.Second, 100*time.Millisecond)
	before := proxyStatus()["total_ws_connections"]
	docker(
		"exec",
		proxy,
		"/app/pair-fixture",
		"http",
		"POST",
		"http://127.0.0.1:9880/_fixture/drop",
	)
	require.Eventually(t, func() bool {
		status := proxyStatus()
		return status["active_websockets"] >= 2 && status["total_ws_connections"] > before
	}, 30*time.Second, 200*time.Millisecond)
	// Manager closes its downstream user relay when the agent transport drops.
	// Reconnect that relay before releasing the outstanding worker result.
	_ = userWS.Close()
	userWS = fixture.connectACPUserTunnel(t, agent.Agent.AgentID, sessionID, headers)
	require.NoError(t, os.WriteFile(filepath.Join(workerDir, "release-new"), nil, 0600))
	var seen [][]byte
	t.Cleanup(func() {
		if t.Failed() {
			for _, payload := range seen {
				t.Logf("isolated user relay: %s", payload)
			}
		}
	})
	readUntil := func(want string) {
		t.Helper()
		for _, payload := range seen {
			if bytes.Contains(payload, []byte(want)) {
				return
			}
		}
		require.NoError(t, userWS.SetReadDeadline(time.Now().Add(15*time.Second)))
		for {
			_, payload, err := userWS.ReadMessage()
			require.NoError(t, err)
			var compact bytes.Buffer
			require.NoError(t, json.Compact(&compact, payload))
			payload = compact.Bytes()
			seen = append(seen, payload)
			if bytes.Contains(payload, []byte(want)) {
				return
			}
		}
	}
	// Verify the canonical session through HTTP before a fresh prompt on the
	// reconnected relay, and also require the pending creation result to arrive.
	var response struct {
		Commands *domain.SessionACPCommands `json:"commands"`
	}
	require.Eventually(t, func() bool {
		response = getJSON[struct {
			Commands *domain.SessionACPCommands `json:"commands"`
		}](t, fixture, base+"/"+sessionID+"/configuration", headers, http.StatusOK)
		return response.Commands != nil && len(response.Commands.AvailableCommands) == 1
	}, 15*time.Second, 100*time.Millisecond)
	require.NotNil(t, response.Commands)
	require.Len(t, response.Commands.AvailableCommands, 1)
	assert.Contains(t, string(response.Commands.AvailableCommands[0]), `"name":"inspect"`)
	readUntil(`"id":"create-1"`)
	writeRawWS(t, userWS, mustMarshalString(t, map[string]any{
		"jsonrpc": "2.0", "id": "prompt-1", "method": "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt":    []map[string]string{{"type": "text", "text": "pair test"}},
		},
	}))
	readUntil("fixture reply")
	readUntil(`"id":"prompt-1"`)
	docker("stop", "--time", "10", name)
	db, err := sql.Open(
		"sqlite3",
		"file:"+filepath.ToSlash(filepath.Join(stateDir, "paxd.db"))+"?mode=ro",
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var boundManagerID string
	require.NoError(t, db.QueryRow(`SELECT manager_session_id FROM acp_session_route
		WHERE connection_id = ? AND native_session_id = ?`, "pair-connection", "fixture-native-1").Scan(&boundManagerID))
	assert.Equal(
		t,
		sessionID,
		boundManagerID,
		"reconnect must retain the durable native session binding",
	)
	events, err := os.ReadFile(filepath.Join(workerDir, "events.jsonl"))
	require.NoError(t, err)
	counts := map[string]int{}
	for _, line := range bytes.Split(events, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event struct {
			Kind   string `json:"kind"`
			Method string `json:"method"`
		}
		require.NoError(t, json.Unmarshal(line, &event))
		if event.Kind == "request" {
			counts[event.Method]++
		}
	}
	assert.Equal(t, 1, counts["session/new"], "reconnect must not repeat session creation")
	assert.Equal(t, 1, counts["session/prompt"])
	assert.Equal(t, 1, counts["initialize"], "the ACP worker must survive the transport reconnect")
}
