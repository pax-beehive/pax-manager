package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentIDFromPath(t *testing.T) {
	t.Run("Given legacy agent path then it returns agent ID", func(t *testing.T) {
		require.Equal(t, "agent_1", agentIDFromPath("/api/user/agents/agent_1"))
	})

	t.Run("Given v1 agent path then it returns agent ID", func(t *testing.T) {
		require.Equal(
			t,
			"agent_1",
			agentIDFromPath("/api/v1/user/usr_1/agents/agent_1"),
		)
	})

	t.Run("Given proxied v1 agent path then it returns agent ID", func(t *testing.T) {
		require.Equal(
			t,
			"agent_75a010ca36ea7543f1268d6e42609a6203df383d0bc5648c",
			agentIDFromPath(
				"/api/pax/api/v1/user/usr_a2c75d7240c95f7ac0531dbcab9cb942f187af00788dfd68/agents/agent_75a010ca36ea7543f1268d6e42609a6203df383d0bc5648c",
			),
		)
	})
}
