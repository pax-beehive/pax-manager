package storage

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryQueueSnapshot(t *testing.T, store *PostgresStore) string {
	t.Helper()
	var snapshot string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT jsonb_build_object(
 'jobs',COALESCE((SELECT jsonb_agg(to_jsonb(j) ORDER BY source_id) FROM secretary_jobs j),'[]'::jsonb),
 'states',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY source_id,message_id) FROM secretary_message_state m),'[]'::jsonb)
 )::text`).Scan(&snapshot))
	return snapshot
}

func TestSecretaryBootstrapPostgresGivenMissingJobThenSeedsOnceAndSkipsUnusedEmptySession(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	_, err := store.db.ExecContext(t.Context(), `DELETE FROM secretary_jobs;
 INSERT INTO agent_sessions(agent_id,session_id) VALUES('source','unused-empty')`)
	require.NoError(t, err)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	var jobs, states, desired int64
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*),max(desired_revision) FROM secretary_jobs`).
			Scan(&jobs, &desired),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_message_state`).
			Scan(&states),
	)
	assert.EqualValues(t, 1, jobs)
	assert.EqualValues(t, 1, desired)
	assert.EqualValues(t, 1, states)
	before := secretaryQueueSnapshot(t, store)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	assert.JSONEq(t, before, secretaryQueueSnapshot(t, store))
	work := secretaryClaim(t, store)
	assert.Equal(t, "session", work.SourceSessionID)
	require.Len(t, work.Messages, 1)
	assert.Equal(t, "m1", work.Messages[0].SourceID)
}

func TestSecretaryBootstrapPostgresGivenConcurrentRestartsThenMissingJobIsSeededOnce(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	_, err := store.db.ExecContext(t.Context(), `DELETE FROM secretary_jobs`)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
 current_database()||':'||current_schema()||':pax:secretary:bootstrap',0))`)
	require.NoError(t, err)
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- NewPostgresStore(store.db, time.Now).BackfillSecretaryJobs(ctx) }()
	}
	require.Eventually(t, func() bool {
		var waiting int
		queryErr := store.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%pax:secretary:bootstrap%'`).Scan(&waiting)
		return queryErr == nil && waiting >= 2
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit())
	for range 2 {
		require.NoError(t, <-results)
	}
	var desired, change int64
	require.NoError(
		t,
		store.db.QueryRowContext(ctx, `SELECT j.desired_revision,s.change_revision FROM secretary_jobs j
 JOIN secretary_message_state s USING(source_id)`).
			Scan(&desired, &change),
	)
	assert.EqualValues(t, 1, desired)
	assert.EqualValues(t, 1, change)
	before := secretaryQueueSnapshot(t, store)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	assert.JSONEq(t, before, secretaryQueueSnapshot(t, store))
}

func TestSecretaryBootstrapPostgresGivenMissedOwnerTransferThenRepairsOwnerAndFencesOldLease(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	initial := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), initial, secretaryPublication(initial))
	require.NoError(t, err)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"other",
		domain.SecretarySettingsUpdate{AgentID: "other", Enabled: true},
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='changed' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	old := secretaryClaim(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`ALTER TABLE agents DISABLE TRIGGER secretary_agent_dirty;
 UPDATE agents SET owner_user_id='other' WHERE agent_id='source';
 ALTER TABLE agents ENABLE TRIGGER secretary_agent_dirty`,
	)
	require.NoError(t, err)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	var owner, lease, checkpointOwner string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT j.owner_user_id,j.lease_token,s.processed_owner_user_id
 FROM secretary_jobs j JOIN secretary_message_state s USING(source_id)`).
			Scan(&owner, &lease, &checkpointOwner),
	)
	assert.Equal(t, "other", owner)
	assert.Empty(t, lease)
	assert.Equal(t, "owner", checkpointOwner)
	before := secretaryQueueSnapshot(t, store)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	assert.JSONEq(t, before, secretaryQueueSnapshot(t, store))
	_, err = store.PublishSecretaryJob(t.Context(), old, secretaryPublication(old))
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	current := secretaryClaim(t, store)
	assert.Equal(t, "other", current.OwnerUserID)
	require.Len(t, current.Messages, 1)
	assert.Equal(t, "changed", current.Messages[0].Body)
	assert.NotEqual(t, old.TargetSummaryID, current.TargetSummaryID)
}

func TestSecretaryBootstrapPostgresGivenOlderCanonicalizationThenRechecksOnlyOnce(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_message_state SET processed_canonicalization_version='older'`,
	)
	require.NoError(t, err)
	var desired int64
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM secretary_jobs`).
			Scan(&desired),
	)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	var after int64
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM secretary_jobs`).
			Scan(&after),
	)
	assert.Equal(t, desired+1, after)
	before := secretaryQueueSnapshot(t, store)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	assert.JSONEq(t, before, secretaryQueueSnapshot(t, store))
	next := secretaryClaim(t, store)
	require.Len(t, next.Messages, 1)
	assert.Equal(t, "m1", next.Messages[0].SourceID)
	_, err = store.PublishSecretaryJob(t.Context(), next, secretaryPublication(next))
	require.NoError(t, err)
	before = secretaryQueueSnapshot(t, store)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	assert.JSONEq(t, before, secretaryQueueSnapshot(t, store))
}

