package storage

import (
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secretaryResetRows() []scriptedRows {
	head := summaryVersionRows(secretarySummaryID("owner_1", "1"), "owner_1", 1)
	sources := summarySourceRows()
	sources.values = sources.values[:1]
	return []scriptedRows{
		secretaryMetadataRows(1, 0), head, sources,
		summaryAccessRows("owner_1", "message_1"), summaryEmptyRows(), summaryEmptyRows(),
		{columns: []string{"id"}, values: [][]driver.Value{{"message_2"}}},
		summaryMessageRows("message_2", "retained", "owner_1"),
		{columns: []string{"revision"}, values: [][]driver.Value{{int64(3)}}},
	}
}

func TestSecretaryRollingDriverGivenDeletedCoverageThenRequeuesCurrentVisibleSources(t *testing.T) {
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
	assert.Equal(t, []string{"message_1"}, work.RemovedMessageIDs)
	assert.EqualValues(t, 3, work.ThroughRevision)
	require.Len(t, script.execTexts, 2)
	assert.Contains(t, script.execTexts[0], "processed_hash=''")
	assert.Contains(t, script.execTexts[1], "secretary_message_state")
}

func TestSecretaryRollingDriverGivenUnavailableResetReadThenDoesNotAdvanceCheckpoint(t *testing.T) {
	for prefix := 0; prefix < len(secretaryResetRows()); prefix++ {
		name := []string{"metadata", "head", "sources", "access", "current_source", "removed_source", "reset_sources", "rebuild_source", "revision"}[prefix]
		t.Run(name, func(t *testing.T) {
			rows := secretaryResetRows()[:prefix]
			script := &scriptedPostgresScript{queries: rows}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			work := secretaryDriverWork()
			assert.Error(t, loadSecretaryRollingBaseline(t.Context(), tx, &work))
			require.NoError(t, tx.Rollback())
			assert.False(t, script.committed)
		})
	}
}

func TestSecretaryRollingDriverGivenLegacyOrResetMarkerThenAvoidsRepeatedBackfill(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "first", true: "retry"}[retry], func(t *testing.T) {
			marker := int64(0)
			if retry {
				marker = -1
			}
			rows := []scriptedRows{secretaryMetadataRows(1, marker), summaryEmptyRows()}
			if !retry {
				rows = append(
					rows,
					scriptedRows{columns: []string{"id"}, values: [][]driver.Value{{"message_1"}}},
					summaryMessageRows("message_1", "old body", "owner_1"),
					scriptedRows{
						columns: []string{"revision"},
						values:  [][]driver.Value{{int64(3)}},
					},
				)
			}
			script := &scriptedPostgresScript{queries: rows}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			work := secretaryDriverWork()
			work.Messages = nil
			require.NoError(t, loadSecretaryRollingBaseline(t.Context(), tx, &work))
			assert.True(t, work.ResetRequired)
			assert.Equal(t, "rolling_summary_rebuild", work.ResetReason)
			assert.Empty(t, work.Messages)
			assert.Nil(t, work.CurrentSummary)
			if retry {
				assert.Empty(t, script.execTexts)
			}
		})
	}
}

func TestSecretaryRollingDriverGivenMalformedMetadataOrResetSourcesThenRejectsRead(t *testing.T) {
	for _, mode := range []string{"metadata", "source_scan", "foreign_source", "hidden_source"} {
		t.Run(mode, func(t *testing.T) {
			rows := secretaryResetRows()
			switch mode {
			case "metadata":
				rows[0].values[0][2] = []byte("invalid")
			case "source_scan":
				rows[6].values[0][0] = nil
			case "foreign_source":
				rows[7] = summaryMessageRows("message_2", "retained", "other")
			case "hidden_source":
				rows[7] = summaryEmptyRows()
			}
			script := &scriptedPostgresScript{queries: rows}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			work := secretaryDriverWork()
			err = loadSecretaryRollingBaseline(t.Context(), tx, &work)
			if mode == "hidden_source" {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
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
