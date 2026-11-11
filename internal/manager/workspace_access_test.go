package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func workspaceAccessFixture(
	t *testing.T,
	approvalMode string,
) (*Service, conversationTestFixture, *secretaryToolsBackend) {
	t.Helper()
	srv, _ := testServer(t, "workspace-access@example.test")
	fixture := testNodeAgent(t, srv, "workspace-access@example.test")
	paxConfig := domain.SessionPaxConfig{ApprovalMode: approvalMode}
	if approvalMode == domain.SessionApprovalModeAutoApproveAll {
		paxConfig.PermissionChoiceID = domain.PermissionChoicePAXAutoApprove
	}
	createConversationTestSessionWithPaxConfig(
		t,
		srv,
		fixture,
		"caller-session",
		"caller-native",
		domain.SessionPaxConfig{ApprovalMode: domain.SessionApprovalModeManual},
	)
	// Register a real Manager dispatch before enabling automatic approval.
	require.NoError(
		t,
		srv.prepareTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-one",
			nil,
		),
	)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	_, err := srv.store.UpdateNodeAgentSession(t.Context(), principal, domain.UpdateSessionRequest{
		NodeID: fixture.nodeID, AgentID: fixture.agentID, SessionID: "caller-session", PaxConfig: paxConfig,
	})
	require.NoError(t, err)
	createConversationTestSession(t, srv, fixture, "source-session", "source-native")
	setWorkspaceAccessTurn(t, srv, fixture, "turn-one", domain.RuntimeLifecycleRunning)
	backend := &secretaryToolsBackend{}
	base := srv.store.(canonicalSessionStore).Store
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(
		nil,
		base.(domain.SearchSourceStore),
		base.(domain.SummaryStore),
		backend,
		[]byte(strings.Repeat("r", 32)),
	)
	return srv, fixture, backend
}

func setWorkspaceAccessTurn(
	t *testing.T,
	srv *Service,
	fixture conversationTestFixture,
	turnID, lifecycle string,
) {
	t.Helper()
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	state := domain.SessionRuntimeState{OwnerUserID: principal.User.UserID, NodeID: fixture.nodeID,
		AgentID: fixture.agentID, SessionID: "caller-session", ActiveTurnID: turnID, ActivePromptRequestID: "prompt-one", Lifecycle: lifecycle}
	if turnID == "" {
		state.ActivePromptRequestID = ""
	}
	require.NoError(t, srv.store.UpdateSessionRuntimeState(t.Context(), state))
}

func callWorkspaceAccessTool(
	t *testing.T,
	srv *Service,
	fixture conversationTestFixture,
	tool, arguments string,
) (any, error) {
	t.Helper()
	return srv.callWorkspaceTool(t.Context(), domain.Node{NodeID: fixture.nodeID}, fixture.agentID,
		workspaceToolRequest{SessionID: "caller-session", Tool: tool, ArgumentsJSON: arguments})
}

func TestWorkspaceAccessGivenNoGrantThenSearchFailsWithoutCreatingApproval(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	_, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	var response apperr.Error
	require.ErrorAs(t, err, &response)
	assert.Equal(t, http.StatusForbidden, response.Status)
	agent, err := srv.store.GetNodeAgent(t.Context(), fixture.nodeID, fixture.agentID)
	require.NoError(t, err)
	approvals, err := srv.workspaceTurnApprovals(t.Context(), agent, "caller-session", "turn-one")
	require.NoError(t, err)
	assert.Empty(t, approvals)
}

