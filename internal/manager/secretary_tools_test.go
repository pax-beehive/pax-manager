package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretaryToolsStore struct {
	domain.SecretaryStore
	active bool
	err    error
}

func putSecretaryToolMessage(
	t *testing.T,
	srv *Service,
	fixture conversationTestFixture,
	id, role, text string,
) {
	t.Helper()
	message := domain.Message{
		MessageID:   id,
		AgentID:     fixture.agentID,
		SessionID:   "source-session",
		Role:        role,
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		MessageType: "agent_message_chunk",
	}
	if role == "user" {
		message.MessageType = "user_message"
		message.Direction = domain.MessageDirectionUserToAgent
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(
		t,
		srv.store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{MessageID: id, PartType: domain.MessagePartText, Text: text},
		),
	)
}

func secretaryToolCall(
	t *testing.T,
	srv *Service,
	fixture conversationTestFixture,
	capability, tool, arguments string,
) (any, error) {
	t.Helper()
	return srv.callSecretaryTool(
		t.Context(),
		domain.Node{NodeID: fixture.nodeID},
		fixture.agentID,
		secretaryToolRequest{
			SessionID:     "sess_secretary_execution",
			Capability:    capability,
			Tool:          tool,
			ArgumentsJSON: arguments,
		},
	)
}

func (s *secretaryToolsStore) SecretaryJobActive(
	context.Context,
	domain.SecretaryJob,
) (bool, error) {
	return s.active, s.err
}

func TestSecretaryToolsGivenHistoryThenReusesPaginationAndExcludesHiddenMessages(t *testing.T) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	putSecretaryToolMessage(t, srv, fixture, "history-user", "user", "Question")
	putSecretaryToolMessage(t, srv, fixture, "history-assistant", "assistant", "First.\n\nLast.")
	putSecretaryToolMessage(t, srv, fixture, "history-hidden", "tool", "Hidden output")
	result, err := secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"history",
		`{"session_id":"source-session","limit":2}`,
	)
	require.NoError(t, err)
	page := result.(secretaryHistoryPage)
	for _, message := range page.Messages {
		assert.NotEqual(t, "history-hidden", message.MessageID)
		assert.NotContains(t, message.Text, "Hidden")
	}
	assert.Greater(t, page.Pagination.HeadSeq, int64(0))
	assert.NotEmpty(t, page.Messages)
	assert.NotEmpty(t, srv.secretaryToolsReads(capability).Messages)
	result, err = secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"history",
		`{"session_id":"source-session","after_seq":1}`,
	)
	require.NoError(t, err)
	assert.IsType(t, secretaryHistoryPage{}, result)
	for _, arguments := range []string{
		`{}`, `{"session_id":"source-session","limit":51}`, `{"session_id":"source-session","limit":-1}`,
		`{"session_id":"source-session","before_seq":1,"after_seq":1}`, `{"session_id":"source-session","after_seq":-1}`,
		`{"session_id":"source-session","before_seq":-1}`, `{"session_id":"source-session","owner_user_id":"foreign"}`, `null null`,
		`{"session_id":"missing"}`, `{"session_id":"sess_secretary_execution"}`,
	} {
		t.Run(arguments, func(t *testing.T) {
			_, err := secretaryToolCall(t, srv, fixture, capability, "history", arguments)
			require.Error(t, err)
		})
	}
}

func TestSecretaryToolsGivenInvalidArgumentsOrUnsupportedToolThenReturnsSafeErrors(t *testing.T) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	for _, test := range []struct{ tool, arguments string }{
		{"publish", `{}`}, {"search", `{`}, {"search", `{"query":"anything","owner_user_id":"foreign"}`},
		{"search", `{"query":"anything","limit":10000}`}, {"read", `{`}, {"read", `{}`},
		{"read", `{"ref":{"kind":"message","id":"missing"}}`}, {"history", `{`},
		{"read", strings.Repeat(" ", 64*1024+1)}, {"search", `{} {}`},
	} {
		t.Run(test.tool+test.arguments[:min(80, len(test.arguments))], func(t *testing.T) {
			_, err := secretaryToolCall(t, srv, fixture, capability, test.tool, test.arguments)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), capability)
		})
	}
}

