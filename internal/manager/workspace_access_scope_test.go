package manager

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestWorkspaceAccessDefaultSearchesAllOwnSessionsWithoutExpansion(t *testing.T) {
	srv, caller, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	other := testNodeAgent(t, srv, caller.userEmail)
	createConversationTestSession(t, srv, other, "other-session", "other-native")
	for _, row := range []struct {
		id, agent, session string
	}{
		{"own-first", caller.agentID, "caller-session"},
		{"own-history", caller.agentID, "source-session"},
		{"other-history", other.agentID, "other-session"},
	} {
		message := domain.Message{MessageID: row.id, AgentID: row.agent, SessionID: row.session,
			Role: "assistant", Source: domain.MessageSourceACPTunnel,
			Direction: domain.MessageDirectionAgentToUser, MessageType: "agent_message_chunk"}
		require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
		require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
			MessageID: row.id, PartIndex: 0, PartType: domain.MessagePartText, Text: "cicd deployment",
		}))
		backend.docs = append(
			backend.docs,
			domain.SearchDocument{Kind: "message", SourceID: row.id},
		)
	}
	result, err := callWorkspaceAccessTool(t, srv, caller, "search",
		`{"query":"cicd","scope":{"agent_ids":["self"],"session_ids":["*"]}}`)
	require.NoError(t, err)
	page := result.(workspaceScopedSearchPage)
	require.Len(t, page.Hits, 2)
	require.Equal(t, []string{caller.agentID}, page.Scope.AgentIDs)
	require.Equal(t, []string{"*"}, page.Scope.SessionIDs)
	require.Equal(t, []string{caller.agentID}, backend.query.AgentIDs)
	require.Empty(t, backend.query.SessionIDs)
	agent, err := srv.store.GetNodeAgent(t.Context(), caller.nodeID, caller.agentID)
	require.NoError(t, err)
	approvals, err := srv.workspaceTurnApprovals(t.Context(), agent, "caller-session", "turn-one")
	require.NoError(t, err)
	require.Empty(t, approvals)
}

func TestWorkspaceAccessRequiresDeclaredScopeAndNeverSilentlyNarrows(t *testing.T) {
	srv, caller, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	for _, input := range []string{
		`{"query":"cicd"}`,
		`{"query":"cicd","scope":{"agent_ids":["self"]}}`,
		`{"query":"cicd","scope":{"agent_ids":[],"session_ids":["*"]}}`,
		`{"query":"cicd","scope":{"agent_ids":["*","self"],"session_ids":["*"]}}`,
	} {
		_, err := callWorkspaceAccessTool(t, srv, caller, "search", input)
		var failure apperr.Error
		require.ErrorAs(t, err, &failure)
		require.Equal(t, http.StatusBadRequest, failure.Status)
	}
	_, err := callWorkspaceAccessTool(t, srv, caller, "search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`)
	var failure apperr.Error
	require.ErrorAs(t, err, &failure)
	require.Equal(t, http.StatusForbidden, failure.Status)
	require.Contains(t, failure.Message, "workspace_request_access")
	require.Empty(t, backend.query.Query, "an unauthorized scope must never reach ES")
}

func TestWorkspaceAccessExpandedAgentScopeIsExplicitAndTurnBound(t *testing.T) {
	srv, caller, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	other := testNodeAgent(t, srv, caller.userEmail)
	createConversationTestSession(t, srv, other, "other-session", "other-native")
	arguments, err := json.Marshal(workspaceAccessRequest{AgentIDs: []string{other.agentID}})
	require.NoError(t, err)
	result, err := callWorkspaceAccessTool(t, srv, caller, "request_access", string(arguments))
	require.NoError(t, err)
	status := result.(workspaceAccessStatus)
	require.Equal(t, "pending", status.Status)
	_, err = srv.store.DecideApproval(t.Context(), testUserPrincipal(t, srv, caller.userEmail),
		status.ApprovalID, domain.ApprovalDecisionRequest{DecisionOption: "allow_once"})
	require.NoError(t, err)
	search, err := json.Marshal(
		workspaceSearchRequest{Query: "cicd", Scope: &domain.WorkspaceSearchScope{
			AgentIDs: []string{other.agentID}, SessionIDs: []string{"*"},
		}},
	)
	require.NoError(t, err)
	_, err = callWorkspaceAccessTool(t, srv, caller, "search", string(search))
	require.NoError(t, err)
	require.Equal(t, []string{other.agentID}, backend.query.AgentIDs)
	_, err = callWorkspaceAccessTool(t, srv, caller, "search",
		`{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`)
	require.Error(t, err, "an agent-specific grant cannot authorize a workspace search")
	_, err = callWorkspaceAccessTool(t, srv, caller, "search",
		`{"query":"cicd","scope":{"agent_ids":["self"],"session_ids":["other-session"]}}`)
	require.Error(t, err, "a session from another agent contradicts the declared self scope")
	require.NoError(t, srv.expireTurnSearchAccess(t.Context(), caller.nodeID, caller.agentID,
		"caller-session", "turn-one"))
	_, err = callWorkspaceAccessTool(t, srv, caller, "search", string(search))
	require.Error(t, err)
}
