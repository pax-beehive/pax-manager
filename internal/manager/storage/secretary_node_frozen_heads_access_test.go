package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryFrozenHeadsGivenCrossSessionLeafRevokedAfterClaimThenRejectsWithoutHeadChange(
	t *testing.T,
) {
	for _, noSummary := range []bool{false, true} {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			first := secretaryNodesReadyJob(t, f.store)
			versions, err := f.store.PublishSecretaryBatch(
				t.Context(),
				first,
				secretaryNodesPublication(first, "baseline"),
			)
			require.NoError(t, err)
			f.addTaskSource(t)
			tasks := secretaryTasksStore(t, f)
			other, err := tasks.GetSecretaryTask(
				t.Context(),
				"owner",
				"2",
				domain.SecretaryTaskQuery{},
			)
			require.NoError(t, err)
			_, err = tasks.ControlSecretaryTask(
				t.Context(),
				"owner",
				"2",
				secretaryTaskObserved(other.Task, "pause"),
			)
			require.NoError(t, err)
			baseline := versions[0]
			baseline, err = f.store.(domain.SummaryStore).PublishSummary(
				t.Context(),
				"owner",
				domain.PublishSummaryRequest{
					SummaryID: baseline.SummaryID, ExpectedRevision: baseline.Revision, RequestID: "cross-session-baseline",
					SubjectType: baseline.SubjectType, SubjectID: baseline.SubjectID, Title: "Cross-session baseline", Body: "Another source",
					Coverage: []domain.MessageVersionRef{
						{
							MessageID:               "m_other_source",
							ContentHash:             domain.SearchContentHash("Another source"),
							CanonicalizationVersion: domain.SearchCanonicalizationVersion,
						},
					},
				},
			)
			require.NoError(t, err)
			f.addMessage(t)
			secretaryTaskUnknown(t, f, "native outcome unknown")
			observed := secretaryReadTask(t, f).Task
			job, err := tasks.ClaimSecretaryTaskRecovery(
				t.Context(),
				"owner",
				"1",
				secretaryTaskObserved(observed, "recheck"),
				time.Minute,
			)
			require.NoError(t, err)
			require.Len(t, job.CurrentSummaries, 1)
			require.NoError(
				t,
				f.store.SaveSecretaryAttemptOutput(
					t.Context(),
					job,
					job.AttemptID,
					"{}",
					domain.SecretaryToolReads{},
				),
			)
			if f.memory != nil {
				session := f.memory.sessions[sessionKey("source", "session_two")]
				session.ArchivedAt = f.now
				f.memory.sessions[sessionKey("source", "session_two")] = session
				assert.Equal(
					t,
					baseline.Revision,
					f.memory.secretaryLocked().nodes[baseline.SummaryID].CurrentRevision,
				)
			} else {
				_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE agent_sessions SET archived_at=NOW() WHERE agent_id='source' AND session_id='session_two'`)
				require.NoError(t, err)
				var revision int64
				require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT current_revision FROM knowledge_nodes WHERE id=$1`, baseline.SummaryID).Scan(&revision))
				assert.Equal(t, baseline.Revision, revision)
			}
			publication := secretaryNodesPublication(job, "new-node")
			if noSummary {
				publication.Outputs = nil
				for i := range publication.Outcomes {
					publication.Outcomes[i].Action = "no_summary"
					publication.Outcomes[i].SummaryKeys = nil
					publication.Outcomes[i].Reason = "No new conclusion"
				}
			}
			_, err = f.store.PublishSecretaryBatch(t.Context(), job, publication)
			assert.ErrorIs(t, err, domain.ErrNotFound)
			assert.Equal(t, job.RetryEpoch, secretaryReadTask(t, f).Task.RetryEpoch)
		})
	}
}