type secretaryToolsBackend struct {
	query domain.SearchQuery
	after func()
	docs  []domain.SearchDocument
}

func (b *secretaryToolsBackend) EnsureIndex(context.Context) error                  { return nil }
func (b *secretaryToolsBackend) Index(context.Context, domain.SearchDocument) error { return nil }

func (b *secretaryToolsBackend) Search(
	_ context.Context,
	query domain.SearchQuery,
) (domain.SearchPage, error) {
	b.query = query
	if b.after != nil {
		b.after()
	}
	return domain.SearchPage{Hits: b.docs}, nil
}

func TestSecretaryToolsGivenSearchThenUsesJobOwnerAndRechecksLeaseBeforeReturning(t *testing.T) {
	srv, fixture, job, _, capability := secretaryToolsFixture(t)
	backend := &secretaryToolsBackend{}
	sources := srv.store.(canonicalSessionStore).Store.(domain.SearchSourceStore)
	summaries := srv.store.(canonicalSessionStore).Store.(domain.SummaryStore)
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(
		nil,
		sources,
		summaries,
		backend,
		[]byte(strings.Repeat("r", 32)),
	)
	result, err := secretaryToolCall(t, srv, fixture, capability, "search", `{"query":"anything"}`)
	require.NoError(t, err)
	assert.IsType(t, userapi.WorkspaceSearchPage{}, result)
	assert.Equal(t, job.OwnerUserID, backend.query.OwnerUserID)
	assert.Equal(t, 20, backend.query.Limit)
	backend.after = func() { srv.secretaryStore.(*secretaryToolsStore).active = false }
	_, err = secretaryToolCall(t, srv, fixture, capability, "search", `{"query":"anything"}`)
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	assert.Empty(t, srv.secretaryToolsReads(capability).Messages)
}

func TestSecretaryToolsGivenRevocationCancellationOrInactiveLeaseThenCannotRead(t *testing.T) {
	srv, fixture, job, agent, _ := secretaryToolsFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	capability, cleanup, err := srv.registerSecretaryTools(
		ctx,
		job,
		agent,
		"sess_secretary_execution",
	)
	require.NoError(t, err)
	cancel()
	_, err = secretaryToolCall(t, srv, fixture, capability, "read", `{}`)
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	cleanup()
	cleanup()
	assert.Empty(t, srv.secretaryToolsReads(capability).Messages)
	_, err = secretaryToolCall(t, srv, fixture, capability, "read", `{}`)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, _, err = srv.registerSecretaryTools(ctx, job, agent, "sess_secretary_execution")
	require.ErrorIs(t, err, context.Canceled)
	for _, invalid := range []domain.Agent{{}, {AgentID: agent.AgentID, OwnerUserID: "foreign", NodeID: agent.NodeID}} {
		_, _, err = srv.registerSecretaryTools(
			t.Context(),
			job,
			invalid,
			"sess_secretary_execution",
		)
		assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	}
	_, _, err = srv.registerSecretaryTools(t.Context(), job, agent, "normal-session")
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	srv.secretaryStore.(*secretaryToolsStore).active = false
	_, _, err = srv.registerSecretaryTools(t.Context(), job, agent, "sess_secretary_execution")
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	srv.secretaryStore.(*secretaryToolsStore).err = errors.New("storage unavailable")
	_, _, err = srv.registerSecretaryTools(t.Context(), job, agent, "sess_secretary_execution")
	assert.EqualError(t, err, "storage unavailable")
	srv.secretaryStore = nil
	_, _, err = srv.registerSecretaryTools(t.Context(), job, agent, "sess_secretary_execution")
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
}

