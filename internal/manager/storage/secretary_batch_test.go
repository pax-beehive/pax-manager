package storage

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryDriverGivenInterleavedInternalEventsThenFillsCanonicalBatch(t *testing.T) {
	queries := []scriptedRows{
		secretaryCandidateRows(),
		secretaryMetadataRows(0, 0),
		summaryEmptyRows(),
		summaryEmptyRows(),
		summaryEmptyRows(),
	}
	for page := 0; page < 4; page++ {
		changes := secretaryChangesRows()
		var messages []scriptedRows
		for offset := 1; offset <= 128; offset++ {
			revision := int64(page*128 + offset)
			id := fmt.Sprintf("message_%04d", revision)
			visible := offset%8 == 0
			changes.values = append(changes.values, []driver.Value{id, revision, "", "", visible})
			if visible {
				messages = append(messages, summaryMessageRows(id, "visible body", "owner_1"))
			}
		}
		queries = append(queries, changes)
		queries = append(queries, messages...)
	}
	queries = append(queries, summaryEmptyRows(), secretaryGuidanceRows())
	script := &scriptedPostgresScript{queries: queries}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Len(t, jobs[0].Messages, domain.SecretaryMaxMessages)
	assert.EqualValues(t, 512, jobs[0].ThroughRevision)
	assert.Equal(t, "message_0008", jobs[0].Messages[0].SourceID)
	assert.Equal(t, "message_0512", jobs[0].Messages[63].SourceID)
	assert.Len(t, script.queryTexts, 6+4+64+1)
}

func TestSecretaryMemoryGivenManyHiddenChangesThenBoundsScanAndMakesProgress(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	delete(store.messages, "m1")
	for i := 1; i <= 1100; i++ {
		id := fmt.Sprintf("hidden_%04d", i)
		secretaryMemoryMessage(store, id, "private reasoning", int64(i))
		message := store.messages[id]
		message.MessageType = "agent_thought_chunk"
		store.messages[id] = message
	}
	secretaryMemoryMessage(store, "visible", "readable", 1101)
	secretaryEnable(t, store)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	assert.EqualValues(t, 1024, store.secretaryState.jobs["1"].processed)
	work := secretaryClaim(t, store)
	require.Len(t, work.Messages, 1)
	assert.Equal(t, "visible", work.Messages[0].SourceID)
	assert.Equal(t, store.secretaryState.jobs["1"].desired, work.ThroughRevision)
}

func TestSecretaryMemoryGivenMoreThanOneBatchOfRemovalsThenDoesNotSkipRemainder(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := store.secretaryState.jobs["1"]
	job.changes = map[string]int64{}
	job.checkpoint = map[string]string{}
	for i := 1; i <= 70; i++ {
		id := fmt.Sprintf("removed_%03d", i)
		job.changes[id] = int64(i)
		job.checkpoint[id] = "previously visible"
	}
	job.desired = 70
	work, err := store.loadMemorySecretaryJobLocked(job)
	require.NoError(t, err)
	assert.Len(t, work.RemovedMessageIDs, domain.SecretaryMaxMessages)
	assert.EqualValues(t, 64, work.ThroughRevision)
	job.processed = work.ThroughRevision
	store.secretaryState.summaries[work.TargetSummaryID] = []domain.SummaryVersion{{
		SummaryID: work.TargetSummaryID, OwnerUserID: "owner", Revision: 1,
	}}
	work, err = store.loadMemorySecretaryJobLocked(job)
	require.NoError(t, err)
	assert.Len(t, work.RemovedMessageIDs, 6)
	assert.EqualValues(t, 70, work.ThroughRevision)
}

func TestSecretaryDriverGivenByteLimitThenKeepsNextVisibleChangeUnprocessed(t *testing.T) {
	queries := []scriptedRows{
		secretaryCandidateRows(), secretaryMetadataRows(0, 0), summaryEmptyRows(), summaryEmptyRows(),
		summaryEmptyRows(),
		secretaryChangesRows(
			[]driver.Value{"first", int64(1), "", "", true},
			[]driver.Value{"hidden", int64(2), "", "", false},
			[]driver.Value{"next", int64(3), "", "", true},
		),
		summaryMessageRows("first", strings.Repeat("x", domain.SecretaryMaxBytes-1), "owner_1"),
		summaryMessageRows("next", "too large for remaining bytes", "owner_1"),
		summaryEmptyRows(),
		secretaryGuidanceRows(),
	}
	script := &scriptedPostgresScript{queries: queries}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	work := secretaryClaim(t, store)
	require.Len(t, work.Messages, 1)
	assert.Equal(t, "first", work.Messages[0].SourceID)
	assert.EqualValues(t, 2, work.ThroughRevision)
}

