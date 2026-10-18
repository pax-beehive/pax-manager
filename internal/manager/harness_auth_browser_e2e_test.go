//go:build !windows

package manager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This opt-in test uses the real Console, Manager API, node-control runner,
// paxl and native process boundary. Vendor programs/SDKs and accounts are fixtures.
func TestHarnessBrowserAuthentication(t *testing.T) {
	console := os.Getenv("PAXL_E2E_CONSOLE_DIR")
	nodeBinary := os.Getenv("PAXL_E2E_NODE_BINARY")
	if console == "" || nodeBinary == "" {
		t.Skip("set PAXL_E2E_CONSOLE_DIR and PAXL_E2E_NODE_BINARY")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	root := t.TempDir()
	paxl := os.Getenv("HARNESS_E2E_PAXL_BINARY")
	require.NotEmpty(t, paxl)
	writeAuthBrowserFixtures(t, root)
	srv, registered := testNodeControlServer(t, "paxl-e2e@example.com")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/node/control", srv.handleNodeControlTunnel)
	mux.HandleFunc("/_fixture/disconnect", func(w http.ResponseWriter, _ *http.Request) {
		connection, err := srv.nodeControls.Connection(registered.NodeID)
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_ = connection.ws.Close()
		w.WriteHeader(http.StatusNoContent)
	})
	routes := srv.routes()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The isolated Console proxy requires a marker cookie; this test uses
		// the existing local-header identity fixture rather than an Access issuer.
		r.Header.Del("Cf-Access-Jwt-Assertion")
		r.Header.Del("Cookie")
		routes.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	node := exec.CommandContext(
		ctx,
		nodeBinary,
		"-test.run=^TestHarnessAuthBrowserE2ENode$",
		"-test.timeout=300s",
	)
	node.Env = append(
		os.Environ(),
		"PAXL_E2E_MANAGER="+server.URL,
		"PAXL_E2E_NODE="+registered.NodeID,
		"PAXL_E2E_KEY="+registered.APIKey,
		"PAXL_E2E_STATE="+root,
		"PAXD_PAXL_COMMAND="+paxl,
		"CLAUDE_CODE_EXECUTABLE="+filepath.Join(root, "initial", "bin", "claude"),
		"CODEX_PATH="+filepath.Join(root, "initial", "bin", "codex"),
		"PI_EXECUTABLE="+filepath.Join(root, "pi-sdk", "dist", "cli.js"),
		"PI_CODING_AGENT_DIR="+filepath.Join(root, "pi-state"),
		"PATH="+filepath.Join(
			root,
			"initial",
			"bin",
		)+string(
			os.PathListSeparator,
		)+os.Getenv(
			"PATH",
		),
	)
	startPaxlE2EProcess(t, node, filepath.Join(root, "node.log"))
	require.Eventually(
		t,
		func() bool { return len(getNode(t, srv, registered.APIKey).Metadata) > 0 },
		30*time.Second,
		100*time.Millisecond,
	)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	nextServer := exec.CommandContext(
		ctx,
		"node",
		filepath.Join(console, "node_modules/next/dist/bin/next"),
		"dev",
		"--webpack",
		"--hostname",
		"127.0.0.1",
		"--port",
		fmt.Sprint(port),
	)
	nextServer.Dir = console
	nextServer.Env = append(
		os.Environ(),
		"PAX_MANAGER_URL="+server.URL,
		"PAX_CF_AUTHORIZATION=isolated-e2e",
		"NEXT_PUBLIC_PAX_BROWSER_REGIONS_ENABLED=false",
		"PAX_BROWSER_REGIONS_ENABLED=false",
	)
	startPaxlE2EProcess(t, nextServer, filepath.Join(root, "console.log"))
	require.Eventually(t, func() bool {
		response, err := http.Get(base + "/api/pax/api/v1/health")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == 200
	}, 90*time.Second, time.Second)
	browser := exec.CommandContext(
		ctx,
		"node",
		filepath.Join(console, "scripts/harness-auth-e2e.mjs"),
	)
	browser.Dir = console
	browser.Env = append(
		os.Environ(),
		"PAXL_E2E_CONSOLE="+base,
		"PAXL_E2E_MANAGER="+server.URL,
		"PAXL_E2E_NODE="+registered.NodeID,
	)
	output, err := browser.CombinedOutput()
	require.NoError(t, err, string(output))
	for _, item := range []string{"daemon.db", "daemon.db-wal"} {
		data, err := os.ReadFile(filepath.Join(root, item))
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		require.NotContains(t, string(data), "fixture-private-code")
		require.NotContains(t, string(data), "fixture-api-key")
	}
	launches, err := os.ReadFile(filepath.Join(root, "claude-launches"))
	require.NoError(t, err)
	require.Equal(
		t,
		"launch\n",
		string(launches),
		"reload must reuse the same native login process",
	)
	stored, err := os.ReadFile(filepath.Join(root, "pi-sdk", "stored-test-key"))
	require.NoError(t, err)
	require.Equal(t, "fixture-api-key", string(stored))

	t.Log(string(output))
}
