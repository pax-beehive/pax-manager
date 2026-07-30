package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestDeliveryConversationTurnMetadataGivenWrappedReplyThenDisplayTextUsesReplyBody(
	t *testing.T,
) {
	reply := "西雅图今天（2026年7月5日）天气晴朗，目前约 70°F / 21°C。下午多为晴到少云，最高约 73°F / 23°C；傍晚后转晴，晚间降到约 61-65°F / 16-19°C。整体适合户外活动。"
	delivery := domain.ConversationDelivery{
		Invocation: domain.ConversationAgentInvocation{
			InvocationID:                "inv_1",
			SourceRepresentativeAgentID: "rep_source",
			SourceRuntimeAgentID:        "agent_source",
			SourceSessionID:             "sess_source",
			TargetRepresentativeAgentID: "rep_target",
			TargetRuntimeAgentID:        "agent_target",
			TargetSessionID:             "sess_target",
			Status:                      domain.ConversationAgentInvocationStatusCompleted,
		},
		SourceSession: domain.AgentSession{AgentID: "agent_target", SessionID: "sess_target"},
		TargetSession: domain.AgentSession{AgentID: "agent_source", SessionID: "sess_source"},
	}
	wrapped := agentConversationDeliveryPrompt(delivery, reply)

	meta := deliveryConversationTurnMetadata(delivery, reply, wrapped)

	assert.Equal(t, reply, meta.Content.DisplayText)
	assert.Equal(t, wrapped, meta.Content.OriginalText)
	assert.NotContains(
		t,
		meta.Content.DisplayText,
		"Your Pax conversation inquiry has received a reply",
	)
	assert.Contains(t, meta.Content.OriginalText, "Reply:\n"+reply)
}

func TestAgentConversationMCPServersUsePortablePaxdCommand(t *testing.T) {
	servers := agentConversationMCPServers(domain.AgentSession{
		AgentID:   "agent_1",
		SessionID: "sess_1",
	})

	require.Len(t, servers, 1)
	server, ok := servers[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, agentConversationMCPServerName("sess_1"), server["name"])
	assert.Equal(t, "paxd", server["command"])
	assert.Equal(t, []string{"mcp", "conversation", "serve"}, server["args"])
	assert.Equal(t, []map[string]string{
		{"name": "PAX_AGENT_ID", "value": "agent_1"},
		{"name": "PAX_SESSION_ID", "value": "sess_1"},
	}, server["env"])
}

func TestAgentConversationMCPServerNameIsStableAndSessionScoped(t *testing.T) {
	name := agentConversationMCPServerName("sess_1")

	assert.Equal(t, name, agentConversationMCPServerName("sess_1"))
	assert.NotEqual(t, name, agentConversationMCPServerName("sess_2"))
	assert.Regexp(t, `^pax-conversation-[a-z2-7]{8}$`, name)
}
