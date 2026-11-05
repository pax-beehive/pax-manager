package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (f *secretaryLifecycleFixture) addTaskSource(t *testing.T) {
	t.Helper()
	if f.memory != nil {
		f.memory.sessions[sessionKey("source", "session_two")] = AgentSession{
			ID:            2,
			AgentID:       "source",
			SessionID:     "session_two",
			RuntimeStatus: "idle",
		}
		secretaryMemoryMessage(f.memory, "m_other_source", "Another source", 1)
		message := f.memory.messages["m_other_source"]
		message.SessionID = "session_two"
		f.memory.messages[message.MessageID] = message
	} else {
		_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO agent_sessions(agent_id,session_id) VALUES('source','session_two');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,status,session_seq)
 VALUES('m_other_source','source','session_two','test','inbound','assistant','chat','complete',1);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('m_other_source',0,'text','Another source')`)
		require.NoError(t, err)
	}
	require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
}

func TestSecretaryTasksGivenBoundedPagesThenBindsCursorsToOwnerAndSource(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := secretaryTasksStore(t, f)
		job := secretaryTaskUnknown(t, f, "private failure")
		f.addTaskSource(t)
		page, err := store.ListSecretaryTasks(
			t.Context(),
			"owner",
			domain.SecretaryTaskQuery{Limit: 1},
		)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, "1", page.Items[0].TaskID)
		require.NotEmpty(t, page.NextCursor)
		next, err := store.ListSecretaryTasks(
			t.Context(),
			"owner",
			domain.SecretaryTaskQuery{Limit: 1, Cursor: page.NextCursor},
		)
		require.NoError(t, err)
		require.Len(t, next.Items, 1)
		assert.Equal(t, "2", next.Items[0].TaskID)
		assert.Empty(t, next.NextCursor)
		_, err = store.ListSecretaryTasks(
			t.Context(),
			"other",
			domain.SecretaryTaskQuery{Cursor: page.NextCursor},
		)
		assert.ErrorIs(t, err, domain.ErrInvalidCursor)
		before, err := store.GetSecretaryTask(
			t.Context(),
			"owner",
			"2",
			domain.SecretaryTaskQuery{},
		)
		require.NoError(t, err)
		before.Task, err = store.ControlSecretaryTask(
			t.Context(),
			"owner",
			"2",
			secretaryTaskObserved(before.Task, "pause"),
		)
		require.NoError(t, err)
		for range 2 {
			if f.memory != nil {
				*f.now = f.now.Add(time.Second)
			}
			detail := secretaryReadTask(t, f)
			action := secretaryTaskObserved(detail.Task, "retry")
			action.ConfirmRestart = true
			_, err = store.ControlSecretaryTask(t.Context(), "owner", "1", action)
			require.NoError(t, err)
			job = secretaryTaskUnknown(t, f, "provider failure")
		}
		after, err := store.GetSecretaryTask(t.Context(), "owner", "2", domain.SecretaryTaskQuery{})
		require.NoError(t, err)
		assert.Equal(
			t,
			before.Task,
			after.Task,
			"single-source retry must not touch another source",
		)
		detail, err := store.GetSecretaryTask(
			t.Context(),
			"owner",
			"1",
			domain.SecretaryTaskQuery{Limit: 1},
		)
		require.NoError(t, err)
		require.Len(t, detail.Attempts, 1)
		require.NotEmpty(t, detail.NextCursor)
		assert.Equal(t, job.AttemptID, detail.Attempts[0].AttemptID)
		older, err := store.GetSecretaryTask(
			t.Context(),
			"owner",
			"1",
			domain.SecretaryTaskQuery{Limit: 1, Cursor: detail.NextCursor},
		)
		require.NoError(t, err)
		require.Len(t, older.Attempts, 1)
		assert.NotEqual(t, detail.Attempts[0].AttemptID, older.Attempts[0].AttemptID)
		_, err = store.GetSecretaryTask(
			t.Context(),
			"owner",
			"2",
			domain.SecretaryTaskQuery{Cursor: detail.NextCursor},
		)
		assert.ErrorIs(t, err, domain.ErrInvalidCursor)
		_, err = store.GetSecretaryTask(t.Context(), "other", "1", domain.SecretaryTaskQuery{})
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSecretaryTasksGivenConcurrentObservedActionsThenOnlyOneRevisionWins(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := secretaryTasksStore(t, f)
		detail := secretaryReadTask(t, f)
		action := secretaryTaskObserved(detail.Task, "pause")
		results := make(chan error, 2)
		var group sync.WaitGroup
		for range 2 {
			group.Add(1)
			go func() {
				defer group.Done()
				_, err := store.ControlSecretaryTask(t.Context(), "owner", "1", action)
				results <- err
			}()
		}
		group.Wait()
		close(results)
		var success, conflicts int
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		assert.Equal(t, 1, success)
		assert.Equal(t, 1, conflicts)
	})
}

func TestSecretaryTasksGivenExpiredRunningAttemptThenRecheckNeverCreatesANewAttempt(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := secretaryTasksStore(t, f)
		original := f.begin(t, f.prepare(t))
		active := secretaryReadTask(t, f)
		retry := secretaryTaskObserved(active.Task, "retry")
		retry.ConfirmRestart = true
		_, err := store.ControlSecretaryTask(t.Context(), "owner", "1", retry)
		assert.ErrorIs(t, err, ErrConflict)
		f.expire(t)
		detail := secretaryReadTask(t, f)
		assert.Equal(t, "needs_recovery", detail.Task.ExecutionState)
		retry = secretaryTaskObserved(detail.Task, "retry")
		_, err = store.ControlSecretaryTask(t.Context(), "owner", "1", retry)
		assert.ErrorIs(t, err, ErrConflict)
		job, err := store.ClaimSecretaryTaskRecovery(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(detail.Task, "recheck"),
			-1,
		)
		require.NoError(t, err)
		assert.Equal(t, original.AttemptID, job.AttemptID)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
		require.NoError(t, store.ReleaseSecretaryTaskRecovery(t.Context(), job))
	})
}

func TestSecretaryTasksGivenRecoveryValidationFailureThenRecordsItButKeepsSchedulingBlocked(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretaryTaskUnknown(t, f, "provider outcome unknown")
		store := secretaryTasksStore(t, f)
		observed := secretaryReadTask(t, f).Task
		job, err := store.ClaimSecretaryTaskRecovery(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(observed, "recheck"),
			time.Minute,
		)
		require.NoError(t, err)
		err = f.store.FailSecretaryAttempt(
			t.Context(),
			job,
			job.AttemptID,
			domain.SecretaryOutputValidationError{
				Code: "schema",
				Err:  errors.New("unsafe raw provider error"),
			},
		)
		require.NoError(t, err)
		require.NoError(t, store.ReleaseSecretaryTaskRecovery(t.Context(), job))
		detail := secretaryReadTask(t, f)
		assert.Equal(t, "blocked", detail.Task.ExecutionState)
		assert.Equal(t, "invalid_output", detail.Task.ErrorCode)
		raw, err := json.Marshal(detail)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "unsafe raw provider error")
		f.releaseConnectionBackoff(t)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
	})
}

func TestSecretaryTasksGivenCanceledContextOrMalformedInputThenReturnsWithoutMutation(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := secretaryTasksStore(t, f)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := store.ListSecretaryTasks(ctx, "owner", domain.SecretaryTaskQuery{})
		assert.ErrorIs(t, err, context.Canceled)
		_, err = store.GetSecretaryTask(ctx, "owner", "1", domain.SecretaryTaskQuery{})
		assert.ErrorIs(t, err, context.Canceled)
		_, err = store.ControlSecretaryTask(ctx, "owner", "1", domain.SecretaryTaskAction{})
		assert.ErrorIs(t, err, context.Canceled)
		_, err = store.ClaimSecretaryTaskRecovery(
			ctx,
			"owner",
			"1",
			domain.SecretaryTaskAction{},
			time.Minute,
		)
		assert.ErrorIs(t, err, context.Canceled)
		assert.ErrorIs(
			t,
			store.ReleaseSecretaryTaskRecovery(ctx, domain.SecretaryJob{}),
			context.Canceled,
		)
		for _, query := range []domain.SecretaryTaskQuery{{Limit: -1}, {Limit: 10000}, {Cursor: "not-a-cursor"}} {
			_, err = store.ListSecretaryTasks(t.Context(), "owner", query)
			assert.ErrorIs(t, err, domain.ErrInvalidCursor)
			_, err = store.GetSecretaryTask(t.Context(), "owner", "1", query)
			assert.ErrorIs(t, err, domain.ErrInvalidCursor)
		}
		_, err = store.ListSecretaryTasks(t.Context(), "", domain.SecretaryTaskQuery{})
		assert.ErrorIs(t, err, ErrUnauthorized)
		_, err = store.GetSecretaryTask(t.Context(), "owner", "-1", domain.SecretaryTaskQuery{})
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = store.ControlSecretaryTask(t.Context(), "", "1", domain.SecretaryTaskAction{})
		assert.ErrorIs(t, err, ErrUnauthorized)
		_, err = store.ClaimSecretaryTaskRecovery(
			t.Context(),
			"",
			"1",
			domain.SecretaryTaskAction{},
			time.Minute,
		)
		assert.ErrorIs(t, err, ErrUnauthorized)
	})
}