func TestWorkspaceAccessGivenManualRequestThenReturnsPendingAndUsesApprovedScope(t *testing.T) {
	srv, fixture, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	result, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"queries":["cicd"],"session_ids":["source-session"],"reason":"Find the deployment decision"}`,
	)
	require.NoError(t, err)
	status := result.(workspaceAccessStatus)
	assert.Equal(t, "pending", status.Status)
	assert.Equal(t, "turn-one", status.TurnID)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	_, err = srv.store.DecideApproval(
		t.Context(),
		principal,
		status.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "allow_once"},
	)
	require.NoError(t, err)
	_, err = callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":" CICD ","scope":{"agent_ids":["*"],"session_ids":["source-session"]}}`,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"source-session"}, backend.query.SessionIDs)
	assert.Equal(t, principal.User.UserID, backend.query.OwnerUserID)
	for _, arguments := range []string{`{"query":"cicd password","scope":{"agent_ids":["*"],"session_ids":["*"]}}`, `{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["foreign"]}}`} {
		_, err = callWorkspaceAccessTool(t, srv, fixture, "search", arguments)
		require.Error(t, err)
	}
	setWorkspaceAccessTurn(t, srv, fixture, "turn-two", domain.RuntimeLifecycleRunning)
	_, err = callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
}

func TestWorkspaceAccessGivenConcurrentRequestsThenReusesOneApproval(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	var group sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		group.Go(func() {
			result, err := callWorkspaceAccessTool(
				t,
				srv,
				fixture,
				"request_access",
				`{"queries":["cicd"]}`,
			)
			assert.NoError(t, err)
			if err == nil {
				ids <- result.(workspaceAccessStatus).ApprovalID
			}
		})
	}
	group.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		assert.Equal(t, first, id)
	}
}

func TestWorkspaceAccessGivenPreauthorizationOrAutoApproveThenAvailableBeforeSearch(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		scope      *domain.WorkspaceSearchAccess
	}{
		{"explicit", domain.SessionApprovalModeManual, &domain.WorkspaceSearchAccess{Queries: []string{"cicd"}}},
		{"automatic", domain.SessionApprovalModeAutoApproveAll, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, fixture, _ := workspaceAccessFixture(t, test.mode)
			setWorkspaceAccessTurn(t, srv, fixture, "", domain.RuntimeLifecycleIdle)
			require.NoError(
				t,
				srv.prepareTurnSearchAccess(
					t.Context(),
					fixture.nodeID,
					fixture.agentID,
					"caller-session",
					"turn-one",
					test.scope,
				),
			)
			setWorkspaceAccessTurn(t, srv, fixture, "turn-one", domain.RuntimeLifecycleRunning)
			if test.scope == nil {
				_, denied := callWorkspaceAccessTool(
					t,
					srv,
					fixture,
					"search",
					`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
				)
				require.Error(
					t,
					denied,
					"auto approve must not expand scope before an explicit request",
				)
				result, accessErr := callWorkspaceAccessTool(
					t,
					srv,
					fixture,
					"request_access",
					`{}`,
				)
				require.NoError(t, accessErr)
				require.Equal(t, "approved", result.(workspaceAccessStatus).Status)
			}
			_, err := callWorkspaceAccessTool(
				t,
				srv,
				fixture,
				"search",
				`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
			)
			require.NoError(t, err)
		})
	}
}

