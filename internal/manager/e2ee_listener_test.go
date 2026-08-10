package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestE2EENotificationListenerDispatchesFixedChannels(t *testing.T) {
	t.Parallel()
	var commands []string
	var events []string
	listener := &e2eeNotificationListener{
		onCommand: func(agentID string) { commands = append(commands, agentID) },
		onEvent:   func(sessionID string) { events = append(events, sessionID) },
	}

	listener.dispatch(e2eeCommandChannel, "agent_1")
	listener.dispatch(e2eeEventChannel, "session_1")
	listener.dispatch("untrusted_dynamic_channel", "ignored")

	assert.Equal(t, []string{"agent_1"}, commands)
	assert.Equal(t, []string{"session_1"}, events)
}
