package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSchemaGivenEncryptedCanonicalHistoryThenScopesMessagesAndPartsToSession(t *testing.T) {
	t.Parallel()
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	schema := string(initSQL)
	messageStart := strings.Index(schema, "CREATE TABLE IF NOT EXISTS e2ee_messages")
	partStart := strings.Index(schema, "CREATE TABLE IF NOT EXISTS e2ee_message_parts")
	require.NotEqual(t, -1, messageStart)
	require.NotEqual(t, -1, partStart)

	assert.Contains(t, schema[messageStart:partStart], "UNIQUE(agent_id, session_id, message_id)")
	assert.Contains(t, schema[partStart:], "UNIQUE(agent_id, session_id, message_id, part_index)")
	assert.Contains(t, schema[partStart:], "FOREIGN KEY (agent_id, session_id, message_id)")
}

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

func TestMemoryE2EEHistoryGivenRevisionsWhenLoadedThenReturnsLatestMessageWithOrderedParts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 7, 18, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()
	base := E2EERecord{
		RecordID: "record_message_1", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", SessionID: "session_1", Kind: "e2ee_message",
		ProtocolVersion: 1, CipherVersion: 1, KeyEpoch: 1,
		Nonce: []byte("123456789012"), Ciphertext: []byte("header-one"), CreatedAt: now,
	}
	message, updated, err := store.UpsertE2EEMessage(ctx, E2EEMessage{
		MessageID: "message_1", Revision: 1, E2EERecord: base,
	})
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, int64(1), message.ID)

	newer := message
	newer.Revision = 2
	newer.RecordID = "record_message_2"
	newer.Ciphertext = []byte("header-two")
	message, updated, err = store.UpsertE2EEMessage(ctx, newer)
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, int64(1), message.ID)
	assert.Equal(t, int64(2), message.Revision)
	duplicate, updated, err := store.UpsertE2EEMessage(ctx, newer)
	require.NoError(t, err)
	assert.False(t, updated)
	assert.Equal(t, int64(2), duplicate.Revision)
	conflictingMessage := newer
	conflictingMessage.Ciphertext = []byte("conflict")
	_, _, err = store.UpsertE2EEMessage(ctx, conflictingMessage)
	require.ErrorIs(t, err, ErrConflict)
	olderMessage := newer
	olderMessage.Revision = 1
	olderMessage.RecordID = "record_message_old"
	olderMessage.Ciphertext = []byte("old")
	message, updated, err = store.UpsertE2EEMessage(ctx, olderMessage)
	require.NoError(t, err)
	assert.False(t, updated)
	assert.Equal(t, int64(2), message.Revision)

	for _, index := range []int{1, 0} {
		partRecord := base
		partRecord.RecordID = "record_part_" + string(rune('0'+index))
		partRecord.Kind = "e2ee_message_part"
		partRecord.Ciphertext = []byte{byte(index)}
		_, updated, err := store.UpsertE2EEMessagePart(ctx, E2EEMessagePart{
			MessageID: "message_1", PartIndex: index, Revision: 2, E2EERecord: partRecord,
		})
		require.NoError(t, err)
		assert.True(t, updated)
	}
	partRecord := base
	partRecord.RecordID = "record_part_0"
	partRecord.Kind = "e2ee_message_part"
	partRecord.Ciphertext = []byte{0}
	part := E2EEMessagePart{
		MessageID: "message_1", PartIndex: 0, Revision: 2, E2EERecord: partRecord,
	}
	_, updated, err = store.UpsertE2EEMessagePart(ctx, part)
	require.NoError(t, err)
	assert.False(t, updated)
	conflictingPart := part
	conflictingPart.Ciphertext = []byte("conflict")
	_, _, err = store.UpsertE2EEMessagePart(ctx, conflictingPart)
	require.ErrorIs(t, err, ErrConflict)
	newerPart := part
	newerPart.Revision = 3
	newerPart.RecordID = "record_part_3"
	newerPart.Ciphertext = []byte("newer")
	storedPart, updated, err := store.UpsertE2EEMessagePart(ctx, newerPart)
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, int64(3), storedPart.Revision)
	olderPart := newerPart
	olderPart.Revision = 1
	storedPart, updated, err = store.UpsertE2EEMessagePart(ctx, olderPart)
	require.NoError(t, err)
	assert.False(t, updated)
	assert.Equal(t, int64(3), storedPart.Revision)
	missingHeaderPart := part
	missingHeaderPart.MessageID = "missing"
	_, _, err = store.UpsertE2EEMessagePart(ctx, missingHeaderPart)
	require.ErrorIs(t, err, ErrNotFound)

	page, err := store.ListE2EEMessageHistoryPage(ctx, "user_1", "session_1", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	assert.Equal(t, int64(2), page.Messages[0].Message.Revision)
	require.Len(t, page.Messages[0].Parts, 2)
	assert.Equal(t, 0, page.Messages[0].Parts[0].PartIndex)
	assert.Equal(t, 1, page.Messages[0].Parts[1].PartIndex)
	assert.False(t, page.HasMore)

	second := newer
	second.MessageID = "message_2"
	second.RecordID = "record_message_second"
	_, updated, err = store.UpsertE2EEMessage(ctx, second)
	require.NoError(t, err)
	assert.True(t, updated)
	page, err = store.ListE2EEMessageHistoryPage(ctx, "user_1", "session_1", 0, 1)
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	assert.Equal(t, "message_2", page.Messages[0].Message.MessageID)
	assert.True(t, page.HasMore)
	assert.Positive(t, page.NextBeforeID)
}
