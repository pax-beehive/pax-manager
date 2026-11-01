//go:build integration

package manager

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func TestPaxdSecretaryToolsGivenRealStdioAndStoredNodeKeyThenReadsAndPublishesExactSources(
	t *testing.T,
) {
	binary := os.Getenv("PAX_SECRETARY_TEST_PAXD_BINARY")
	if binary == "" {
		t.Skip("requires PAX_SECRETARY_TEST_PAXD_BINARY built from the matching paxd revision")
	}
	info, err := os.Stat(binary)
	require.NoError(t, err)
	require.False(t, info.IsDir())
	require.NotZero(t, info.Mode().Perm()&0111, "paxd fixture must be executable")
	srv, _ := testServer(t, "binary-secretary@example.test")
	fixture := testNodeAgent(t, srv, "binary-secretary@example.test")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	createConversationTestSession(t, srv, fixture, "source-session", "source-native")
	createConversationTestSession(t, srv, fixture, "related-session", "related-native")
	putSecretaryToolMessage(t, srv, fixture, "binary-source", "user", "Update our database plan.")
	related := domain.Message{
		MessageID:   "binary-related",
		AgentID:     fixture.agentID,
		SessionID:   "related-session",
		Role:        "assistant",
		MessageType: "agent_message_chunk",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &related))
	require.NoError(
		t,
		srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{MessageID: related.MessageID,
			PartType: domain.MessagePartText, Text: "Prior discussion.\n\nDatabase indexing is settled."}),
	)
	baseStore := srv.store.(canonicalSessionStore).Store
	sources := baseStore.(domain.SearchSourceStore)
	relatedDocument, err := sources.ReadSearchDocument(
		t.Context(),
		owner,
		"message",
		related.MessageID,
	)
	require.NoError(t, err)
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(
		srv.auth,
		sources,
		baseStore.(domain.SummaryStore),
		&workspaceTestBackend{
			docs: []domain.SearchDocument{relatedDocument},
		},
		[]byte(strings.Repeat("binary-receipt-key", 3)),
	)
	jobs := srv.secretaryStore
	_, err = jobs.UpdateSecretarySettings(
		t.Context(),
		owner,
		domain.SecretarySettingsUpdate{AgentID: fixture.agentID, Enabled: true},
	)
	require.NoError(t, err)
	claimed, err := jobs.ClaimSecretaryJobs(t.Context(), 10, time.Minute, 0)
	require.NoError(t, err)
	var job domain.SecretaryJob
	for _, candidate := range claimed {
		if candidate.SourceSessionID == "source-session" {
			job = candidate
		}
	}
	require.NotEmpty(t, job.LeaseToken)
	prompt, err := secretary.BuildPrompt(job)
	require.NoError(t, err)
	var calls atomic.Int64
	var wrongNodeKey atomic.Bool
	routes := srv.routes()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/secretary/tools") {
			calls.Add(1)
			if r.Header.Get("X-Pax-Key") != fixture.nodeAPIKey {
				wrongNodeKey.Store(true)
			}
		}
		routes.ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(
			server.URL,
			"http",
		)+"/api/v1/agent/tunnel?agent_id="+fixture.agentID,
		http.Header{"X-Pax-Key": []string{fixture.nodeAPIKey}},
	)
	require.NoError(t, err)
	defer func() { _ = ws.Close() }()
	completeMockAgentReconcile(t, ws, fixture.agentID, 1)
	waitACPTunnelAgentRegistered(t, srv, fixture.agentID, "")
	type outcome struct {
		result secretary.ExecutionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := srv.executeSecretaryAgent(
			t.Context(),
			secretary.ExecutionRequest{OwnerID: owner, AgentID: fixture.agentID,
				RunID: job.RunID, Prompt: prompt, Job: job, BeforePrompt: func(ctx context.Context, agentID, sessionID string) error {
					return jobs.RegisterSecretaryInternalSession(ctx, job, agentID, sessionID)
				}},
		)
		done <- outcome{result: result, err: runErr}
	}()
	created, createRequest := readMockACPRequest(t, ws, "session/new")
	environment := secretaryPaxdEnvironment(t, createRequest.Params)
	capability := environment["PAX_SECRETARY_CAPABILITY"]
	require.NotEmpty(t, capability)
	assert.Equal(t, fixture.agentID, environment["PAX_AGENT_ID"])
	assert.Equal(t, secretary.ExecutionSessionID(owner, job.RunID), environment["PAX_SESSION_ID"])
	writeMockACPResponse(
		t,
		ws,
		created.QueueID,
		1,
		createRequest.ID,
		json.RawMessage(`{"sessionId":"binary-secretary-native"}`),
	)
	promptEnvelope, promptRequest := readMockACPRequest(t, ws, "session/prompt")
	home := t.TempDir()
	process := startSecretaryPaxd(t, binary, home, environment)
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	process.rpc(t, "tools/list", map[string]any{}, &listed)
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	assert.ElementsMatch(
		t,
		[]string{"workspace_search", "workspace_read", "workspace_expand", "workspace_history"},
		names,
	)
	bootstrap := process.tool(t, "workspace_search", map[string]any{"query": "bootstrap"})
	assertSecretaryPaxdFailure(t, bootstrap, "unavailable")
	assert.Zero(t, calls.Load(), "unconfigured paxd must not guess a Manager identity")
	seedSecretaryPaxdCredentials(t, home, server.URL, fixture)
	searchResult := process.tool(
		t,
		"workspace_search",
		map[string]any{"query": "database", "limit": 2},
	)
	require.False(t, searchResult.IsError)
	var page userapi.WorkspaceSearchPage
	require.NoError(t, json.Unmarshal(searchResult.StructuredContent, &page))
	require.Len(t, page.Hits, 1)
	assert.Equal(t, related.MessageID, page.Hits[0].Ref.ID)
	readResult := process.tool(t, "workspace_read", map[string]any{"ref": page.Hits[0].Ref})
	require.False(t, readResult.IsError)
	var read userapi.WorkspaceMessageRead
	require.NoError(t, json.Unmarshal(readResult.StructuredContent, &read))
	assert.Equal(t, "Database indexing is settled.", read.Text)
	require.NotEmpty(t, read.ReadReceipt)
	expanded := process.tool(
		t,
		"workspace_expand",
		map[string]any{"message_id": read.MessageID, "content_hash": read.Ref.ContentHash,
			"canonicalization_version": read.Ref.CanonicalizationVersion, "view": "full"},
	)
	require.False(t, expanded.IsError)
	var full userapi.WorkspaceMessageRead
	require.NoError(t, json.Unmarshal(expanded.StructuredContent, &full))
	assert.Equal(t, relatedDocument.Body, full.Text)
	assert.Equal(t, read.Ref, full.Ref)
	historyResult := process.tool(
		t,
		"workspace_history",
		map[string]any{"session_id": "related-session", "limit": 2},
	)
	require.False(t, historyResult.IsError)
	var history secretaryHistoryPage
	require.NoError(t, json.Unmarshal(historyResult.StructuredContent, &history))
	require.Len(t, history.Messages, 1)
	assert.Equal(t, related.MessageID, history.Messages[0].MessageID)
	assert.NotEmpty(t, history.Messages[0].ReadReceipt)
	assert.False(t, wrongNodeKey.Load(), "every bridge request must use the stored node key")
	assert.GreaterOrEqual(t, calls.Load(), int64(4))
	forgedEnvironment := make(map[string]string, len(environment))
	for key, value := range environment {
		forgedEnvironment[key] = value
	}
	forgedEnvironment["PAX_SECRETARY_CAPABILITY"] = "forged-capability"
	forged := startSecretaryPaxd(t, binary, home, forgedEnvironment)
	beforeForged := calls.Load()
	forgedRead := forged.tool(t, "workspace_read", map[string]any{"ref": read.Ref})
	assertSecretaryPaxdFailure(t, forgedRead, "unauthenticated")
	assert.Equal(
		t,
		beforeForged+1,
		calls.Load(),
		"forged capability must reach and be rejected by Manager",
	)
	assert.NotContains(t, string(forgedRead.StructuredContent), capability)
	assert.NotContains(t, string(forgedRead.StructuredContent), fixture.nodeAPIKey)
	ref := domain.MessageVersionRef{MessageID: read.MessageID, ContentHash: read.Ref.ContentHash,
		CanonicalizationVersion: read.Ref.CanonicalizationVersion, ReadReceipt: read.ReadReceipt}
	output, err := json.Marshal(
		map[string]any{"title": "Database plan", "body": "Database indexing is settled.",
			"related_sources": []domain.MessageVersionRef{
				ref,
			}, "claims": []map[string]any{{"text": "Database indexing is settled.",
				"evidence": []map[string]string{
					{"message_id": read.MessageID, "quote": read.Text},
				}}}},
	)
	require.NoError(t, err)
	writeMockACPChunk(
		t,
		ws,
		promptEnvelope.QueueID,
		2,
		environment["PAX_SESSION_ID"],
		string(output),
	)
	writeMockACPResponse(
		t,
		ws,
		promptEnvelope.QueueID,
		3,
		promptRequest.ID,
		json.RawMessage(`{"stopReason":"end_turn"}`),
	)
	var result secretary.ExecutionResult
	select {
	case got := <-done:
		require.NoError(t, got.err)
		result = got.result
	case <-time.After(10 * time.Second):
		t.Fatal("Secretary execution did not complete after the real MCP reads")
	}
	require.Len(t, result.Reads.Messages, 1)
	assert.Equal(t, read.Text, result.Reads.Messages[0].Text)
	publication, err := secretary.BuildPublicationWithReads(job, result.Text, result.Reads)
	require.NoError(t, err)
	job.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
	version, err := jobs.PublishSecretaryJob(t.Context(), job, publication)
	require.NoError(t, err)
	require.Len(
		t,
		version.Coverage,
		2,
		"the source delta and selected related source are frozen together",
	)
	assert.Equal(t, read.Text, version.Claims[0].Evidence[0].Quote)
	resolved, err := baseStore.(domain.SummaryStore).ReadMessageVersion(t.Context(), owner, ref)
	require.NoError(t, err)
	assert.Equal(t, relatedDocument.Body, resolved.Body)
	revokedRead := process.tool(t, "workspace_read", map[string]any{"ref": read.Ref})
	assertSecretaryPaxdFailure(t, revokedRead, "unauthenticated")
	assert.NotContains(t, string(revokedRead.StructuredContent), capability)
	assert.NotContains(t, string(revokedRead.StructuredContent), fixture.nodeAPIKey)
}