func TestSecretaryDriverGivenOnlyInvisibleChangesThenBoundsQueriesAndAdvancesCheckpoint(
	t *testing.T,
) {
	queries := []scriptedRows{
		secretaryCandidateRows(),
		secretaryMetadataRows(0, 0),
		summaryEmptyRows(),
		summaryEmptyRows(),
		summaryEmptyRows(),
	}
	for page := 0; page < secretaryChangeScanLimit/secretaryChangePageSize; page++ {
		changes := secretaryChangesRows()
		for offset := 1; offset <= secretaryChangePageSize; offset++ {
			revision := int64(page*secretaryChangePageSize + offset)
			changes.values = append(changes.values, []driver.Value{
				fmt.Sprintf("hidden_%04d", revision), revision, "", "", false,
			})
		}
		queries = append(queries, changes)
	}
	queries = append(queries, secretaryOpenBatchRows(false))
	script := &scriptedPostgresScript{queries: queries}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	assert.Len(t, script.queryTexts, 6+secretaryChangeScanLimit/secretaryChangePageSize)
	require.Len(t, script.execArgs, 2)
	assert.EqualValues(t, secretaryChangeScanLimit, script.execArgs[1][1].Value)
}

func TestSecretaryDriverGivenPreviouslyVisibleMessagesNowHiddenThenBoundsRemovalCheckpoint(
	t *testing.T,
) {
	changes := secretaryChangesRows()
	for i := 1; i <= 70; i++ {
		changes.values = append(changes.values, []driver.Value{
			fmt.Sprintf(
				"removed_%03d",
				i,
			), int64(i), "old hash", domain.SearchCanonicalizationVersion, false,
		})
	}
	script := &scriptedPostgresScript{queries: []scriptedRows{
		secretaryCandidateRows(), secretaryMetadataRows(0, 0), summaryEmptyRows(), summaryEmptyRows(), summaryEmptyRows(), changes, summaryEmptyRows(), secretaryGuidanceRows(),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	work := secretaryClaim(t, store)
	assert.Empty(t, work.Messages)
	assert.Len(t, work.RemovedMessageIDs, domain.SecretaryMaxMessages)
	assert.EqualValues(t, 64, work.ThroughRevision)
}

func TestSecretaryMemoryGivenFilledCanonicalBatchWhenRetriedThenPreservesExecutionIdentity(
	t *testing.T,
) {
	store, now := secretaryMemoryFixture(t)
	for i := 2; i <= 600; i++ {
		id := fmt.Sprintf("message_%04d", i)
		secretaryMemoryMessage(store, id, "body", int64(i))
		if i%8 != 0 {
			message := store.messages[id]
			message.MessageType = "agent_thought_chunk"
			store.messages[id] = message
		}
	}
	secretaryEnable(t, store)
	first := secretaryClaim(t, store)
	require.Len(t, first.Messages, domain.SecretaryMaxMessages)
	require.NoError(
		t,
		store.FailSecretaryJob(t.Context(), first, errors.New("temporary unavailable")),
	)
	*now = now.Add(6 * time.Second)
	retry := secretaryClaim(t, store)
	assert.Equal(t, first.RunID, retry.RunID)
	assert.Equal(t, first.ThroughRevision, retry.ThroughRevision)
	assert.Equal(t, first.Messages, retry.Messages)
}

func TestSecretaryBatchGivenForeignOrUnavailableSourceThenFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		doc   domain.SearchDocument
		err   error
		want  error
		moved bool
	}{
		{name: "foreign owner", doc: domain.SearchDocument{OwnerUserID: "other"}, want: ErrNotFound},
		{name: "read failure", err: domain.ErrConflict, want: domain.ErrConflict},
		{name: "moved source", doc: domain.SearchDocument{OwnerUserID: "owner", AgentID: "other"}, moved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := domain.SecretaryJob{
				OwnerUserID:     "owner",
				SourceAgentID:   "source",
				ThroughRevision: 4,
			}
			batch := newSecretaryBatch(&work)
			consumed, err := batch.consume(
				secretaryChange{id: "message", revision: 5},
				tc.doc,
				tc.err,
			)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				assert.False(t, consumed)
				assert.EqualValues(t, 4, work.ThroughRevision)
				return
			}
			require.NoError(t, err)
			assert.True(t, consumed)
			assert.Equal(t, []string{"message"}, work.RemovedMessageIDs)
		})
	}
}