func TestSecretaryBootstrapPostgresGivenStableHeadSQLThenIdentityMatchesDomain(t *testing.T) {
	store := secretaryPostgresFixture(t)
	for _, owner := range []string{"owner", "owner:with/separators", "\u7528\u6237"} {
		for _, source := range []int64{1, 9223372036854775807} {
			var actual string
			err := store.db.QueryRowContext(t.Context(), `SELECT `+secretaryStableHeadSQL+`
 FROM (SELECT $1::text owner_user_id) a CROSS JOIN (SELECT $2::bigint id) s`, owner, source).Scan(&actual)
			require.NoError(t, err)
			assert.Equal(t, domain.SecretarySummaryID(owner, strconv.FormatInt(source, 10)), actual)
		}
	}
}

func TestSecretaryBootstrapPostgresGivenExistingQueueThenRestartPreservesEveryQueueAndCheckpointField(
	t *testing.T,
) {
	for _, state := range []string{"completed", "pending", "leased", "deferred"} {
		t.Run(state, func(t *testing.T) {
			store := secretaryPostgresFixture(t)
			secretaryEnable(t, store)
			if state != "pending" {
				work := secretaryClaim(t, store)
				switch state {
				case "completed":
					_, err := store.PublishSecretaryJob(
						t.Context(),
						work,
						secretaryPublication(work),
					)
					require.NoError(t, err)
				case "deferred":
					require.NoError(
						t,
						store.FailSecretaryJob(t.Context(), work, validationFailure()),
					)
				}
			}
			before := secretaryQueueSnapshot(t, store)
			for range 3 {
				restarted := NewPostgresStore(store.db, time.Now)
				require.NoError(t, restarted.BackfillSecretaryJobs(t.Context()))
				assert.JSONEq(t, before, secretaryQueueSnapshot(t, store))
			}
		})
	}
}

func TestSecretaryBootstrapPostgresGivenMissingStateThenSeedsOnlyMissingMessageOnce(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 VALUES('m2','source','session','test','inbound','user','chat',2);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('m2',0,'text','new source');
 DELETE FROM secretary_message_state WHERE message_id='m2'`,
	)
	require.NoError(t, err)
	var desired int64
	var original string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM secretary_jobs`).
			Scan(&desired),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT to_jsonb(m)::text FROM secretary_message_state m WHERE message_id='m1'`).
			Scan(&original),
	)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	var actual int64
	var after string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM secretary_jobs`).
			Scan(&actual),
	)
	assert.Equal(t, desired+1, actual)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT to_jsonb(m)::text FROM secretary_message_state m WHERE message_id='m1'`).
			Scan(&after),
	)
	assert.JSONEq(t, original, after)
	seeded := secretaryQueueSnapshot(t, store)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	assert.JSONEq(t, seeded, secretaryQueueSnapshot(t, store))
	next := secretaryClaim(t, store)
	require.Len(t, next.Messages, 1)
	assert.Equal(t, "m2", next.Messages[0].SourceID)
}

func TestSecretaryBootstrapPostgresGivenDisabledLastMessageDeletionThenExplicitReconcilePublishesEmptyHead(
	t *testing.T,
) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "reenable", true: "manual_retry"}[manual], func(t *testing.T) {
			store := secretaryPostgresFixture(t)
			cfg := secretaryEnable(t, store)
			work := secretaryClaim(t, store)
			_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
			require.NoError(t, err)
			_, err = store.UpdateSecretarySettings(
				t.Context(),
				"owner",
				domain.SecretarySettingsUpdate{
					AgentID:          "secretary",
					ExpectedRevision: cfg.Revision,
				},
			)
			require.NoError(t, err)
			_, err = store.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
			require.NoError(t, err)
			if manual {
				_, err = store.db.ExecContext(
					t.Context(),
					`UPDATE secretary_settings SET enabled=TRUE,revision=revision+1 WHERE owner_user_id='owner'`,
				)
				require.NoError(t, err)
				require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
			} else {
				_, err = store.UpdateSecretarySettings(t.Context(), "owner", domain.SecretarySettingsUpdate{AgentID: "secretary", Enabled: true, ExpectedRevision: cfg.Revision + 1})
				require.NoError(t, err)
			}
			reset := secretaryClaim(t, store)
			assert.True(t, reset.ResetRequired)
			assert.Empty(t, reset.Messages)
			request := secretaryPublication(reset)
			request.Body = "No readable messages remain."
			head, err := store.PublishSecretaryJob(t.Context(), reset, request)
			require.NoError(t, err)
			assert.Empty(t, head.Coverage)
			assert.EqualValues(t, 2, head.Revision)
			before := secretaryQueueSnapshot(t, store)
			require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
			assert.JSONEq(t, before, secretaryQueueSnapshot(t, store))
		})
	}
}