type secretaryPaxdToolResult struct {
	IsError           bool            `json:"isError"`
	StructuredContent json.RawMessage `json:"structuredContent"`
}

func assertSecretaryPaxdFailure(t *testing.T, result secretaryPaxdToolResult, code string) {
	t.Helper()
	require.True(t, result.IsError)
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(result.StructuredContent, &failure))
	assert.Equal(t, code, failure.Error.Code)
}

type secretaryPaxdProcess struct {
	stdin  io.WriteCloser
	lines  chan []byte
	done   chan error
	nextID int
	stderr bytes.Buffer
}

func startSecretaryPaxd(
	t *testing.T,
	binary, home string,
	environment map[string]string,
) *secretaryPaxdProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	process := &secretaryPaxdProcess{lines: make(chan []byte, 8), done: make(chan error, 1)}
	command := exec.CommandContext(ctx, binary, "mcp", "conversation", "serve")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "HOME" || strings.HasPrefix(name, "PAX_") || strings.HasPrefix(name, "PAXD_") ||
			strings.Contains(strings.ToLower(name), "proxy") {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(
		command.Env,
		"HOME="+home,
		"PAXD_DB_PATH="+filepath.Join(home, ".paxd", "paxd.db"),
		"PAX_USER_API_KEY=",
	)
	for name, value := range environment {
		command.Env = append(command.Env, name+"="+value)
	}
	var err error
	process.stdin, err = command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	command.Stderr = &process.stderr
	require.NoError(t, command.Start())
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			process.lines <- append([]byte(nil), scanner.Bytes()...)
		}
		close(process.lines)
	}()
	go func() { process.done <- command.Wait() }()
	t.Cleanup(func() {
		_ = process.stdin.Close()
		select {
		case err := <-process.done:
			assert.NoError(t, err, "paxd stdio must close cleanly")
		case <-time.After(3 * time.Second):
			cancel()
			<-process.done
		}
		cancel()
	})
	var initialized map[string]any
	process.rpc(
		t,
		"initialize",
		map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "secretary-acceptance", "version": "1"},
		},
		&initialized,
	)
	assert.Equal(t, "2024-11-05", initialized["protocolVersion"])
	return process
}

