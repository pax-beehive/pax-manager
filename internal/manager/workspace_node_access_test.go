package manager

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func nodeSearchArguments(t *testing.T, nodes, agents, sessions []string) string {
	t.Helper()
	encoded, err := json.Marshal(
		workspaceSearchRequest{Query: "cicd", Scope: &domain.WorkspaceSearchScope{
			NodeIDs: nodes, AgentIDs: agents, SessionIDs: sessions,
		}},
	)
	require.NoError(t, err)
	return string(encoded)
}

func createNodeSearchPeer(
	t *testing.T,
	srv *Service,
	caller conversationTestFixture,
) conversationTestFixture {
	t.Helper()
	agent, _, err := srv.store.CreateNodeAgent(
		t.Context(),
		testUserPrincipal(t, srv, caller.userEmail),
		domain.CreateAgentRequest{NodeID: caller.nodeID, Name: "node-peer", AgentType: "hermes"},
	)
	require.NoError(t, err)
	peer := caller
	peer.agentID = agent.AgentID
	createConversationTestSession(t, srv, peer, "peer-session", "peer-native")
	return peer
}

func TestWorkspaceNodeDefaultIncludesPeerAgentsAndExcludesOtherNodes(t *testing.T) {
	srv, caller, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	peer := createNodeSearchPeer(t, srv, caller)
	other := testNodeAgent(t, srv, caller.userEmail)
	createConversationTestSession(t, srv, other, "other-node-session", "other-node-native")
	for _, row := range []struct{ id, node, agent, session string }{
		{"own", caller.nodeID, caller.agentID, "caller-session"},
		{"own-history", caller.nodeID, caller.agentID, "source-session"},
		{"peer", peer.nodeID, peer.agentID, "peer-session"},
		{"other-node", other.nodeID, other.agentID, "other-node-session"},
	} {
		message := domain.Message{
			MessageID:   row.id,
			NodeID:      row.node,
			AgentID:     row.agent,
			SessionID:   row.session,
			Role:        "assistant",
			Source:      domain.MessageSourceACPTunnel,
			Direction:   domain.MessageDirectionAgentToUser,
			MessageType: "agent_message_chunk",
		}
		require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
		require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
			MessageID: row.id, PartIndex: 0, PartType: domain.MessagePartText, Text: "cicd deployment",
		}))
		// Deliberately return every candidate. Fresh-source filtering must also
		// enforce the node boundary if an index is stale or ignores its filter.
		backend.docs = append(
			backend.docs,
			domain.SearchDocument{Kind: "message", SourceID: row.id},
		)
	}
	result, err := callWorkspaceAccessTool(t, srv, caller, "search",
		nodeSearchArguments(t, []string{"self"}, []string{"*"}, []string{"*"}))
	require.NoError(t, err)
	page := result.(workspaceScopedSearchPage)
	require.Len(t, page.Hits, 3)
	for _, hit := range page.Hits {
		require.NotEqual(t, "other-node", hit.Ref.ID)
	}
	require.Equal(t, []string{caller.nodeID}, page.Scope.NodeIDs)
	require.Equal(t, []string{"*"}, page.Scope.AgentIDs)
	require.Equal(t, []string{caller.nodeID}, backend.query.NodeIDs)
	require.Empty(t, backend.query.AgentIDs)
	result, err = callWorkspaceAccessTool(t, srv, caller, "search",
		nodeSearchArguments(t, []string{"self"}, []string{peer.agentID}, []string{"peer-session"}))
	require.NoError(t, err)
	require.Len(t, result.(workspaceScopedSearchPage).Hits, 1)
	agent, err := srv.store.GetNodeAgent(t.Context(), caller.nodeID, caller.agentID)
	require.NoError(t, err)
	approvals, err := srv.workspaceTurnApprovals(t.Context(), agent, "caller-session", "turn-one")
	require.NoError(t, err)
	require.Empty(t, approvals, "same-node search must not create an approval")
}