func TestSecretaryToolsGivenLedgerThenPinsVersionsDeduplicatesAndEnforcesBudget(t *testing.T) {
	grant := &secretaryToolGrant{
		reads:     map[string]userapi.WorkspaceMessageRead{},
		summaries: map[domain.SummaryRef]bool{},
	}
	read := userapi.WorkspaceMessageRead{
		MessageID:   "m",
		Text:        "body",
		View:        "last",
		ReadReceipt: "receipt",
		Ref:         userapi.SearchRef{ContentHash: "hash", CanonicalizationVersion: "v1"},
		Context: []userapi.WorkspaceMessageRead{
			{MessageID: "m2", Text: "context", ReadReceipt: "receipt2"},
		},
	}
	require.NoError(t, grant.record(read))
	require.NoError(t, grant.record(read))
	assert.Equal(t, len("bodycontext"), grant.readBytes)
	assert.Len(t, grant.reads, 2)
	require.NoError(t, grant.record(domain.SummaryVersion{SummaryID: "summary", Revision: 2}))
	require.NoError(t, grant.record(domain.SummaryVersion{SummaryID: "summary", Revision: 1}))
	srv := &Service{}
	srv.secretaryTools.Store("cap", grant)
	ledger := srv.secretaryToolsReads("cap")
	assert.Len(t, ledger.Messages, 2)
	assert.Equal(
		t,
		[]domain.SummaryRef{
			{SummaryID: "summary", Revision: 1},
			{SummaryID: "summary", Revision: 2},
		},
		ledger.Summaries,
	)
	grant.readBytes = 4 * 1024 * 1024
	require.Error(t, grant.record(userapi.WorkspaceMessageRead{ReadReceipt: "new", Text: "extra"}))
	grant.readBytes = 0
	for i := 0; i < 100; i++ {
		grant.summaries[domain.SummaryRef{SummaryID: "other", Revision: int64(i)}] = true
	}
	require.Error(t, grant.record(domain.SummaryVersion{SummaryID: "new", Revision: 1}))
	grant.closed = true
	assert.ErrorIs(t, grant.record(read), domain.ErrSecretaryFenced)
}

func TestSecretaryToolsGivenSharedReceiptKeyThenUserReadsCanBeVerified(t *testing.T) {
	srv, fixture, job, _, capability := secretaryToolsFixture(t)
	putSecretaryToolMessage(t, srv, fixture, "receipt-source", "user", "Visible source")
	data, err := secretaryToolCall(
		t,
		srv,
		fixture,
		capability,
		"read",
		`{"ref":{"kind":"message","id":"receipt-source"}}`,
	)
	require.NoError(t, err)
	read := data.(userapi.WorkspaceMessageRead)
	verified, err := srv.workspaceSearch.VerifyReadReceipt(
		t.Context(),
		job.OwnerUserID,
		domain.MessageVersionRef{
			MessageID: read.MessageID, ContentHash: read.Ref.ContentHash, CanonicalizationVersion: read.Ref.CanonicalizationVersion},
		read.ReadReceipt,
	)
	require.NoError(t, err)
	assert.Equal(t, read.Text, verified.Text)
	_, err = srv.workspaceSearch.WithPrincipal(secretaryToolPrincipal{owner: "foreign"}).
		Read(t.Context(), auth.RequestMetadata{},
			userapi.WorkspaceReadRequest{Ref: read.Ref})
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSecretaryToolsGivenNodeHTTPRouteThenRequiresBothNodeKeyAndExecutionCapability(
	t *testing.T,
) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	putSecretaryToolMessage(t, srv, fixture, "http-source", "user", "Trusted original")
	body, err := json.Marshal(
		secretaryToolRequest{SessionID: "sess_secretary_execution", Capability: capability,
			Tool: "read", ArgumentsJSON: `{"ref":{"kind":"message","id":"http-source"}}`},
	)
	require.NoError(t, err)
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"active", string(body), http.StatusOK},
		{"bad body", `{`, http.StatusBadRequest},
		{"unknown capability", `{"session_id":"sess_secretary_execution","capability":"invalid","tool":"read","arguments_json":"{}"}`, http.StatusUnauthorized},
		{"spoofed user", `{"owner_user_id":"foreign"}`, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/node/agents/"+fixture.agentID+"/secretary/tools",
				strings.NewReader(test.body),
			)
			request.Header.Set("X-Pax-Key", fixture.nodeAPIKey)
			request.Header.Set("X-User-Email", "foreign@example.test")
			request.Header.Set("X-Pax-User-ID", "foreign")
			setJSON(request)
			recorder := httptest.NewRecorder()
			srv.routes().ServeHTTP(recorder, request)
			assert.Equal(t, test.status, recorder.Code, recorder.Body.String())
			assert.NotContains(t, recorder.Body.String(), capability)
			if test.status == http.StatusOK {
				assert.Contains(t, recorder.Body.String(), "Trusted original")
			}
		})
	}
}

