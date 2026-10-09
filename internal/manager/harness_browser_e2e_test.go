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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This opt-in test uses the real Console, Manager API, node-control runner,
// SQLite command store and binary installer. Only release artifacts are fixtures.
func TestHarnessBrowserUpgrade(t *testing.T) {
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
	writeHarnessBrowserPackages(t, root)
	srv, registered := testNodeControlServer(t, "paxl-e2e@example.com")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/node/control", srv.handleNodeControlTunnel)
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
		"-test.run=^TestHarnessBrowserE2ENode$",
		"-test.timeout=300s",
	)
	node.Env = append(
		os.Environ(),
		"PAXL_E2E_MANAGER="+server.URL,
		"PAXL_E2E_NODE="+registered.NodeID,
		"PAXL_E2E_KEY="+registered.APIKey,
		"PAXL_E2E_STATE="+root,
		"PAXD_PAXL_COMMAND="+paxl,
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
		filepath.Join(console, "scripts/harness-upgrade-e2e.mjs"),
	)
	browser.Dir = console
	browser.Env = append(os.Environ(), "PAXL_E2E_CONSOLE="+base, "PAXL_E2E_NODE="+registered.NodeID)
	output, err := browser.CombinedOutput()
	require.NoError(t, err, string(output))
	for _, bin := range []string{"claude", "codex", "pi", "claude-agent-acp", "codex-acp", "pi-acp"} {
		target, err := filepath.EvalSymlinks(filepath.Join(root, "initial", "bin", bin))
		require.NoError(t, err)
		require.Contains(t, target, ".paxl-harness-versions")
		version, err := exec.Command(filepath.Join(root, "initial", "bin", bin), "--version").
			CombinedOutput()
		require.NoError(t, err)
		require.Contains(t, string(version), "1.2.3")
	}
	queries, err := os.ReadFile(filepath.Join(root, "initial", "bin", "latest-queries"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"@anthropic-ai/claude-code@latest", "@openai/codex@latest", "@earendil-works/pi-coding-agent@latest",
		"@agentclientprotocol/claude-agent-acp@latest", "@agentclientprotocol/codex-acp@latest", "@ccgv2/pi-acp@latest",
	}, strings.Fields(string(queries)), "each blank-version command must resolve latest exactly once, including after reload")
	t.Log(string(output))
}