func TestWorkspaceAccessGivenTurnEndsDuringSearchThenWithholdsResultAndExpiresGrant(t *testing.T) {
	srv, fixture, backend := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	require.NoError(
		t,
		srv.prepareTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-one",
			&domain.WorkspaceSearchAccess{},
		),
	)
	backend.after = func() {
		require.NoError(
			t,
			srv.expireTurnSearchAccess(
				t.Context(),
				fixture.nodeID,
				fixture.agentID,
				"caller-session",
				"turn-one",
			),
		)
	}
	_, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
	_, err = callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"queries":["another query"]}`,
	)
	require.Error(t, err)
}

func TestWorkspaceAccessGivenCancelledTurnWithoutGrantThenCannotRequestAuthorization(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	require.NoError(
		t,
		srv.expireTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-one",
		),
	)
	_, err := callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
	require.Error(t, err)
}

func TestWorkspaceAccessGivenInvalidTransportIdentityOrToolThenRejects(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	_, err := srv.callWorkspaceTool(
		t.Context(),
		domain.Node{NodeID: "foreign"},
		fixture.agentID,
		workspaceToolRequest{
			SessionID:     "caller-session",
			Tool:          "request_access",
			ArgumentsJSON: `{}`,
		},
	)
	require.Error(t, err)
	for _, test := range []struct{ tool, arguments string }{
		{"read", `{"ref":{"kind":"message","id":"one"}}`}, {"history", `{}`},
		{"request_access", `{"queries":["*","cicd"]}`}, {"request_access", `{"owner_user_id":"foreign"}`},
	} {
		_, err = callWorkspaceAccessTool(t, srv, fixture, test.tool, test.arguments)
		require.Error(t, err)
	}
}

func TestWorkspaceAccessGivenDeniedRequestThenRepeatedRequestRemainsDenied(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	result, err := callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
	require.NoError(t, err)
	status := result.(workspaceAccessStatus)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	_, err = srv.store.DecideApproval(
		t.Context(),
		principal,
		status.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "deny"},
	)
	require.NoError(t, err)
	result, err = callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
	require.NoError(t, err)
	assert.Equal(t, "denied", result.(workspaceAccessStatus).Status)
	assert.Equal(t, status.ApprovalID, result.(workspaceAccessStatus).ApprovalID)
	var body workspaceAccessBody
	approval, err := srv.store.GetApproval(t.Context(), principal, status.ApprovalID)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(approval.RequestBody, &body))
	assert.Equal(t, []string{"*"}, body.Scope.Queries)
}

func TestWorkspaceAccessGivenImplicitAgentAutoApproveThenStillRequiresDataAuthorization(
	t *testing.T,
) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	require.NoError(
		t,
		srv.store.SetSessionApprovalMode(
			t.Context(),
			fixture.agentID,
			"caller-session",
			domain.SessionApprovalModeAutoApproveAll,
		),
	)
	require.NoError(
		t,
		srv.prepareTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-one",
			nil,
		),
	)
	_, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
	result, err := callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
	require.NoError(t, err)
	assert.Equal(t, "pending", result.(workspaceAccessStatus).Status)
}

func TestWorkspaceAccessGivenSnapshotLagThenUsesObservedTurnCompletion(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	agent := &ACPTunnelAgent{
		ownerUserID: principal.User.UserID,
		nodeID:      fixture.nodeID,
		agentID:     fixture.agentID,
	}
	setWorkspaceAccessTurn(t, srv, fixture, "", domain.RuntimeLifecycleIdle)
	require.NoError(
		t,
		srv.prepareTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-one",
			&domain.WorkspaceSearchAccess{},
		),
	)
	srv.acpRuntime.apply(
		agent,
		"caller-session",
		acpRuntimeEvent{kind: "prompt_started", turnID: "turn-one", requestID: "prompt-one"},
		"",
	)
	_, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.NoError(t, err)
	// The persistent snapshot still claims running when ACP has completed.
	setWorkspaceAccessTurn(t, srv, fixture, "turn-one", domain.RuntimeLifecycleRunning)
	srv.acpRuntime.apply(
		agent,
		"caller-session",
		acpRuntimeEvent{kind: "prompt_completed", requestID: "prompt-one"},
		"turn-one",
	)
	_, err = callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
}

func TestWorkspaceAccessGivenConflictingActiveTurnsThenFailsClosed(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	agent := &ACPTunnelAgent{
		ownerUserID: principal.User.UserID,
		nodeID:      fixture.nodeID,
		agentID:     fixture.agentID,
	}
	require.NoError(
		t,
		srv.prepareTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-two",
			&domain.WorkspaceSearchAccess{},
		),
	)
	srv.acpRuntime.apply(
		agent,
		"caller-session",
		acpRuntimeEvent{kind: "prompt_started", turnID: "turn-two", requestID: "prompt-two"},
		"",
	)
	_, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
}

func TestWorkspaceAccessGivenExpiredTurnRecordThenRestartCannotRestoreAuthorization(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	require.NoError(
		t,
		srv.prepareTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-one",
			&domain.WorkspaceSearchAccess{},
		),
	)
	require.NoError(
		t,
		srv.expireTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-one",
		),
	)
	// Losing the in-memory fence must not revive a persisted grant.
	srv.workspaceAccessEnds.Delete(acpSessionKey(fixture.agentID, "caller-session"))
	_, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
	_, err = callWorkspaceAccessTool(t, srv, fixture, "request_access", `{"queries":["new scope"]}`)
	require.Error(t, err)
	setWorkspaceAccessTurn(t, srv, fixture, "turn-two", domain.RuntimeLifecycleRunning)
	require.NoError(
		t,
		srv.prepareTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-two",
			&domain.WorkspaceSearchAccess{},
		),
	)
	_, err = callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.NoError(t, err)
}

type workspaceAccessBarrierStore struct {
	domain.Store
	created chan struct{}
	release chan struct{}
}

func TestWorkspaceAccessGivenNodeFabricatedActiveTurnThenCannotRequestOrSearch(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	srv.workspaceAccessTurns.Delete(acpSessionKey(fixture.agentID, "caller-session"))
	for _, tool := range []string{"request_access", "search"} {
		arguments := `{}`
		if tool == "search" {
			arguments = `{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`
		}
		_, err := callWorkspaceAccessTool(t, srv, fixture, tool, arguments)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Manager-dispatched turn")
	}
}

func TestWorkspaceAccessGivenManagerRestartThenPersistedApprovalDoesNotRestoreGrant(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	require.NoError(
		t,
		srv.prepareTurnSearchAccess(
			t.Context(),
			fixture.nodeID,
			fixture.agentID,
			"caller-session",
			"turn-one",
			&domain.WorkspaceSearchAccess{},
		),
	)
	srv.workspaceAccessTurns.Delete(acpSessionKey(fixture.agentID, "caller-session"))
	_, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
	_, err = callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
	require.Error(t, err)
}

func (s workspaceAccessBarrierStore) CreateApproval(
	ctx context.Context, node domain.Node, request domain.CreateApprovalRequest,
) (domain.AgentApproval, error) {
	close(s.created)
	select {
	case <-s.release:
		return s.Store.CreateApproval(ctx, node, request)
	case <-ctx.Done():
		return domain.AgentApproval{}, ctx.Err()
	}
}

func (s workspaceAccessBarrierStore) ExpireWorkspaceTurnApprovals(
	ctx context.Context, ownerID, nodeID, agentID, sessionID, turnID string,
) error {
	return s.Store.(workspaceTurnApprovalExpirer).ExpireWorkspaceTurnApprovals(
		ctx,
		ownerID,
		nodeID,
		agentID,
		sessionID,
		turnID,
	)
}

func TestWorkspaceAccessGivenTurnEndsWhileCreatingGrantThenExpiresTheNewRecord(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	barrier := workspaceAccessBarrierStore{
		Store:   srv.store,
		created: make(chan struct{}),
		release: make(chan struct{}),
	}
	srv.store = barrier
	requestDone := make(chan error, 1)
	go func() {
		_, err := callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
		requestDone <- err
	}()
	select {
	case <-barrier.created:
	case <-time.After(5 * time.Second):
		t.Fatal("authorization request did not reach creation")
	}
	expireDone := make(chan error, 1)
	go func() {
		expireDone <- srv.expireTurnSearchAccess(t.Context(), fixture.nodeID, fixture.agentID, "caller-session", "turn-one")
	}()
	require.Eventually(t, func() bool {
		return srv.workspaceTurnEnded(fixture.agentID, "caller-session", "turn-one")
	}, time.Second, time.Millisecond)
	close(barrier.release)
	require.NoError(t, <-expireDone)
	require.Error(t, <-requestDone)
	agent, err := srv.store.GetNodeAgent(t.Context(), fixture.nodeID, fixture.agentID)
	require.NoError(t, err)
	approvals, err := srv.workspaceTurnApprovals(t.Context(), agent, "caller-session", "turn-one")
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	require.NotNil(t, approvals[0].ExpiresAt)
	assert.False(t, approvals[0].ExpiresAt.After(srv.clock()))
	srv.workspaceAccessEnds.Delete(acpSessionKey(fixture.agentID, "caller-session"))
	_, err = callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
}
