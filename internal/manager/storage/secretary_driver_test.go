package storage

import (
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryCandidateRows() scriptedRows {
	return scriptedRows{
		columns: []string{
			"source",
			"owner",
			"source_agent",
			"session",
			"agent",
			"settings_revision",
			"through",
			"retry",
		},
		values: [][]driver.Value{
			{"1", "owner_1", "agent_1", "session_1", "selected", int64(1), int64(2), int64(0)},
		},
	}
}

func secretaryChangesRows(values ...[]driver.Value) scriptedRows {
	return scriptedRows{
		columns: []string{"message", "revision", "hash", "canonical"},
		values:  values,
	}
}

func TestSecretaryDriverGivenClaimedSourceWhenLoadedThenReturnsBoundedCanonicalWork(t *testing.T) {
	for _, mode := range []string{"new", "existing", "noop", "deleted", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			changes := secretaryChangesRows([]driver.Value{"message_1", int64(1), "", ""})
			message := summaryMessageRows("message_1", "old body", "owner_1")
			queries := []scriptedRows{secretaryCandidateRows(), changes, message}
			switch mode {
			case "new":
				queries = append(queries, summaryEmptyRows(), summaryEmptyRows())
			case "existing":
				queries = append(
					queries,
					scriptedRows{
						columns: []string{"id", "revision"},
						values:  [][]driver.Value{{"existing", int64(1)}},
					},
					scriptedRows{
						columns: []string{"id"},
						values:  [][]driver.Value{{"message_1"}, {"message_2"}},
					},
					message,
					summaryMessageRows("message_2", "other covered body", "owner_1"),
					summaryEmptyRows(),
				)
			case "noop":
				queries[1] = secretaryChangesRows(
					[]driver.Value{
						"message_1",
						int64(1),
						domain.SearchContentHash("old body"),
						domain.SearchCanonicalizationVersion,
					},
				)
				queries = append(queries, summaryEmptyRows())
			case "deleted":
				queries[2] = summaryEmptyRows()
				queries = append(queries, summaryEmptyRows())
			case "oversized":
				queries[2] = summaryMessageRows(
					"message_1",
					strings.Repeat("x", domain.SecretaryMaxBytes+1),
					"owner_1",
				)
				queries = append(queries, summaryEmptyRows())
			}
			script := &scriptedPostgresScript{queries: queries}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
			require.NoError(t, err)
			assert.True(t, script.committed)
			if mode == "noop" || mode == "deleted" || mode == "oversized" {
				assert.Empty(t, jobs)
				return
			}
			require.Len(t, jobs, 1)
			assert.NotEmpty(t, jobs[0].LeaseToken)
			assert.NotEmpty(t, jobs[0].RunID)
			assert.EqualValues(t, 1, jobs[0].ThroughRevision)
			if mode == "existing" {
				assert.Equal(t, "existing", jobs[0].TargetSummaryID)
				assert.Len(t, jobs[0].Messages, 2)
			} else {
				assert.Equal(t, "old body", jobs[0].Messages[0].Body)
			}
		})
	}
}

func secretaryDriverWork() domain.SecretaryJob {
	work := domain.SecretaryJob{
		SourceID:         "1",
		OwnerUserID:      "owner_1",
		SourceAgentID:    "agent_1",
		SourceSessionID:  "session_1",
		AgentID:          "selected",
		LeaseToken:       "lease",
		SettingsRevision: 1,
		ThroughRevision:  1,
		Messages: []domain.SearchDocument{
			{
				SourceID:                "message_1",
				ContentHash:             domain.SearchContentHash("old body"),
				CanonicalizationVersion: domain.SearchCanonicalizationVersion,
				Body:                    "old body",
			},
		},
	}
	secretaryPrepareJob(&work)
	return work
}

