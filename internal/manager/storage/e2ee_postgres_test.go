package storage

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresE2EECommandLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	command := testE2EECommand(now)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{columns: []string{"id", "created_at"}, values: [][]driver.Value{{int64(11), now}}},
		{
			columns: agentCommandColumns(),
			values:  [][]driver.Value{agentCommandValues(command, int64(11))},
		},
		{columns: []string{"connection_epoch"}, values: [][]driver.Value{{int64(3)}}},
		{columns: []string{"connection_epoch"}, values: [][]driver.Value{{int64(3)}}},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	ctx := context.Background()

	created, inserted, err := store.CreateAgentCommand(ctx, command)
	require.NoError(t, err)
	assert.True(t, inserted)
	assert.Equal(t, int64(11), created.ID)
	assert.True(t, script.committed)
	require.Len(t, script.execTexts, 1)
	assert.Contains(t, script.execTexts[0], "pax_agent_commands")

	commands, err := store.ListPendingAgentCommands(ctx, "agent_1", 3, 100)
	require.NoError(t, err)
	require.Len(t, commands, 1)
	assert.Equal(t, "cmd_1", commands[0].RecordID)
	assert.Contains(t, script.queryTexts[1], "delivered_epoch")

	epoch, err := store.RegisterAgentConnection(ctx, "agent_1")
	require.NoError(t, err)
	assert.Equal(t, int64(3), epoch)
	epoch, err = store.CurrentAgentConnectionEpoch(ctx, "agent_1")
	require.NoError(t, err)
	assert.Equal(t, int64(3), epoch)

	require.NoError(t, store.MarkAgentCommandDelivered(ctx, "cmd_1", 3))
	require.NoError(t, store.AcknowledgeAgentCommand(ctx, "cmd_1", 3))
}

func TestPostgresE2EECommandConflictAndFenceFailure(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	command := testE2EECommand(now)
	existing := command
	existing.Ciphertext = []byte("different")
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			{columns: []string{"id", "created_at"}},
			{
				columns: agentCommandColumns(),
				values:  [][]driver.Value{agentCommandValues(existing, int64(11))},
			},
		},
		execResults: []int64{0},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	_, _, err := store.CreateAgentCommand(context.Background(), command)
	require.ErrorIs(t, err, ErrConflict)
	assert.True(t, script.rolled)
	require.ErrorIs(
		t,
		store.MarkAgentCommandDelivered(context.Background(), "cmd_1", 2),
		ErrConflict,
	)
}

func TestPostgresE2EEEventInsertAndCursorRead(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	event := testE2EEEvent(now)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{columns: []string{"cursor", "created_at"}, values: [][]driver.Value{{int64(21), now}}},
		{
			columns: agentEventColumns(),
			values:  [][]driver.Value{agentEventValues(event, int64(21))},
		},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	ctx := context.Background()

	created, inserted, err := store.InsertAgentEvent(ctx, event)
	require.NoError(t, err)
	assert.True(t, inserted)
	assert.Equal(t, int64(21), created.Cursor)
	require.Len(t, script.execTexts, 1)
	assert.Contains(t, script.execTexts[0], "pax_agent_events")

	events, err := store.ListAgentEvents(ctx, "user_1", "session_1", 20, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "event_1", events[0].RecordID)
}

func testE2EECommand(now time.Time) AgentCommand {
	return AgentCommand{E2EERecord: E2EERecord{
		RecordID: "cmd_1", OwnerUserID: "user_1", NodeID: "node_1", AgentID: "agent_1",
		SessionID: "session_1", Kind: "acp_command", ProtocolVersion: 1,
		CipherVersion: 1, KeyEpoch: 1, Nonce: []byte("123456789012"),
		Ciphertext: []byte("ciphertext"), CreatedAt: now,
	}}
}

func testE2EEEvent(now time.Time) AgentEvent {
	return AgentEvent{E2EERecord: E2EERecord{
		RecordID: "event_1", OwnerUserID: "user_1", NodeID: "node_1", AgentID: "agent_1",
		SessionID: "session_1", Kind: "acp_event", ProtocolVersion: 1,
		CipherVersion: 1, KeyEpoch: 1, Nonce: []byte("123456789012"),
		Ciphertext: []byte("ciphertext"), CreatedAt: now,
	}}
}

func agentCommandColumns() []string {
	return []string{"id", "command_id", "owner_user_id", "node_id", "agent_id", "session_id",
		"kind", "protocol_version", "cipher_version", "key_epoch", "nonce", "ciphertext",
		"created_at", "delivered_at", "delivered_epoch", "acknowledged_at",
		"acknowledged_epoch", "expires_at"}
}

func agentCommandValues(command AgentCommand, id int64) []driver.Value {
	return []driver.Value{
		id,
		command.RecordID,
		command.OwnerUserID,
		command.NodeID,
		command.AgentID,
		command.SessionID,
		command.Kind,
		int64(command.ProtocolVersion),
		int64(command.CipherVersion),
		command.KeyEpoch,
		command.Nonce,
		command.Ciphertext,
		command.CreatedAt,
		nil,
		nil,
		nil,
		nil,
		nil,
	}
}

func agentEventColumns() []string {
	return []string{"cursor", "local_id", "owner_user_id", "agent_id", "session_id", "kind",
		"protocol_version", "cipher_version", "key_epoch", "nonce", "ciphertext", "created_at"}
}

func agentEventValues(event AgentEvent, cursor int64) []driver.Value {
	return []driver.Value{cursor, event.RecordID, event.OwnerUserID, event.AgentID, event.SessionID,
		event.Kind, int64(event.ProtocolVersion), int64(event.CipherVersion), event.KeyEpoch,
		event.Nonce, event.Ciphertext, event.CreatedAt}
}
