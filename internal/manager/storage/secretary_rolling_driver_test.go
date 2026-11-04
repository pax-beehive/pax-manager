package storage

import (
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryResetRows() []scriptedRows {
	id := secretarySummaryID("owner_1", "1")
	head := summaryVersionRows(id, "owner_1", 1)
	sources := summarySourceRows()
	sources.values = sources.values[:1]
	return []scriptedRows{
		secretaryMetadataRows(1, 0), summaryEmptyRows(),
		{columns: []string{"id", "revision"}, values: [][]driver.Value{{id, int64(1)}}},
		head, sources, summaryAccessRows("owner_1", "message_1"), summaryEmptyRows(),
	}
}

func TestSecretaryRollingDriverGivenDeletedCoverageThenInvalidatesWithoutReplayingHistory(
	t *testing.T,
) {
	script := &scriptedPostgresScript{queries: secretaryResetRows()}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	work := secretaryDriverWork()
	require.NoError(t, loadSecretaryRollingBaseline(t.Context(), tx, &work))
	assert.True(t, work.ResetRequired)
	assert.Nil(t, work.CurrentSummary)
	assert.Equal(
		t,
		[]domain.SummaryRef{{SummaryID: work.TargetSummaryID, Revision: 1}},
		work.InvalidatedSummaries,
	)
	assert.EqualValues(t, 1, work.ThroughRevision)
	assert.Empty(t, script.execTexts)
}

func TestSecretaryRollingDriverGivenUnavailableBaselineReadThenDoesNotAdvanceCheckpoint(
	t *testing.T,
) {
	for prefix := 0; prefix < len(secretaryResetRows()); prefix++ {
		t.Run(string(rune('a'+prefix)), func(t *testing.T) {
			script := &scriptedPostgresScript{queries: secretaryResetRows()[:prefix]}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			work := secretaryDriverWork()
			assert.Error(t, loadSecretaryRollingBaseline(t.Context(), tx, &work))
			require.NoError(t, tx.Rollback())
			assert.False(t, script.committed)
			assert.Empty(t, script.execTexts)
		})
	}
}

func TestSecretaryRollingDriverGivenProcessedSourceWithoutNodesThenDoesNotRebuild(t *testing.T) {
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			secretaryMetadataRows(1, 0),
			summaryEmptyRows(),
			summaryEmptyRows(),
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	work := secretaryDriverWork()
	require.NoError(t, loadSecretaryRollingBaseline(t.Context(), tx, &work))
	assert.False(t, work.ResetRequired)
	assert.Empty(t, work.CurrentSummaries)
	assert.Empty(t, script.execTexts)
}

func TestSecretaryRollingDriverGivenMalformedMetadataOrNodeThenRejectsRead(t *testing.T) {
	for _, mode := range []string{"metadata", "node"} {
		t.Run(mode, func(t *testing.T) {
			rows := secretaryResetRows()
			if mode == "metadata" {
				rows[0].values[0][2] = []byte("invalid")
			} else {
				rows[2].values[0][0] = nil
			}
			script := &scriptedPostgresScript{queries: rows}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			work := secretaryDriverWork()
			assert.Error(t, loadSecretaryRollingBaseline(t.Context(), tx, &work))
		})
	}
}

func TestSecretaryRollingDriverGivenPublicationGraphUnavailableThenRollsBack(t *testing.T) {
	work := secretaryDriverWork()
	queries := append(
		secretaryGuardRows(),
		summaryEmptyRows(),
		summaryEmptyRows(),
		summaryMessageRows("message_1", "old body", "owner_1"),
	)
	script := &scriptedPostgresScript{queries: queries}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.Error(t, err)
	assert.False(t, script.committed)
	assert.True(t, script.rolled)
	assert.NotContains(t, script.execTexts, "UPDATE secretary_jobs SET processed_revision")
}
