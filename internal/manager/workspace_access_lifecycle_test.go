package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestWorkspaceAccessLifecycleExpiresSuccessfulAndFailedPrompts(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "completed"
		if failed {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
			require.NoError(
				t,
				srv.prepareTurnSearchAccess(t.Context(), fixture.nodeID, fixture.agentID,
					"caller-session", "turn-one", &domain.WorkspaceSearchAccess{}),
			)
			principal := testUserPrincipal(t, srv, fixture.userEmail)
			agent := &ACPTunnelAgent{agentID: fixture.agentID, nodeID: fixture.nodeID,
				ownerUserID: principal.User.UserID, store: srv.store}
			frame := &acpFrameContext{agent: agent, direction: acpAgentToUser,
				managerSessionID: "caller-session", businessTurnID: "turn-one", requestKind: "session/prompt",
				frame: acpJSONRPCMessage{
					ID:     json.RawMessage("41"),
					Result: json.RawMessage(`{"stopReason":"end_turn"}`),
				}}
			if failed {
				frame.frame.Result = nil
				frame.frame.Error = json.RawMessage(`{"code":-32603,"message":"prompt failed"}`)
			}
			called := false
			require.NoError(t, (workspaceAccessLifecycleMiddleware{service: srv}).HandleACPFrame(
				t.Context(),
				frame,
				func(context.Context, *acpFrameContext) error { called = true; return nil },
			))
			require.True(t, called)
			_, err := callWorkspaceAccessTool(
				t,
				srv,
				fixture,
				"search",
				`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
			)
			require.Error(t, err, "completion must fence even a stale running snapshot")
			agentRecord, err := srv.store.GetNodeAgent(t.Context(), fixture.nodeID, fixture.agentID)
			require.NoError(t, err)
			approvals, err := srv.workspaceTurnApprovals(
				t.Context(),
				agentRecord,
				"caller-session",
				"turn-one",
			)
			require.NoError(t, err)
			require.Len(t, approvals, 1)
			require.NotNil(t, approvals[0].ExpiresAt)
			require.False(t, approvals[0].ExpiresAt.After(srv.clock()))
		})
	}
}

func TestWorkspaceAccessLifecycleKeepsGrantForInternalRouteRecovery(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	require.NoError(t, srv.prepareTurnSearchAccess(t.Context(), fixture.nodeID, fixture.agentID,
		"caller-session", "turn-one", &domain.WorkspaceSearchAccess{}))
	frame := &acpFrameContext{
		agent:     &ACPTunnelAgent{agentID: fixture.agentID, nodeID: fixture.nodeID},
		direction: acpAgentToUser, managerSessionID: "caller-session",
		businessTurnID: "turn-one", requestKind: "session/prompt",
		frame: acpJSONRPCMessage{ID: json.RawMessage("41"), Error: json.RawMessage(
			`{"message":"route missing","data":{"kind":"session_route_missing","retryable":true}}`,
		)},
	}
	require.NoError(t, (workspaceAccessLifecycleMiddleware{service: srv}).HandleACPFrame(
		t.Context(), frame, func(context.Context, *acpFrameContext) error { return nil },
	))
	_, err := callWorkspaceAccessTool(
		t,
		srv,
		fixture,
		"search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	)
	require.NoError(t, err)
}

type workspaceFailingTurnWriter struct{ header http.Header }

func (w *workspaceFailingTurnWriter) Header() http.Header { return w.header }
func (*workspaceFailingTurnWriter) Write([]byte) (int, error) {
	return 0, errors.New("connection closed before dispatch")
}
func (*workspaceFailingTurnWriter) WriteHeader(int) {}
func (*workspaceFailingTurnWriter) Flush()          {}

func TestWorkspaceAccessLifecycleExpiresGrantWhenPromptWasNeverDispatched(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	runner := &conversationRunner{
		service:      srv,
		searchAccess: &domain.WorkspaceSearchAccess{},
		agentConn: &ACPTunnelAgent{
			agentID: fixture.agentID, nodeID: fixture.nodeID,
		},
	}
	writer := &workspaceFailingTurnWriter{header: make(http.Header)}
	_, err := srv.promptConversationOnce(t.Context(), writer, writer, runner,
		conversationSession{managerID: "caller-session"}, nil, "turn-one", nil)
	require.ErrorContains(t, err, "connection closed before dispatch")
	_, err = callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
	require.Error(t, err)
}

func TestWorkspaceAccessLifecycleSharedAgentCallerCannotAuthorizeOwnerData(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeAutoApproveAll)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	runner := &conversationRunner{searchAccessOwnerID: "another-user",
		agentConn: &ACPTunnelAgent{agentID: fixture.agentID, nodeID: fixture.nodeID,
			ownerUserID: principal.User.UserID},
		searchAccess: &domain.WorkspaceSearchAccess{},
	}
	session := conversationSession{managerID: "caller-session"}
	require.Error(
		t,
		srv.prepareConversationSearchAccess(t.Context(), runner, session, "shared-turn"),
	)
	runner.searchAccess = nil
	require.NoError(
		t,
		srv.prepareConversationSearchAccess(t.Context(), runner, session, "shared-turn"),
	)
	setWorkspaceAccessTurn(t, srv, fixture, "shared-turn", domain.RuntimeLifecycleRunning)
	_, err := callWorkspaceAccessTool(t, srv, fixture, "request_access", `{}`)
	require.Error(t, err, "team visibility is not consent to search the agent owner's workspace")
}

func TestWorkspaceAccessLateTerminalCannotRestoreCancelledNextTurn(t *testing.T) {
	srv, fixture, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	require.NoError(t, srv.prepareTurnSearchAccess(t.Context(), fixture.nodeID, fixture.agentID,
		"caller-session", "turn-two", nil))
	setWorkspaceAccessTurn(t, srv, fixture, "turn-two", domain.RuntimeLifecycleRunning)
	require.NoError(t, srv.expireTurnSearchAccess(t.Context(), fixture.nodeID, fixture.agentID,
		"caller-session", "turn-two"))
	frame := &acpFrameContext{
		agent:     &ACPTunnelAgent{agentID: fixture.agentID, nodeID: fixture.nodeID},
		direction: acpAgentToUser, managerSessionID: "caller-session",
		businessTurnID: "turn-one", requestKind: "session/prompt",
		frame: acpJSONRPCMessage{
			ID:     json.RawMessage("1"),
			Result: json.RawMessage(`{"stopReason":"end_turn"}`),
		},
	}
	require.NoError(t, (workspaceAccessLifecycleMiddleware{service: srv}).HandleACPFrame(
		t.Context(), frame, func(context.Context, *acpFrameContext) error { return nil },
	))
	_, exists := srv.workspaceAccessTurns.Load(acpSessionKey(fixture.agentID, "caller-session"))
	require.False(t, exists)
	_, err := callWorkspaceAccessTool(t, srv, fixture, "search",
		`{"query":"cicd","scope":{"agent_ids":["self"],"session_ids":["*"]}}`)
	require.Error(t, err)
	require.Empty(t, backend.query.Query)
}
