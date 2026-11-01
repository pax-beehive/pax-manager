package storage

import (
	"database/sql/driver"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchPriorityGivenBatchSizeThenReservesHistoryAndKeepsLeaseFences(t *testing.T) {
	for _, test := range []struct{ limit, live int }{{1, 1}, {2, 1}, {5, 4}, {20, 16}, {100, 80}, {0, 80}, {101, 80}} {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{columns: []string{"kind", "id", "revision", "token"}, values: [][]driver.Value{}},
			},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		work, err := store.ClaimSearchBatch(t.Context(), test.limit, time.Minute)
		require.NoError(t, err)
		assert.Empty(t, work)
		require.Len(t, script.queryArgs[0], 4)
		assert.EqualValues(t, test.live, script.queryArgs[0][3].Value)
		assert.Contains(t, script.queryTexts[0], "NOT is_backfill")
		assert.Contains(t, script.queryTexts[0], "FOR UPDATE SKIP LOCKED")
		assert.Contains(t, script.queryTexts[0], "available_at<=NOW()")
		assert.Contains(t, script.queryTexts[0], "lease_until<=NOW()")
		assert.True(t, script.committed)
		cleanup()
	}
}
