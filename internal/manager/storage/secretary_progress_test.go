package storage

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryProgress(
	t *testing.T,
	store domain.SecretaryProgressStore,
) domain.SecretaryProgressPage {
	t.Helper()
	page, err := store.ReadSecretaryProgress(t.Context(), "owner", domain.SecretaryProgressQuery{})
	require.NoError(t, err)
	return page
}

func TestSecretaryProgressGivenCurrentCoverageThenReportsSessionsMessagesAndKnownTurns(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryMemoryMessage(store, "m2", "second", 2)
	for _, id := range []string{"m1", "m2"} {
		message := store.messages[id]
		message.TurnID = "turn-1"
		store.messages[id] = message
	}
	initial := secretaryProgress(t, store)
	assert.Equal(t, 1, initial.Counts.UnstartedSessions)
	assert.Equal(t, 2, initial.Counts.PendingMessages)
	assert.Equal(t, 1, initial.Counts.PendingTurns)
	assert.Zero(t, initial.Counts.UngroupedMessages)

	cfg := secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
	require.NoError(t, err)
	complete := secretaryProgress(t, store)
	assert.Equal(t, 1, complete.Counts.CompletedSessions)
	assert.Equal(t, 2, complete.Counts.CompletedMessages)
	assert.Equal(t, 1, complete.Counts.CompletedTurns)
	require.Len(t, complete.Sessions, 1)
	assert.Equal(t, "completed", complete.Sessions[0].State)
	assert.NotNil(t, complete.Sessions[0].LastCompletedAt)

	secretaryMemoryMessage(store, "m3", "new turn", 3)
	message := store.messages["m3"]
	message.TurnID = "turn-2"
	store.messages["m3"] = message
	partial := secretaryProgress(t, store)
	assert.Equal(t, 1, partial.Counts.PartialSessions)
	assert.Equal(t, 2, partial.Counts.CompletedMessages)
	assert.Equal(t, 1, partial.Counts.PendingMessages)
	assert.Equal(t, 1, partial.Counts.PendingTurns)
	assert.Equal(t, "partial", partial.Sessions[0].State)

	_, err = store.UpdateSecretarySettings(t.Context(), "owner", domain.SecretarySettingsUpdate{
		AgentID: "secretary", ExpectedRevision: cfg.Revision,
	})
	require.NoError(t, err)
	store.messageParts[messagePartKey{MessageID: "m1", Index: 1}] = MessagePart{
		MessageID: "m1", PartIndex: 1, PartType: "text", Text: " appended while disabled",
	}
	changed := secretaryProgress(t, store)
	assert.Equal(t, 1, changed.Counts.CompletedMessages)
	assert.Equal(t, 2, changed.Counts.PendingTurns)
	assert.Zero(t, changed.Counts.CompletedSessions)
	delete(store.messageParts, messagePartKey{MessageID: "m1", Index: 1})
	delete(store.messages, "m3")
	message = store.messages["m2"]
	message.TurnID = ""
	store.messages["m2"] = message
	current := secretaryProgress(t, store)
	assert.Equal(t, 1, current.Counts.CompletedSessions)
	assert.Equal(t, 1, current.Counts.TotalTurns)
	assert.Equal(t, 1, current.Counts.UngroupedMessages)
}

