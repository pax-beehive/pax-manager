//go:build !windows

package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This opt-in test uses the real Console, Manager API, node-control runner,
// SQLite command store and binary installer. Only release artifacts are fixtures.
func TestPaxlBrowserUpgrade(t *testing.T) {
	console := os.Getenv("PAXL_E2E_CONSOLE_DIR")
	nodeBinary := os.Getenv("PAXL_E2E_NODE_BINARY")
	if console == "" || nodeBinary == "" {
		t.Skip("set PAXL_E2E_CONSOLE_DIR and PAXL_E2E_NODE_BINARY")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	old := []byte("#!/bin/sh\nprintf '{\"version\":\"1.2.2\"}\\n'\n")
	next := []byte("#!/bin/sh\nprintf '{\"version\":\"1.2.3\"}\\n'\n")
	path := filepath.Join(root, "paxl")
	require.NoError(t, os.WriteFile(path, old, 0755))
	sum := sha256.Sum256(next)
	srv, registered := testNodeControlServer(t, "paxl-e2e@example.com")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/node/control", srv.handleNodeControlTunnel)
	var server *httptest.Server
	mux.HandleFunc(
		"/api/v1/public/artifacts/download",
		func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "paxl", r.URL.Query().Get("product"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).
				Encode(map[string]any{"code": 0, "data": map[string]any{"product": "paxl", "version": "1.2.3", "url": server.URL + "/fixture", "sha256": hex.EncodeToString(sum[:]), "size_bytes": len(next), "platform": "linux/arm64", "tags": []string{"stable"}}})
		},
	)
	mux.HandleFunc(
		"/fixture",
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(next) },
	)
	routes := srv.routes()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The isolated Console proxy requires a marker cookie; this test uses
		// the existing local-header identity fixture rather than an Access issuer.
		r.Header.Del("Cf-Access-Jwt-Assertion")
		r.Header.Del("Cookie")
		routes.ServeHTTP(w, r)
	}))
	server = httptest.NewServer(mux)
	defer server.Close()
	node := exec.CommandContext(
		ctx,
		nodeBinary,
		"-test.run=^TestPaxlBrowserE2ENode$",
		"-test.timeout=180s",
	)
	node.Env = append(
		os.Environ(),
		"PAXL_E2E_MANAGER="+server.URL,
		"PAXL_E2E_NODE="+registered.NodeID,
		"PAXL_E2E_KEY="+registered.APIKey,
		"PAXL_E2E_STATE="+root,
		"PAXD_PAXL_COMMAND="+path,
	)
	startPaxlE2EProcess(t, node, filepath.Join(root, "node.log"))
	waitNode(t, srv, registered.APIKey, func(node Node) bool { return len(node.Metadata) > 0 })
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
		filepath.Join(console, "scripts/paxl-upgrade-e2e.mjs"),
	)
	browser.Dir = console
	browser.Env = append(os.Environ(), "PAXL_E2E_CONSOLE="+base, "PAXL_E2E_NODE="+registered.NodeID)
	output, err := browser.CombinedOutput()
	require.NoError(t, err, string(output))
	installed, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, next, installed)
	t.Log(string(output))
}

func startPaxlE2EProcess(t *testing.T, cmd *exec.Cmd, logPath string) {
	t.Helper()
	log, err := os.Create(logPath)
	require.NoError(t, err)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = log
	cmd.Stderr = log
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = log.Close()
		if t.Failed() {
			raw, _ := os.ReadFile(logPath)
			t.Log(string(raw))
		}
	})
}
