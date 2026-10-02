package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestE2EEReplayGivenTwoTurnsWhenOpeningThenPagesTheEntireLatestTurn(t *testing.T) {
	s := NewMemoryStore(time.Now)
	for i, turn := range []string{"old", "old", "new", "new", "new"} {
		_, _, err := s.InsertAgentEvent(t.Context(), replayEvent(fmt.Sprint(i), turn))
		require.NoError(t, err)
	}
	page, err := s.ReadE2EEReplay(t.Context(), "user", "session", domain.E2EEReplayQuery{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	assert.Equal(t, "new", page.TurnRef)
	assert.Equal(t, int64(3), page.TurnStartCursor)
	assert.True(t, page.HasMore)
	assert.True(t, page.HasOlder)
	assert.Equal(t, int64(5), page.HeadCursor)
	// A new frame arriving during replay is delivered by the subsequent SSE, not this snapshot.
	_, _, err = s.InsertAgentEvent(t.Context(), replayEvent("late", "new"))
	require.NoError(t, err)
	tail, err := s.ReadE2EEReplay(
		t.Context(),
		"user",
		"session",
		domain.E2EEReplayQuery{
			Limit:         2,
			TurnRef:       page.TurnRef,
			AfterCursor:   page.NextAfterCursor,
			ThroughCursor: page.HeadCursor,
		},
	)
	require.NoError(t, err)
	require.Len(t, tail.Events, 1)
	assert.Equal(t, "4", tail.Events[0].RecordID)
	assert.False(t, tail.HasMore)
	live, err := s.ListAgentEvents(t.Context(), "user", "session", page.HeadCursor, 100)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "late", live[0].RecordID)
	older, err := s.ReadE2EEReplay(
		t.Context(),
		"user",
		"session",
		domain.E2EEReplayQuery{Limit: 10, BeforeTurn: page.TurnStartCursor},
	)
	require.NoError(t, err)
	assert.Equal(t, "old", older.TurnRef)
	assert.Len(t, older.Events, 2)
	assert.False(t, older.HasOlder)
}

func TestE2EEReplayGivenDuplicateOrForeignEventsThenDoesNotDuplicateOrLeak(t *testing.T) {
	s := NewMemoryStore(time.Now)
	e := replayEvent("e", "turn")
	_, _, err := s.InsertAgentEvent(t.Context(), e)
	require.NoError(t, err)
	_, created, err := s.InsertAgentEvent(t.Context(), e)
	require.NoError(t, err)
	assert.False(t, created)
	e.TurnRef = "changed"
	_, _, err = s.InsertAgentEvent(t.Context(), e)
	require.ErrorIs(t, err, domain.ErrConflict)
	page, err := s.ReadE2EEReplay(
		t.Context(),
		"other",
		"session",
		domain.E2EEReplayQuery{Limit: 100},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Events)
	assert.Zero(t, page.HeadCursor)
}

func TestE2EEReplayGivenLegacyFramesThenRetainsAReadableFallback(t *testing.T) {
	s := NewMemoryStore(time.Now)
	_, _, err := s.InsertAgentEvent(t.Context(), replayEvent("legacy", ""))
	require.NoError(t, err)
	_, _, err = s.InsertAgentEvent(t.Context(), replayEvent("new", "turn"))
	require.NoError(t, err)
	page, err := s.ReadE2EEReplay(
		t.Context(),
		"user",
		"session",
		domain.E2EEReplayQuery{Limit: 100, BeforeTurn: 2},
	)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Empty(t, page.TurnRef)
	assert.Equal(t, "legacy", page.Events[0].RecordID)
}

func TestE2EEReplayGivenUnindexedLifecycleTailThenOpensLatestIndexedTurn(t *testing.T) {
	s := NewMemoryStore(time.Now)
	for i, turn := range []string{"", "old", "new", ""} {
		_, _, err := s.InsertAgentEvent(t.Context(), replayEvent(fmt.Sprint(i), turn))
		require.NoError(t, err)
	}
	page, err := s.ReadE2EEReplay(t.Context(), "user", "session", domain.E2EEReplayQuery{})
	require.NoError(t, err)
	assert.Equal(t, "new", page.TurnRef)
	assert.Equal(t, int64(4), page.HeadCursor)
}

func replayEvent(id, turn string) domain.AgentEvent {
	return domain.AgentEvent{
		TurnRef: turn,
		E2EERecord: domain.E2EERecord{
			RecordID:        id,
			OwnerUserID:     "user",
			AgentID:         "agent",
			SessionID:       "session",
			Kind:            "acp_event",
			ProtocolVersion: 1,
			CipherVersion:   1,
			KeyEpoch:        1,
			Nonce:           []byte("123456789012"),
			Ciphertext:      []byte("opaque ciphertext"),
			CreatedAt:       time.Now(),
		},
	}
}