func secretaryGuardRows() []scriptedRows {
	return []scriptedRows{
		{columns: []string{"owner"}, values: [][]driver.Value{{"owner_1"}}},
		{columns: []string{"revision"}, values: [][]driver.Value{{int64(1)}}},
		{columns: []string{"active"}, values: [][]driver.Value{{true}}},
	}
}

func TestSecretaryDriverGivenValidPublicationThenSummaryAndCheckpointCommitTogether(t *testing.T) {
	work := secretaryDriverWork()
	request := secretaryPublication(work)
	queries := append(
		secretaryGuardRows(),
		summaryEmptyRows(),
		summaryEmptyRows(),
		summaryMessageRows("message_1", "old body", "owner_1"),
	)
	script := &scriptedPostgresScript{queries: queries}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	version, err := store.PublishSecretaryJob(t.Context(), work, request)
	require.NoError(t, err)
	assert.EqualValues(t, 1, version.Revision)
	assert.True(t, script.committed)
	joined := strings.Join(script.execTexts, "\n")
	assert.Contains(t, joined, "INSERT INTO message_snapshots")
	assert.Contains(t, joined, "INSERT INTO summary_versions")
	assert.Contains(t, joined, "processed_hash")
	assert.Contains(t, joined, "processed_revision")
}

func TestSecretaryDriverGivenPublishedRequestThenRetryReusesItsExactVersion(t *testing.T) {
	work := secretaryDriverWork()
	request := secretaryPublication(work)
	versionRow := summaryVersionRows(work.TargetSummaryID, "owner_1", 1)
	versionRow.values[0][3] = "fragment"
	versionRow.values[0][4] = secretarySubject(work.SourceID)
	queries := append(
		secretaryGuardRows(),
		scriptedRows{
			columns: []string{"id", "revision", "hash"},
			values: [][]driver.Value{
				{work.TargetSummaryID, int64(1), secretaryRequestHash(request)},
			},
		},
		versionRow,
		summarySourceRows(),
		summaryAccessRows("owner_1", "message_1"),
		summaryMessageRows("message_1", "old body", "owner_1"),
	)
	script := &scriptedPostgresScript{queries: queries}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	version, err := store.PublishSecretaryJob(t.Context(), work, request)
	require.NoError(t, err)
	assert.EqualValues(t, 1, version.Revision)
	assert.NotContains(t, strings.Join(script.execTexts, "\n"), "INSERT INTO summary_versions")
}

func TestSecretaryDriverGivenSettingsCASAndStatusThenPersistsOnlyOwnerConfiguration(t *testing.T) {
	queries := []scriptedRows{{columns: []string{"allowed"}, values: [][]driver.Value{{true}}},
		{
			columns: []string{"owner", "agent", "enabled", "revision", "updated"},
			values:  [][]driver.Value{{"owner_1", "selected", true, int64(1), time.Now()}},
		},
		{
			columns: []string{"owner", "agent", "enabled", "revision", "updated"},
			values:  [][]driver.Value{{"owner_1", "selected", true, int64(1), time.Now()}},
		},
		{
			columns: []string{"pending", "processing", "completed", "error"},
			values:  [][]driver.Value{{int64(2), true, nil, "offline"}},
		},
		{columns: []string{"enabled"}, values: [][]driver.Value{{true}}},
	}
	script := &scriptedPostgresScript{queries: queries}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	settings, err := store.UpdateSecretarySettings(
		t.Context(),
		"owner_1",
		domain.SecretarySettingsUpdate{AgentID: "selected", Enabled: true},
	)
	require.NoError(t, err)
	assert.True(t, settings.Enabled)
	status, err := store.GetSecretaryStatus(t.Context(), "owner_1")
	require.NoError(t, err)
	assert.True(t, status.Processing)
	assert.Equal(t, 2, status.PendingCount)
	assert.Equal(t, "offline", status.LastError)
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner_1"))
	assert.Contains(t, strings.Join(script.execTexts, "\n"), "retry_generation")
}