func TestWorkspaceNodeDefaultRejectsCrossNodeAndContradictoryDeclarations(t *testing.T) {
	srv, caller, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	createNodeSearchPeer(t, srv, caller)
	other := testNodeAgent(t, srv, caller.userEmail)
	createConversationTestSession(t, srv, other, "other-node-session", "other-node-native")
	foreign := testNodeAgent(t, srv, "foreign@example.test")
	for _, arguments := range []string{
		nodeSearchArguments(t, []string{other.nodeID}, []string{"*"}, []string{"*"}),
		nodeSearchArguments(t, []string{"*"}, []string{"self"}, []string{"*"}),
		nodeSearchArguments(t, []string{"self"}, []string{"*"}, []string{"other-node-session"}),
		nodeSearchArguments(t, []string{"self"}, []string{"self"}, []string{"peer-session"}),
		nodeSearchArguments(t, []string{foreign.nodeID}, []string{"*"}, []string{"*"}),
		`{"query":"cicd","scope":{"agent_ids":["self"],"session_ids":["*"]}}`,
	} {
		_, err := callWorkspaceAccessTool(t, srv, caller, "search", arguments)
		var failure apperr.Error
		require.ErrorAs(t, err, &failure, arguments)
		require.Equal(t, http.StatusForbidden, failure.Status, arguments)
		require.Empty(t, backend.query.Query, "a rejected range must not reach the backend")
	}
}

func TestWorkspaceNodeExpansionOnlyAuthorizesItsDeclaredNodes(t *testing.T) {
	srv, caller, backend := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	other := testNodeAgent(t, srv, caller.userEmail)
	createConversationTestSession(t, srv, other, "other-node-session", "other-node-native")
	arguments, err := json.Marshal(
		workspaceAccessRequest{Queries: []string{"cicd"}, NodeIDs: []string{other.nodeID}},
	)
	require.NoError(t, err)
	result, err := callWorkspaceAccessTool(t, srv, caller, "request_access", string(arguments))
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	require.Equal(t, []string{other.nodeID}, request.Scope.NodeIDs)
	_, err = srv.decideUserApproval(
		t.Context(),
		testUserPrincipal(t, srv, caller.userEmail),
		request.ApprovalID,
		domain.ApprovalDecisionRequest{DecisionOption: "allow_once"},
	)
	require.NoError(t, err)
	_, err = callWorkspaceAccessTool(
		t,
		srv,
		caller,
		"search",
		nodeSearchArguments(
			t,
			[]string{other.nodeID},
			[]string{"*"},
			[]string{"other-node-session"},
		),
	)
	require.NoError(t, err)
	require.Equal(t, []string{other.nodeID}, backend.query.NodeIDs)
	_, err = callWorkspaceAccessTool(t, srv, caller, "search",
		nodeSearchArguments(t, []string{"*"}, []string{"*"}, []string{"*"}))
	require.Error(t, err)
	require.NoError(
		t,
		srv.expireTurnSearchAccess(
			t.Context(),
			caller.nodeID,
			caller.agentID,
			"caller-session",
			"turn-one",
		),
	)
	_, err = callWorkspaceAccessTool(t, srv, caller, "search",
		nodeSearchArguments(t, []string{other.nodeID}, []string{"*"}, []string{"*"}))
	require.Error(t, err)
}

func TestWorkspaceNodeSelfPreauthorizationAndLegacyMissingNodeScope(t *testing.T) {
	srv, caller, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	result, err := callWorkspaceAccessTool(
		t,
		srv,
		caller,
		"request_access",
		`{"node_ids":["self"],"timeout_seconds":0}`,
	)
	require.NoError(t, err)
	request := result.(workspaceAccessStatus)
	require.Equal(t, []string{caller.nodeID}, request.Scope.NodeIDs)
	legacy, err := domain.NormalizeWorkspaceSearchAccess(&domain.WorkspaceSearchAccess{})
	require.NoError(t, err)
	require.Equal(t, []string{"*"}, legacy.NodeIDs)
	status := accessRequestStatus(t, srv, caller, "access_status", request.ApprovalID)
	require.Equal(t, request.Scope.NodeIDs, status.Scope.NodeIDs)
	require.Equal(
		t,
		"cancelled",
		accessRequestStatus(t, srv, caller, "cancel_access", request.ApprovalID).Status,
	)
	require.NoError(t, srv.prepareTurnSearchAccess(
		t.Context(),
		caller.nodeID,
		caller.agentID,
		"caller-session",
		"next-node-turn",
		&domain.WorkspaceSearchAccess{NodeIDs: []string{"self"}},
	))
	agent, err := srv.store.GetNodeAgent(t.Context(), caller.nodeID, caller.agentID)
	require.NoError(t, err)
	approvals, err := srv.workspaceTurnApprovals(
		t.Context(),
		agent,
		"caller-session",
		"next-node-turn",
	)
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	var body workspaceAccessBody
	require.NoError(t, json.Unmarshal(approvals[0].RequestBody, &body))
	require.Equal(t, []string{caller.nodeID}, body.Scope.NodeIDs)
	require.Equal(t, "approved", srv.workspaceApprovalStatus(approvals[0]))
}