func TestSecretaryProgressGivenPaginationThenCountsRemainGlobalAndCursorIsOwnerBound(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	for _, id := range []string{"2", "3"} {
		store.sessions[sessionKey("source", id)] = AgentSession{
			ID:        int64(id[0] - '0'),
			AgentID:   "source",
			SessionID: id,
		}
		secretaryMemoryMessage(store, "m"+id, "body", 1)
		message := store.messages["m"+id]
		message.SessionID = id
		store.messages[message.MessageID] = message
	}
	page, err := store.ReadSecretaryProgress(
		t.Context(),
		"owner",
		domain.SecretaryProgressQuery{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Sessions, 1)
	assert.Equal(t, 3, page.Counts.TotalSessions)
	assert.Equal(t, "1", page.Sessions[0].SourceID)
	require.NotEmpty(t, page.NextCursor)
	next, err := store.ReadSecretaryProgress(
		t.Context(),
		"owner",
		domain.SecretaryProgressQuery{Limit: 1, Cursor: page.NextCursor},
	)
	require.NoError(t, err)
	assert.Equal(t, page.Counts, next.Counts)
	assert.Equal(t, "2", next.Sessions[0].SourceID)
	last, err := store.ReadSecretaryProgress(
		t.Context(),
		"owner",
		domain.SecretaryProgressQuery{Limit: 100, Cursor: next.NextCursor},
	)
	require.NoError(t, err)
	require.Len(t, last.Sessions, 1)
	assert.Empty(t, last.NextCursor)
	_, err = store.ReadSecretaryProgress(
		t.Context(),
		"other",
		domain.SecretaryProgressQuery{Cursor: page.NextCursor},
	)
	assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	_, err = store.ReadSecretaryProgress(t.Context(), "", domain.SecretaryProgressQuery{})
	assert.ErrorIs(t, err, ErrUnauthorized)
	for _, query := range []domain.SecretaryProgressQuery{{Limit: -1}, {Limit: 101}, {Cursor: "!"}, {Cursor: strings.Repeat("a", 2049)}, {Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"owner":"x","after":"1"}`))}} {
		_, err = store.ReadSecretaryProgress(t.Context(), "owner", query)
		assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	}
}

func TestSecretaryProgressGivenHiddenSourcesThenOnlyCurrentOwnerVisiblePlaintextCounts(
	t *testing.T,
) {
	for _, reason := range []string{"archived", "internal-marker", "internal-prefix", "registered", "foreign", "missing-node", "reasoning", "tool", "empty", "replaced"} {
		t.Run(reason, func(t *testing.T) {
			store, now := secretaryMemoryFixture(t)
			session := store.sessions[sessionKey("source", "session")]
			message := store.messages["m1"]
			switch reason {
			case "archived":
				session.ArchivedAt = now
			case "internal-marker":
				session.Source = domain.SecretaryInternalSource
			case "internal-prefix":
				delete(store.sessions, sessionKey("source", "session"))
				session.SessionID = "sess_secretary_private"
				message.SessionID = session.SessionID
			case "registered":
				store.secretaryLocked().internal[sessionKey("source", "session")] = true
			case "foreign":
				agent := store.agents["source"]
				agent.OwnerUserID = "other"
				store.agents["source"] = agent
			case "missing-node":
				session.NodeID = "deleted-node"
			case "reasoning":
				message.RawJSON = json.RawMessage(`{"channel":"analysis"}`)
			case "tool":
				message.Role = "tool"
			case "empty":
				delete(store.messageParts, messagePartKey{MessageID: "m1", Index: 0})
			case "replaced":
				store.messages["replacement"] = Message{
					MessageID:       "replacement",
					AgentID:         "source",
					SessionID:       "session",
					MessageType:     domain.MessageTypePaxInvocation,
					ParentMessageID: "m1",
				}
			}
			store.sessions[sessionKey("source", session.SessionID)] = session
			store.messages["m1"] = message
			page := secretaryProgress(t, store)
			assert.Zero(t, page.Counts.TotalSessions)
			assert.Empty(t, page.Sessions)
		})
	}
}

func TestSecretaryProgressGivenTransferredAgentThenPriorOwnerCheckpointCannotCount(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
	require.NoError(t, err)
	agent := store.agents["source"]
	agent.OwnerUserID = "other"
	store.agents["source"] = agent
	page, err := store.ReadSecretaryProgress(t.Context(), "other", domain.SecretaryProgressQuery{})
	require.NoError(t, err)
	assert.Equal(t, 1, page.Counts.UnstartedSessions)
	assert.Equal(t, 1, page.Counts.PendingMessages)
	assert.Nil(t, page.Sessions[0].LastCompletedAt)
	assert.Zero(t, secretaryProgress(t, store).Counts.TotalSessions)
}
