package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryMessageSeqIsMonotonicPerSessionAndImmutable(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	})

	newMsg := func(id, session string) *Message {
		return &Message{
			MessageID: id, AgentID: "a", SessionID: session,
			Source:    domain.MessageSourceACPTunnel,
			Direction: domain.MessageDirectionAgentToUser, Role: "assistant",
		}
	}

	a := newMsg("m1", "sess_a")
	require.NoError(t, store.UpsertMessage(ctx, a))
	b := newMsg("m2", "sess_a")
	require.NoError(t, store.UpsertMessage(ctx, b))
	// A different session has its own independent sequence.
	c := newMsg("m3", "sess_b")
	require.NoError(t, store.UpsertMessage(ctx, c))

	assert.Equal(t, int64(1), a.SessionSeq)
	assert.Equal(t, int64(2), b.SessionSeq, "second message in the session advances the seq")
	assert.Equal(t, int64(1), c.SessionSeq, "a different session starts its own sequence")

	// Re-upserting an existing message (e.g. tool_call merge / text append flush)
	// must NOT change or re-advance its seq.
	again := newMsg("m1", "sess_a")
	again.MessageType = "tool_call"
	require.NoError(t, store.UpsertMessage(ctx, again))
	assert.Equal(t, int64(1), again.SessionSeq, "seq is immutable once assigned")

	// And it must not have consumed a new number.
	d := newMsg("m4", "sess_a")
	require.NoError(t, store.UpsertMessage(ctx, d))
	assert.Equal(t, int64(3), d.SessionSeq, "re-upsert did not burn a seq")
}

func TestMemoryListMessageHistoryPageBySeq(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	})
	for i := 1; i <= 5; i++ {
		require.NoError(t, store.UpsertMessage(ctx, &Message{
			MessageID: "m" + string(rune('0'+i)), AgentID: "a", SessionID: "s",
			Source:    domain.MessageSourceACPTunnel,
			Direction: domain.MessageDirectionAgentToUser, Role: "assistant",
		}))
	}

	// Latest page (no cursor): newest 2, ascending, head=5, older exists.
	latest, err := store.ListMessageHistoryPageBySeq(ctx, "a", "s", 0, 0, 2)
	require.NoError(t, err)
	require.Len(t, latest.Messages, 2)
	assert.Equal(t, int64(4), latest.Messages[0].SessionSeq)
	assert.Equal(t, int64(5), latest.Messages[1].SessionSeq)
	assert.Equal(t, int64(5), latest.HeadSeq)
	assert.True(t, latest.HasOlder)
	assert.False(t, latest.HasNewer)
	assert.Equal(t, int64(4), latest.NextBeforeSeq)

	// Scroll back before seq 4 → seq 2,3.
	older, err := store.ListMessageHistoryPageBySeq(ctx, "a", "s", 0, latest.NextBeforeSeq, 2)
	require.NoError(t, err)
	require.Len(t, older.Messages, 2)
	assert.Equal(t, int64(2), older.Messages[0].SessionSeq)
	assert.Equal(t, int64(3), older.Messages[1].SessionSeq)
	assert.True(t, older.HasOlder)
	assert.True(t, older.HasNewer)

	// Catch up forward after seq 3 → seq 4,5 ascending.
	newer, err := store.ListMessageHistoryPageBySeq(ctx, "a", "s", 3, 0, 10)
	require.NoError(t, err)
	require.Len(t, newer.Messages, 2)
	assert.Equal(t, int64(4), newer.Messages[0].SessionSeq)
	assert.Equal(t, int64(5), newer.Messages[1].SessionSeq)
	assert.False(t, newer.HasNewer, "caught up to head")
	assert.True(t, newer.HasOlder)

	// Fully caught up: after head → empty, not behind.
	caught, err := store.ListMessageHistoryPageBySeq(ctx, "a", "s", 5, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, caught.Messages)
	assert.Equal(t, int64(5), caught.HeadSeq)
	assert.False(t, caught.HasNewer)
}

func TestMemoryListConversationHistoryPageBySeq(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	})
	user, err := store.EnsureUser(ctx, "c@example.com", "C", "user")
	require.NoError(t, err)
	agentModel, err := store.RegisterAgent(ctx, user, RegisterAgentRequest{Name: "a", OS: "darwin"}, "hash")
	require.NoError(t, err)
	const convID = "conv_seq"
	// Seed membership directly (package-internal) so the read is authorized.
	store.conversationMembers[convID+":"+user.UserID] = domain.ConversationMember{
		ConversationID: convID, UserID: user.UserID,
	}

	for i := 1; i <= 3; i++ {
		require.NoError(t, store.UpsertMessage(ctx, &Message{
			MessageID: "cm" + string(rune('0'+i)), AgentID: agentModel.AgentID,
			SessionID: "s", ConversationID: convID,
			Source:    domain.MessageSourceACPTunnel,
			Direction: domain.MessageDirectionAgentToUser, Role: "assistant",
		}))
	}

	principal := UserPrincipal{User: user}
	page, err := store.ListConversationHistoryPageBySeq(ctx, principal, convID, 0, 0, 2)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
	assert.Equal(t, int64(2), page.Messages[0].ConversationSeq)
	assert.Equal(t, int64(3), page.Messages[1].ConversationSeq)
	assert.Equal(t, int64(3), page.HeadSeq)
	assert.True(t, page.HasOlder)

	// Access control: a non-member is denied.
	_, err = store.ListConversationHistoryPageBySeq(ctx, UserPrincipal{User: User{UserID: "intruder"}}, convID, 0, 0, 10)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryMessageSeqTracksConversationScopeSeparately(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	})

	mk := func(id, session, conversation string) *Message {
		return &Message{
			MessageID: id, AgentID: "a", SessionID: session, ConversationID: conversation,
			Source:    domain.MessageSourceACPTunnel,
			Direction: domain.MessageDirectionAgentToUser, Role: "assistant",
		}
	}

	// Two sessions feeding one conversation: conversation_seq is monotonic across
	// both, while each session_seq is independent.
	m1 := mk("m1", "sess_a", "conv_1")
	require.NoError(t, store.UpsertMessage(ctx, m1))
	m2 := mk("m2", "sess_b", "conv_1")
	require.NoError(t, store.UpsertMessage(ctx, m2))
	m3 := mk("m3", "sess_a", "conv_1")
	require.NoError(t, store.UpsertMessage(ctx, m3))

	assert.Equal(t, int64(1), m1.ConversationSeq)
	assert.Equal(t, int64(2), m2.ConversationSeq)
	assert.Equal(t, int64(3), m3.ConversationSeq)

	assert.Equal(t, int64(1), m1.SessionSeq)
	assert.Equal(t, int64(1), m2.SessionSeq, "sess_b is its own session scope")
	assert.Equal(t, int64(2), m3.SessionSeq, "sess_a advances independently")
}
