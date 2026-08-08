package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryE2EECommandLifecycleUsesConnectionFence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()
	epoch, err := store.RegisterAgentConnection(ctx, "agent_1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), epoch)

	command, created, err := store.CreateAgentCommand(ctx, AgentCommand{E2EERecord: E2EERecord{
		RecordID: "cmd_1", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", SessionID: "session_1", Kind: "acp_command",
		ProtocolVersion: 1, CipherVersion: 1, KeyEpoch: 1,
		Nonce: []byte("123456789012"), Ciphertext: []byte("ciphertext"), CreatedAt: now,
	}})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, int64(1), command.ID)

	_, created, err = store.CreateAgentCommand(ctx, command)
	require.NoError(t, err)
	assert.False(t, created)
	conflictingCommand := command
	conflictingCommand.Ciphertext = []byte("different")
	_, _, err = store.CreateAgentCommand(ctx, conflictingCommand)
	require.ErrorIs(t, err, ErrConflict)

	newEpoch, err := store.RegisterAgentConnection(ctx, "agent_1")
	require.NoError(t, err)
	require.Equal(t, int64(2), newEpoch)
	require.ErrorIs(t, store.MarkAgentCommandDelivered(ctx, "cmd_1", epoch), ErrConflict)
	require.NoError(t, store.MarkAgentCommandDelivered(ctx, "cmd_1", newEpoch))
	secondCommand := command
	secondCommand.ID = 0
	secondCommand.RecordID = "cmd_2"
	secondCommand.Ciphertext = []byte("ciphertext-two")
	_, created, err = store.CreateAgentCommand(ctx, secondCommand)
	require.NoError(t, err)
	assert.True(t, created)
	deliverable, err := store.ListPendingAgentCommands(ctx, "agent_1", newEpoch, 100)
	require.NoError(t, err)
	require.Len(t, deliverable, 1)
	assert.Equal(t, "cmd_2", deliverable[0].RecordID)
	require.NoError(t, store.MarkAgentCommandDelivered(ctx, "cmd_2", newEpoch))
	require.NoError(t, store.AcknowledgeAgentCommand(ctx, "cmd_1", newEpoch))
	require.NoError(t, store.AcknowledgeAgentCommand(ctx, "cmd_2", newEpoch))

	pending, err := store.ListPendingAgentCommands(ctx, "agent_1", newEpoch, 100)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestMemoryE2EEEventsAreIdempotentAndCursorOrdered(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()

	first, inserted, err := store.InsertAgentEvent(ctx, AgentEvent{E2EERecord: E2EERecord{
		RecordID: "local_1", OwnerUserID: "user_1", AgentID: "agent_1",
		SessionID: "session_1", Kind: "acp_event", ProtocolVersion: 1,
		CipherVersion: 1, KeyEpoch: 1, Nonce: []byte("123456789012"),
		Ciphertext: []byte("one"), CreatedAt: now,
	}})
	require.NoError(t, err)
	assert.True(t, inserted)
	assert.Equal(t, int64(1), first.Cursor)

	duplicate, inserted, err := store.InsertAgentEvent(ctx, first)
	require.NoError(t, err)
	assert.False(t, inserted)
	assert.Equal(t, first.Cursor, duplicate.Cursor)
	conflictingEvent := first
	conflictingEvent.Ciphertext = []byte("different")
	_, _, err = store.InsertAgentEvent(ctx, conflictingEvent)
	require.ErrorIs(t, err, ErrConflict)

	_, inserted, err = store.InsertAgentEvent(ctx, AgentEvent{E2EERecord: E2EERecord{
		RecordID: "local_2", OwnerUserID: "user_1", AgentID: "agent_1",
		SessionID: "session_1", Kind: "acp_event", ProtocolVersion: 1,
		CipherVersion: 1, KeyEpoch: 1, Nonce: []byte("123456789012"),
		Ciphertext: []byte("two"), CreatedAt: now,
	}})
	require.NoError(t, err)
	assert.True(t, inserted)

	events, err := store.ListAgentEvents(ctx, "user_1", "session_1", first.Cursor, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "local_2", events[0].RecordID)
}
