package manager

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func accessRequestStatus(
	t *testing.T,
	srv *Service,
	fixture conversationTestFixture,
	operation, id string,
) workspaceAccessStatus {
	t.Helper()
	arguments, err := json.Marshal(map[string]string{"approval_id": id})
	require.NoError(t, err)
	result, err := callWorkspaceAccessTool(t, srv, fixture, operation, string(arguments))
	require.NoError(t, err)
	return result.(workspaceAccessStatus)
}

func TestWorkspaceRequestDeadlineAndStatusPolling(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	now := srv.clock().UTC()
	srv.clock = func() time.Time { return now }
	result, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"queries":["cicd"]}`,
	)
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	require.Equal(t, "pending", request.Status)
	require.NotEmpty(t, request.ApprovalID)
	require.Equal(t, "turn-one", request.TurnID)
	require.Equal(t, 2000, request.PollAfterMS)
	require.NotNil(t, request.ExpiresAt)
	require.Equal(t, now.Add(10*time.Minute), *request.ExpiresAt)
	require.Equal(
		t,
		request,
		accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID),
	)
	now = now.Add(10 * time.Minute)
	expired := accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID)
	require.Equal(t, "expired", expired.Status)
	require.Zero(t, expired.PollAfterMS)
	// An expired request cannot poison this turn or extend itself when queried.
	require.Equal(t, request.ExpiresAt, expired.ExpiresAt)
	result, err = callWorkspaceAccessTool(t, srv, fixture, "request_access", `{"queries":["cicd"]}`)
	require.NoError(t, err)
	replacement := result.(workspaceAccessStatus)
	require.Equal(t, "pending", replacement.Status)
	require.NotEqual(t, request.ApprovalID, replacement.ApprovalID)
	result, err = callWorkspaceAccessTool(t, srv, fixture, "request_access", `{"queries":["cicd"]}`)
	require.NoError(t, err)
	require.Equal(t, replacement.ApprovalID, result.(workspaceAccessStatus).ApprovalID)
}

func TestWorkspaceRequestNoTimeLimitStillEndsWithTurn(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	now := srv.clock().UTC()
	srv.clock = func() time.Time { return now }
	result, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"timeout_seconds":0}`,
	)
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	require.Nil(t, request.ExpiresAt)
	now = now.Add(48 * time.Hour)
	require.Equal(
		t,
		"pending",
		accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID).Status,
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
	setWorkspaceAccessTurn(t, srv, fixture, "", domain.RuntimeLifecycleIdle)
	require.Equal(
		t,
		"expired",
		accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID).Status,
	)
	require.Equal(
		t,
		"expired",
		accessRequestStatus(t, srv, fixture, "cancel_access", request.ApprovalID).Status,
	)
	_, err = srv.store.DecideApproval(
		t.Context(),
		testUserPrincipal(t, srv, fixture.userEmail),
		request.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "allow_once"},
	)
	require.Error(t, err, "no-time-limit requests must not authorize a finished turn")
}

func TestWorkspaceRequestCancelIsIdempotentAndStopsLateApproval(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	result, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"timeout_seconds":0}`,
	)
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	for range 2 {
		cancelled := accessRequestStatus(t, srv, fixture, "cancel_access", request.ApprovalID)
		require.Equal(t, "cancelled", cancelled.Status)
		require.Zero(t, cancelled.PollAfterMS)
	}
	require.Equal(
		t,
		"cancelled",
		accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID).Status,
	)
	_, err = srv.store.DecideApproval(
		t.Context(),
		testUserPrincipal(t, srv, fixture.userEmail),
		request.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "allow_once"},
	)
	require.Error(t, err)
	_, err = callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.Error(t, err)
}

func TestWorkspaceRequestApprovalOutlivesPendingDeadlineAndCanBeCancelled(t *testing.T) {
	srv, fixture, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	now := srv.clock().UTC()
	srv.clock = func() time.Time { return now }
	result, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"timeout_seconds":2}`,
	)
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	_, err = srv.store.DecideApproval(
		t.Context(),
		testUserPrincipal(t, srv, fixture.userEmail),
		request.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "allow_once"},
	)
	require.NoError(t, err)
	now = now.Add(time.Minute)
	require.Equal(
		t,
		"approved",
		accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID).Status,
	)
	search := `{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`
	_, err = callWorkspaceAccessTool(t, srv, fixture, "search", search)
	require.NoError(t, err)
	backend.query = domain.SearchQuery{}
	require.Equal(
		t,
		"cancelled",
		accessRequestStatus(t, srv, fixture, "cancel_access", request.ApprovalID).Status,
	)
	_, err = callWorkspaceAccessTool(t, srv, fixture, "search", search)
	require.Error(t, err)
	require.Empty(t, backend.query.Query)
}

