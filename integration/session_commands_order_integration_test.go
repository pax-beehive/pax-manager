//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestGivenCommandsBeforeCreationReplyThenCatalogBindsWithoutBlockingTunnel(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)
	headers := fixture.userHeaders()
	node := createBoundaryNode(t, fixture, headers, "command-order-node")
	agent := createBoundaryAgent(t, fixture, headers, node.NodeID, "command-order-agent")
	id := fmt.Sprintf("sess-command-order-%d", time.Now().UnixNano())
	base := "/api/v1/user/self/nodes/" + node.NodeID + "/agents/" + agent.Agent.AgentID + "/sessions"
	postJSON[session](t, fixture, base, map[string]any{"session_id": id}, headers, http.StatusOK)
	agentWS := fixture.connectACPAgentTunnel(t, node.APIKey, agent.Agent.AgentID, id)
	t.Cleanup(func() { _ = agentWS.Close() })
	userWS := fixture.connectACPUserTunnel(t, agent.Agent.AgentID, id, headers)
	t.Cleanup(func() { _ = userWS.Close() })
	writeRawWS(t, userWS, mustMarshalString(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{"sessionId": id},
	}))
	request := assertAgentDataContains(t, agentWS, 1, `"method":"session/new"`)
	var rpc struct {
		ID json.RawMessage `json:"id"`
	}
	require.NoError(t, json.Unmarshal(request.Payload, &rpc))
	native := "native-" + id
	writeAgentData(t, agentWS, agent.Agent.AgentID, 1, mustMarshalString(t, map[string]any{
		"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": native, "update": map[string]any{
				"sessionUpdate": "available_commands_update",
				"availableCommands": []map[string]string{
					{"name": "inspect", "description": "Inspect state"},
				},
			},
		},
	}))
	assertAgentAck(t, agentWS, agent.Agent.AgentID, 1)
	writeAgentData(t, agentWS, agent.Agent.AgentID, 2, mustMarshalString(t, map[string]any{
		"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]string{"sessionId": native},
	}))
	assertAgentAck(t, agentWS, agent.Agent.AgentID, 2)
	assertRawWSContains(t, userWS, id)
	response := getJSON[struct {
		Commands *domain.SessionACPCommands `json:"commands"`
	}](t, fixture, base+"/"+id+"/configuration", headers, http.StatusOK)
	require.NotNil(t, response.Commands)
	require.Len(t, response.Commands.AvailableCommands, 1)
	var command struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(response.Commands.AvailableCommands[0], &command))
	assert.Equal(t, "inspect", command.Name)
}
