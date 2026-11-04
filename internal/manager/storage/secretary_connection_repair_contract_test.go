package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryConnectionRepairGivenLegacyUnknownThenRequiresExactProofBeforeRequeue(
	t *testing.T,
) {
	for _, scenario := range []string{"safe_missing_tunnel", "other_error", "session_exists", "orphan_message", "active_lease", "changed_settings", "changed_epoch", "changed_agent", "partial_output"} {
		t.Run(scenario, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				job := f.begin(t, f.prepare(t))
				cause := "agent tunnel not connected"
				if scenario == "other_error" {
					cause = "context deadline exceeded"
				}
				failure := errors.Join(domain.ErrSecretaryExecutionUnknown, errors.New(cause))
				require.NoError(
					t,
					f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, failure),
				)
				require.NoError(t, f.store.FailSecretaryJob(t.Context(), job, failure))
				sessionID := secretary.ExecutionSessionID(job.OwnerUserID, job.AttemptID)
				f.prepareLegacyConnectionRepairScenario(t, job, sessionID, scenario)
				f.restart()
				require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
				state := f.connectionState(t, job)
				if scenario != "safe_missing_tunnel" {
					assert.Equal(t, "unknown", state.state)
					assert.Equal(t, "unknown", state.attempt)
					assert.False(t, state.notDispatched)
					assert.Zero(t, state.failures)
					return
				}
				assert.Equal(t, "pending", state.state)
				assert.Equal(t, "failed", state.attempt)
				assert.True(t, state.notDispatched)
				assert.Equal(t, 1, state.failures)
				assert.False(t, state.blocked)
				assert.Empty(t, state.lease)
				assert.WithinDuration(t, state.retryAfter, state.available, time.Microsecond)
				assert.Zero(t, state.processed)
				require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
				assert.Equal(
					t,
					state,
					f.connectionState(t, job),
					"startup repair must be idempotent",
				)
				f.releaseConnectionBackoff(t)
				retried := f.begin(t, f.prepare(t))
				assert.Equal(t, job.BatchID, retried.BatchID)
				assert.NotEqual(t, job.AttemptID, retried.AttemptID)
			})
		})
	}
}

func (f *secretaryLifecycleFixture) prepareLegacyConnectionRepairScenario(
	t *testing.T,
	job domain.SecretaryJob,
	sessionID, scenario string,
) {
	t.Helper()
	if f.memory != nil {
		queued := f.memory.secretaryState.jobs[job.SourceID]
		cfg := f.memory.secretaryState.settings[job.OwnerUserID]
		switch scenario {
		case "session_exists":
			f.memory.sessions[sessionKey(job.AgentID, sessionID)] = AgentSession{
				AgentID:   job.AgentID,
				SessionID: sessionID,
			}
		case "orphan_message":
			f.memory.messages["orphan"] = Message{
				MessageID: "orphan",
				AgentID:   job.AgentID,
				SessionID: sessionID,
			}
		case "active_lease":
			queued.work.LeaseToken, queued.leaseUntil = "live", f.now.Add(time.Minute)
		case "changed_settings":
			cfg.Revision++
		case "changed_epoch":
			queued.work.RetryEpoch++
		case "changed_agent":
			cfg.AgentID = "source"
		case "partial_output":
			batch := f.memory.secretaryState.executions[job.BatchID]
			batch.attempts[len(batch.attempts)-1].Output = "partial"
		}
		f.memory.secretaryState.settings[job.OwnerUserID] = cfg
		return
	}
	var err error
	switch scenario {
	case "session_exists":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`INSERT INTO agent_sessions(agent_id,session_id) VALUES($1,$2)`,
			job.AgentID,
			sessionID,
		)
	case "orphan_message":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type) VALUES('orphan',$1,$2,'test','inbound','assistant','chat')`,
			job.AgentID,
			sessionID,
		)
	case "active_lease":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`UPDATE secretary_jobs SET lease_token='live',lease_until=NOW()+INTERVAL '1 minute' WHERE source_id=$1`,
			job.SourceID,
		)
	case "changed_settings":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`UPDATE secretary_settings SET revision=revision+1 WHERE owner_user_id=$1`,
			job.OwnerUserID,
		)
	case "changed_epoch":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`UPDATE secretary_jobs SET retry_epoch=retry_epoch+1 WHERE source_id=$1`,
			job.SourceID,
		)
	case "changed_agent":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`UPDATE secretary_settings SET agent_id='source' WHERE owner_user_id=$1`,
			job.OwnerUserID,
		)
	case "partial_output":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`UPDATE secretary_attempts SET output_text='partial' WHERE attempt_id=$1`,
			job.AttemptID,
		)
	}
	require.NoError(t, err)
}
