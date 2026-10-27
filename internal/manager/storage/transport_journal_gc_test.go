package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGivenCleanupWhenPruningThenKeepWatermarksTailAndBoundedBatch(t *testing.T) {
	script := &scriptedPostgresScript{execResults: []int64{9}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	cutoff := time.Now().UTC().Add(-72 * time.Hour)
	deleted, err := store.PruneCompletedTransportFrames(context.Background(), cutoff, 1000, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(9), deleted)
	require.Len(t, script.execTexts, 1)
	sql := script.execTexts[0]
	assert.Contains(t, sql, "inbound_acked_through")
	assert.Contains(t, sql, "inbound_applied_through")
	assert.Contains(t, sql, "next_outbound_seq")
	assert.Contains(t, sql, "SKIP LOCKED")
	assert.Contains(t, sql, "LIMIT $2")
	assert.Contains(t, sql, "updated_at < $1")
	assert.Equal(t, cutoff, script.execArgs[0][0].Value)
}

func TestGivenInvalidCleanupParametersThenNoDelete(t *testing.T) {
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	for _, tc := range []struct {
		cutoff time.Time
		limit  int
		tail   int64
	}{
		{time.Time{}, 1000, 100}, {time.Now(), 0, 100},
		{time.Now(), 10001, 100}, {time.Now(), 1000, -1},
	} {
		_, err := store.PruneCompletedTransportFrames(
			context.Background(),
			tc.cutoff,
			tc.limit,
			tc.tail,
		)
		require.Error(t, err)
	}
	assert.Empty(t, script.execTexts)
}

func TestGivenDatabaseUnavailableThenReportCleanupFailure(t *testing.T) {
	store, cleanup := scriptedPostgresStore(t, &scriptedPostgresScript{})
	defer cleanup()
	require.NoError(t, store.db.Close())
	_, err := store.PruneCompletedTransportFrames(context.Background(), time.Now(), 1000, 100)
	require.Error(t, err)
}