func (p *secretaryPaxdProcess) rpc(t *testing.T, method string, params, result any) {
	t.Helper()
	p.nextID++
	request, err := json.Marshal(
		map[string]any{"jsonrpc": "2.0", "id": p.nextID, "method": method, "params": params},
	)
	require.NoError(t, err)
	_, err = p.stdin.Write(append(request, '\n'))
	require.NoError(t, err)
	select {
	case line, ok := <-p.lines:
		require.True(t, ok, "paxd exited before answering MCP request")
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		require.NoError(t, json.Unmarshal(line, &response))
		require.Equal(t, p.nextID, response.ID)
		require.Empty(t, response.Error, "MCP request must succeed at the protocol layer")
		require.NoError(t, json.Unmarshal(response.Result, result))
	case <-time.After(10 * time.Second):
		t.Fatal("paxd MCP response timed out")
	}
}

func (p *secretaryPaxdProcess) tool(
	t *testing.T,
	name string,
	arguments any,
) secretaryPaxdToolResult {
	t.Helper()
	var result secretaryPaxdToolResult
	p.rpc(t, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result)
	return result
}

func secretaryPaxdEnvironment(t *testing.T, params json.RawMessage) map[string]string {
	t.Helper()
	var request struct {
		MCPServers []struct {
			Name string                         `json:"name"`
			Args []string                       `json:"args"`
			Env  []struct{ Name, Value string } `json:"env"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(params, &request))
	for _, server := range request.MCPServers {
		environment := map[string]string{}
		for _, entry := range server.Env {
			environment[entry.Name] = entry.Value
		}
		if environment["PAX_SECRETARY_CAPABILITY"] != "" {
			assert.Equal(t, []string{"mcp", "conversation", "serve"}, server.Args)
			return environment
		}
	}
	t.Fatal("Secretary session/new did not include its execution-scoped MCP environment")
	return nil
}

func seedSecretaryPaxdCredentials(
	t *testing.T,
	home, managerURL string,
	fixture conversationTestFixture,
) {
	t.Helper()
	keyPath := filepath.Join(home, ".paxd", "secrets", "node_key")
	require.NoError(t, os.MkdirAll(filepath.Dir(keyPath), 0700))
	require.NoError(t, os.WriteFile(keyPath, []byte(fixture.nodeAPIKey+"\n"), 0600))
	db, err := sql.Open("sqlite3", filepath.Join(home, ".paxd", "paxd.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO remote(id,name,cloud_api_url,node_id,cloud_api_key_ref,enabled,created_at,updated_at)
 VALUES('fixture','Fixture',?,?,?,1,?,?)`,
		managerURL,
		fixture.nodeID,
		"file:"+keyPath,
		now,
		now,
	)
	require.NoError(t, err)
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO agent_connection(id,remote_id,name,cloud_agent_id,instance_id,agent_type,harness,command_json,enabled,desired_state,created_at,updated_at)
 VALUES('fixture_conn','fixture','Secretary',?,'fixture_instance','codex','codex','["true"]',1,'running',?,?)`,
		fixture.agentID,
		now,
		now,
	)
	require.NoError(t, err)
}