func secretaryToolsFixture(
	t *testing.T,
) (*Service, conversationTestFixture, domain.SecretaryJob, domain.Agent, string) {
	t.Helper()
	srv, _ := testServer(t, "secretary-tools@example.test")
	fixture := testNodeAgent(t, srv, "secretary-tools@example.test")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	agent, err := srv.store.GetAgent(t.Context(), principal, fixture.agentID)
	require.NoError(t, err)
	srv.secretaryStore = &secretaryToolsStore{active: true}
	createConversationTestSession(t, srv, fixture, "source-session", "source-native")
	job := domain.SecretaryJob{
		OwnerUserID:      principal.User.UserID,
		AgentID:          agent.AgentID,
		SourceAgentID:    agent.AgentID,
		SourceSessionID:  "source-session",
		LeaseToken:       "lease",
		RunID:            "run",
		SettingsRevision: 1,
	}
	capability, cleanup, err := srv.registerSecretaryTools(
		t.Context(),
		job,
		agent,
		"sess_secretary_execution",
	)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	return srv, fixture, job, agent, capability
}

func TestSecretaryToolsGivenActiveRunWhenReadingThenPinsCanonicalReceiptAndOwner(t *testing.T) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	message := domain.Message{
		MessageID:   "tool-source",
		AgentID:     fixture.agentID,
		SessionID:   "source-session",
		Role:        "assistant",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		MessageType: "agent_message_chunk",
		CreatedAt:   time.Now(),
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(
		t,
		srv.store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{
				MessageID: message.MessageID,
				PartType:  domain.MessagePartText,
				Text:      "First paragraph.\n\nLast paragraph.",
			},
		),
	)
	data, err := srv.callSecretaryTool(
		t.Context(),
		domain.Node{NodeID: fixture.nodeID},
		fixture.agentID,
		secretaryToolRequest{
			SessionID: "sess_secretary_execution", Capability: capability, Tool: "read", ArgumentsJSON: `{"ref":{"kind":"message","id":"tool-source"}}`,
		},
	)
	require.NoError(t, err)
	read := data.(userapi.WorkspaceMessageRead)
	assert.Equal(t, "Last paragraph.", read.Text)
	assert.NotEmpty(t, read.ReadReceipt)
	ledger := srv.secretaryToolsReads(capability)
	require.Len(t, ledger.Messages, 1)
	assert.Equal(t, read.Text, ledger.Messages[0].Text)
	assert.Equal(t, read.ReadReceipt, ledger.Messages[0].ReadReceipt)
}

func TestSecretaryToolsGivenMissingOrMismatchedCapabilityThenRejects(t *testing.T) {
	srv, fixture, _, _, capability := secretaryToolsFixture(t)
	valid := secretaryToolRequest{
		SessionID:     "sess_secretary_execution",
		Capability:    capability,
		Tool:          "search",
		ArgumentsJSON: `{"query":"anything"}`,
	}
	for _, test := range []struct {
		name            string
		nodeID, agentID string
		request         secretaryToolRequest
	}{
		{"unknown capability", fixture.nodeID, fixture.agentID, secretaryToolRequest{Capability: "unknown"}},
		{"wrong node", "foreign-node", fixture.agentID, valid},
		{"wrong agent", fixture.nodeID, "foreign-agent", valid},
		{"wrong session", fixture.nodeID, fixture.agentID, secretaryToolRequest{SessionID: "foreign", Capability: capability}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := srv.callSecretaryTool(
				t.Context(),
				domain.Node{NodeID: test.nodeID},
				test.agentID,
				test.request,
			)
			require.Error(t, err)
		})
	}
	_, err := srv.callSecretaryTool(
		t.Context(),
		domain.Node{NodeID: fixture.nodeID},
		fixture.agentID,
		valid,
	)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), capability)
	assert.NotEmpty(t, capability)
	assert.GreaterOrEqual(t, len(capability), 43)
	encoded, err := json.Marshal(valid)
	require.NoError(t, err)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/agents/"+fixture.agentID+"/secretary/tools",
		strings.NewReader(string(encoded)),
	)
	response := httptest.NewRecorder()
	srv.routes().ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
}
