//go:build integration

package integration_test

import (
	"net/http"
	"testing"
)

func TestNodeScopedMessageTenantBoundariesIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)

	ownerHeaders := fixture.userHeaders()
	otherHeaders := map[string]string{"X-User-Email": "other-" + fixture.email}

	nodeA := createBoundaryNode(t, fixture, ownerHeaders, "boundary-node-a")
	nodeB := createBoundaryNode(t, fixture, ownerHeaders, "boundary-node-b")
	agentA := createBoundaryAgent(t, fixture, ownerHeaders, nodeA.NodeID, "boundary-agent-a")
	agentB := createBoundaryAgent(t, fixture, ownerHeaders, nodeA.NodeID, "boundary-agent-b")

	sessionA := "sess-boundary-agent-a"
	sessionB := "sess-boundary-agent-b"
	createBoundarySession(t, fixture, ownerHeaders, nodeA.NodeID, agentA.Agent.AgentID, sessionA)
	createBoundarySession(t, fixture, ownerHeaders, nodeA.NodeID, agentB.Agent.AgentID, sessionB)

	validBody := map[string]any{
		"node_id":      nodeA.NodeID,
		"agent_id":     agentA.Agent.AgentID,
		"session_id":   sessionA,
		"message":      "valid route-owned message",
		"message_type": "chat",
	}

	t.Run(
		"Given another tenant then node scoped session messages are not visible or writable",
		func(t *testing.T) {
			fixture.getExpectError(
				t,
				nodeSessionMessagesPath(nodeA.NodeID, agentA.Agent.AgentID, sessionA),
				otherHeaders,
				http.StatusNotFound,
			)
			fixture.postExpectError(
				t,
				nodeSessionMessagesPath(nodeA.NodeID, agentA.Agent.AgentID, sessionA),
				validBody,
				otherHeaders,
				http.StatusNotFound,
			)
		},
	)

	t.Run(
		"Given a different node path then body node ID cannot cross into that node",
		func(t *testing.T) {
			fixture.postExpectError(
				t,
				nodeSessionMessagesPath(nodeB.NodeID, agentA.Agent.AgentID, sessionA),
				validBody,
				ownerHeaders,
				http.StatusNotFound,
			)
		},
	)

	t.Run(
		"Given a different agent path then body agent ID cannot cross into that agent",
		func(t *testing.T) {
			fixture.postExpectError(
				t,
				nodeSessionMessagesPath(nodeA.NodeID, agentB.Agent.AgentID, sessionA),
				validBody,
				ownerHeaders,
				http.StatusNotFound,
			)
		},
	)

	t.Run(
		"Given a different session path then body session ID cannot cross into that session",
		func(t *testing.T) {
			fixture.postExpectError(
				t,
				nodeSessionMessagesPath(nodeA.NodeID, agentA.Agent.AgentID, sessionB),
				validBody,
				ownerHeaders,
				http.StatusNotFound,
			)
		},
	)

	t.Run("Given matching node agent and session then the message is created", func(t *testing.T) {
		msg := postJSON[mailboxMessage](
			t,
			fixture,
			nodeSessionMessagesPath(nodeA.NodeID, agentA.Agent.AgentID, sessionA),
			map[string]any{
				"node_id":      "node_wrong",
				"agent_id":     agentB.Agent.AgentID,
				"session_id":   sessionB,
				"message":      "route params are authoritative",
				"message_type": "chat",
			},
			ownerHeaders,
			http.StatusOK,
		)
		if msg.MessageID == "" ||
			msg.AgentID != agentA.Agent.AgentID ||
			msg.SessionID != sessionA {
			t.Fatalf("message did not use route scope: %+v", msg)
		}
	})
}

func createBoundaryNode(
	t *testing.T,
	fixture *integrationFixture,
	headers map[string]string,
	name string,
) registerNodeResponse {
	t.Helper()
	token := postJSON[registrationTokenResponse](
		t,
		fixture,
		"/api/v1/user/self/node-registration-tokens",
		map[string]any{"expires_in_seconds": 600},
		headers,
		http.StatusOK,
	)
	node := postJSON[registerNodeResponse](
		t,
		fixture,
		"/api/v1/node/register",
		map[string]any{
			"name":         name,
			"hostname":     name,
			"machine_type": "integration",
			"os":           "linux",
			"arch":         "amd64",
		},
		map[string]string{"X-Registration-Token": token.Token},
		http.StatusOK,
	)
	if node.NodeID == "" || node.APIKey == "" {
		t.Fatalf("bad node response: %+v", node)
	}
	return node
}

func createBoundaryAgent(
	t *testing.T,
	fixture *integrationFixture,
	headers map[string]string,
	nodeID string,
	name string,
) createNodeAgentResponse {
	t.Helper()
	agent := postJSON[createNodeAgentResponse](
		t,
		fixture,
		"/api/v1/user/self/nodes/"+nodeID+"/agents",
		map[string]any{"name": name, "agent_type": "hermes"},
		headers,
		http.StatusOK,
	)
	if agent.Agent.AgentID == "" {
		t.Fatalf("bad agent response: %+v", agent)
	}
	return agent
}

func createBoundarySession(
	t *testing.T,
	fixture *integrationFixture,
	headers map[string]string,
	nodeID string,
	agentID string,
	sessionID string,
) session {
	t.Helper()
	return postJSON[session](
		t,
		fixture,
		"/api/v1/user/self/nodes/"+nodeID+"/agents/"+agentID+"/sessions",
		map[string]any{"session_id": sessionID, "name": sessionID},
		headers,
		http.StatusOK,
	)
}

func nodeSessionMessagesPath(nodeID string, agentID string, sessionID string) string {
	return "/api/v1/user/self/nodes/" + nodeID + "/agents/" + agentID + "/sessions/" +
		sessionID + "/messages"
}
