package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryTasksGivenSourceAccessRevokedThenHidesListDetailAndControls(t *testing.T) {
	for _, change := range []string{"archived", "owner", "internal", "node"} {
		t.Run(change, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				secretaryTaskUnknown(t, f, "provider error")
				store := secretaryTasksStore(t, f)
				task := secretaryReadTask(t, f).Task
				f.revokeSecretaryTaskSource(t, change)
				page, err := store.ListSecretaryTasks(
					t.Context(),
					"owner",
					domain.SecretaryTaskQuery{},
				)
				require.NoError(t, err)
				assert.Empty(t, page.Items)
				_, err = store.GetSecretaryTask(
					t.Context(),
					"owner",
					"1",
					domain.SecretaryTaskQuery{},
				)
				assert.ErrorIs(t, err, ErrNotFound)
				_, err = store.ControlSecretaryTask(
					t.Context(),
					"owner",
					"1",
					secretaryTaskObserved(task, "pause"),
				)
				assert.ErrorIs(t, err, ErrNotFound)
				_, err = store.ClaimSecretaryTaskRecovery(
					t.Context(),
					"owner",
					"1",
					secretaryTaskObserved(task, "recheck"),
					time.Minute,
				)
				assert.ErrorIs(t, err, ErrNotFound)
			})
		})
	}
}

func (f *secretaryLifecycleFixture) revokeSecretaryTaskSource(t *testing.T, change string) {
	t.Helper()
	if f.memory != nil {
		session := f.memory.sessions[sessionKey("source", "session")]
		agent := f.memory.agents["source"]
		switch change {
		case "archived":
			session.ArchivedAt = f.now
		case "owner":
			agent.OwnerUserID = "other"
		case "internal":
			session.Source = domain.SecretaryInternalSource
		case "node":
			agent.NodeID = "deleted"
		}
		f.memory.agents["source"] = agent
		f.memory.sessions[sessionKey("source", "session")] = session
		return
	}
	var err error
	switch change {
	case "archived":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`UPDATE agent_sessions SET archived_at=NOW() WHERE id=1`,
		)
	case "owner":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`UPDATE agents SET owner_user_id='other' WHERE agent_id='source'`,
		)
	case "internal":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`UPDATE agent_sessions SET source='secretary_internal' WHERE id=1`,
		)
	case "node":
		_, err = f.postgres.db.ExecContext(
			t.Context(),
			`INSERT INTO nodes(node_id,owner_user_id,hostname,api_key_hash,deleted_at)
 VALUES('deleted','owner','test','node-key',NOW()); UPDATE agents SET node_id='deleted' WHERE agent_id='source'`,
		)
	}
	require.NoError(t, err)
}

func TestSecretaryTasksGivenActualInternalSessionThenLinksItOnlyWhileCurrentlyReadable(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryTaskUnknown(t, f, "provider error")
		id := secretaryExecutionSessionID(job.OwnerUserID, job.AttemptID)
		if f.memory != nil {
			f.memory.sessions[sessionKey(job.AgentID, id)] = AgentSession{
				AgentID:       job.AgentID,
				SessionID:     id,
				Source:        domain.SecretaryInternalSource,
				RuntimeStatus: "idle",
			}
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO agent_sessions(agent_id,session_id,source,runtime_status)
 VALUES($1,$2,'secretary_internal','idle')`, job.AgentID, id)
			require.NoError(t, err)
		}
		detail := secretaryReadTask(t, f)
		assert.Equal(t, id, detail.Task.ExecutionSessionID)
		assert.Equal(t, job.AgentID, detail.Task.ExecutionAgentID)
		assert.Equal(t, "idle", detail.Task.ExecutionStatus)
		require.Len(t, detail.Attempts, 1)
		assert.Equal(t, id, detail.Attempts[0].ExecutionSessionID)
		if f.memory != nil {
			agent := f.memory.agents[job.AgentID]
			agent.OwnerUserID = "other"
			f.memory.agents[job.AgentID] = agent
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE agents SET owner_user_id='other' WHERE agent_id=$1`, job.AgentID)
			require.NoError(t, err)
		}
		detail = secretaryReadTask(t, f)
		assert.Empty(t, detail.Task.ExecutionSessionID)
		assert.Empty(t, detail.Attempts[0].ExecutionSessionID)
	})
}

func TestSecretaryTasksGivenIncompleteSourceFailureThenReportsWaitingWithoutAutomaticRetryDate(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryClaim(t, f.store)
		require.NoError(
			t,
			f.store.FailSecretaryJob(t.Context(), job, domain.ErrSecretarySourceIncomplete),
		)
		detail := secretaryReadTask(t, f)
		assert.Equal(t, "waiting_source", detail.Task.ExecutionState)
		assert.Equal(t, "source_incomplete", detail.Task.ErrorCode)
		assert.Nil(t, detail.Task.NextRetryAt)
	})
}
