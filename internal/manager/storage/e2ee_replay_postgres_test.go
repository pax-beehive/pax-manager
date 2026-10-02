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

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPostgresE2EEReplayGivenConcurrentArrivalThenSnapshotAndReconnectDoNotLoseFrames(
	t *testing.T,
) {
	open := pairingPostgresFixture(t)
	db := open()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	ddl := string(raw)
	start := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS agent_events")
	end := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS e2ee_messages")
	_, err = db.ExecContext(t.Context(), ddl[start:end])
	require.NoError(t, err)
	// Run the migration twice to check upgrades are idempotent.
	_, err = db.ExecContext(t.Context(), ddl[start:end])
	require.NoError(t, err)
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO users VALUES ('user');INSERT INTO agents VALUES ('agent')`,
	)
	require.NoError(t, err)
	s := NewPostgresStore(db, time.Now)
	for _, e := range []domain.AgentEvent{replayEvent("legacy", ""), replayEvent("a", "old"), replayEvent("b", "new"), replayEvent("c", "new")} {
		_, _, err = s.InsertAgentEvent(t.Context(), e)
		require.NoError(t, err)
	}
	page, err := s.ReadE2EEReplay(t.Context(), "user", "session", domain.E2EEReplayQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Equal(t, "b", page.Events[0].RecordID)
	assert.True(t, page.HasMore)
	assert.True(t, page.HasOlder)
	e := replayEvent("c", "new")
	_, created, err := s.InsertAgentEvent(t.Context(), e)
	require.NoError(t, err)
	assert.False(t, created)
	e.TurnRef = "wrong"
	_, _, err = s.InsertAgentEvent(t.Context(), e)
	require.ErrorIs(t, err, domain.ErrConflict)
	writer := NewPostgresStore(open(), time.Now)
	late, _, err := writer.InsertAgentEvent(t.Context(), replayEvent("late", "new"))
	require.NoError(t, err)
	tail, err := s.ReadE2EEReplay(
		t.Context(),
		"user",
		"session",
		domain.E2EEReplayQuery{
			TurnRef:       page.TurnRef,
			ThroughCursor: page.HeadCursor,
			AfterCursor:   page.NextAfterCursor,
			Limit:         1,
		},
	)
	require.NoError(t, err)
	require.Len(t, tail.Events, 1)
	assert.Equal(t, "c", tail.Events[0].RecordID)
	assert.False(t, tail.HasMore)
	live, err := s.ListAgentEvents(t.Context(), "user", "session", page.HeadCursor, 100)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, late.Cursor, live[0].Cursor)
	older, err := s.ReadE2EEReplay(
		t.Context(),
		"user",
		"session",
		domain.E2EEReplayQuery{BeforeTurn: page.TurnStartCursor},
	)
	require.NoError(t, err)
	assert.Equal(t, "old", older.TurnRef)
	legacy, err := s.ReadE2EEReplay(
		t.Context(),
		"user",
		"session",
		domain.E2EEReplayQuery{BeforeTurn: older.TurnStartCursor},
	)
	require.NoError(t, err)
	require.Len(t, legacy.Events, 1)
	assert.Empty(t, legacy.TurnRef)
	empty, err := s.ReadE2EEReplay(t.Context(), "other", "session", domain.E2EEReplayQuery{})
	require.NoError(t, err)
	assert.Empty(t, empty.Events)
	assert.Zero(t, empty.HeadCursor)
	empty, err = s.ReadE2EEReplay(
		t.Context(),
		"user",
		"session",
		domain.E2EEReplayQuery{BeforeTurn: 1},
	)
	require.NoError(t, err)
	assert.Empty(t, empty.Events)

	for i, turn := range []string{"indexed", ""} {
		e := replayEvent([]string{"indexed-other", "lifecycle-other"}[i], turn)
		e.SessionID = "other-session"
		_, _, err = s.InsertAgentEvent(t.Context(), e)
		require.NoError(t, err)
	}
	latest, err := s.ReadE2EEReplay(t.Context(), "user", "other-session", domain.E2EEReplayQuery{})
	require.NoError(t, err)
	assert.Equal(t, "indexed", latest.TurnRef)
	require.Len(t, latest.Events, 1)
	assert.Greater(t, latest.HeadCursor, latest.Events[0].Cursor)

	// Hold a lower event cursor uncommitted. Later writers must wait, otherwise
	// a reader could advance past that cursor and permanently miss its commit.
	blocker := open()
	tx, err := blocker.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(
		t.Context(),
		`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"pax:e2ee:session",
	)
	require.NoError(t, err)
	_, err = tx.ExecContext(
		t.Context(),
		`INSERT INTO agent_events(owner_user_id,agent_id,session_id,local_id,kind,protocol_version,cipher_version,key_epoch,nonce,ciphertext,turn_ref) VALUES ('user','agent','session','uncommitted','acp_event',1,1,1,'nonce','cipher','new')`,
	)
	require.NoError(t, err)
	result := make(chan error, 1)
	concurrent := NewPostgresStore(open(), time.Now)
	ctx, cancelWriter := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWriter()
	go func() {
		_, _, err := concurrent.InsertAgentEvent(ctx, replayEvent("after-commit", "new"))
		result <- err
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event='advisory' AND query LIKE '%pg_advisory_xact_lock%')`).
			Scan(&waiting)
		return err == nil && waiting
	}, time.Second, 10*time.Millisecond)
	beforeCommit, err := s.ReadE2EEReplay(
		t.Context(),
		"user",
		"session",
		domain.E2EEReplayQuery{Limit: 100},
	)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-result)
	recovered, err := s.ListAgentEvents(
		t.Context(),
		"user",
		"session",
		beforeCommit.HeadCursor,
		100,
	)
	require.NoError(t, err)
	require.Len(t, recovered, 2)
	assert.Equal(t, "uncommitted", recovered[0].RecordID)
	assert.Equal(t, "after-commit", recovered[1].RecordID)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.ReadE2EEReplay(cancelled, "user", "session", domain.E2EEReplayQuery{})
	require.Error(t, err)
}