func TestWorkspaceRequestStatusAndCancellationCannotAccessOtherSessions(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	result, err := callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	arguments, err := json.Marshal(map[string]string{"approval_id": request.ApprovalID})
	require.NoError(t, err)
	for _, operation := range []string{"access_status", "cancel_access"} {
		_, err = srv.callWorkspaceTool(
			t.Context(),
			domain.Node{NodeID: fixture.nodeID},
			fixture.agentID,
			workspaceToolRequest{
				SessionID:     "source-session",
				Tool:          operation,
				ArgumentsJSON: string(arguments),
			},
		)
		require.Error(t, err)
		other := testNodeAgent(t, srv, fixture.userEmail)
		createConversationTestSession(
			t,
			srv,
			other,
			"other-session-"+operation,
			"other-native-"+operation,
		)
		_, err = srv.callWorkspaceTool(
			t.Context(),
			domain.Node{NodeID: other.nodeID},
			other.agentID,
			workspaceToolRequest{
				SessionID:     "other-session-" + operation,
				Tool:          operation,
				ArgumentsJSON: string(arguments),
			},
		)
		require.Error(t, err)
	}
	require.Equal(
		t,
		"pending",
		accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID).Status,
	)
}

func TestWorkspaceRequestTimeoutValidationAndDistinctTimeouts(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	for _, arguments := range []string{`{"timeout_seconds":-1}`, `{"timeout_seconds":86401}`,
		`{"timeout_seconds":1.5}`, `{"timeout_seconds":"0"}`, `{"timeout_seconds":true}`} {
		_, err := callWorkspaceAccessTool(t, srv, fixture, "request_access", arguments)
		require.Error(t, err, arguments)
	}
	first, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"timeout_seconds":0}`,
	)
	require.NoError(t, err)
	second, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"timeout_seconds":86400}`,
	)
	require.NoError(t, err)
	require.NotEqual(
		t,
		first.(workspaceAccessStatus).ApprovalID,
		second.(workspaceAccessStatus).ApprovalID,
	)
	for _, operation := range []string{"access_status", "cancel_access"} {
		for _, arguments := range []string{`{}`, `{"approval_id":" "}`, `{"approval_id":4}`,
			`{"approval_id":"missing","turn_id":"spoofed"}`} {
			_, err = callWorkspaceAccessTool(t, srv, fixture, operation, arguments)
			require.Error(t, err)
		}
	}
}

func TestWorkspaceRequestDenialRemainsTerminalAfterDeadline(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	now := srv.clock()
	srv.clock = func() time.Time { return now }
	result, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"timeout_seconds":2}`,
	)
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	_, err = srv.decideUserApproval(
		t.Context(),
		testUserPrincipal(t, srv, fixture.userEmail),
		request.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "deny"},
	)
	require.NoError(t, err)
	now = now.Add(time.Hour)
	require.Equal(
		t,
		"denied",
		accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID).Status,
	)
	result, err = callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"timeout_seconds":2}`,
	)
	require.NoError(t, err)
	require.Equal(t, "denied", result.(workspaceAccessStatus).Status)
	require.Equal(t, request.ApprovalID, result.(workspaceAccessStatus).ApprovalID)
}

func TestWorkspaceRequestCannotBeApprovedAfterManagerLosesTurnRegistration(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	result, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"request_access",
		`{"timeout_seconds":0}`,
	)
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	// Model a restart before any old pending record has a persisted deadline.
	srv.workspaceAccessTurns.Delete(acpSessionKey(fixture.agentID, "caller-session"))
	_, err = srv.decideUserApproval(
		t.Context(),
		testUserPrincipal(t, srv, fixture.userEmail),
		request.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "allow_once"},
	)
	require.ErrorIs(t, err, domain.ErrConflict)
	require.Equal(
		t,
		"expired",
		accessRequestStatus(t, srv, fixture, "access_status", request.ApprovalID).Status,
	)
}
