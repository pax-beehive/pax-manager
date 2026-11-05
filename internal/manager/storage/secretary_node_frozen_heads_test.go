package storage

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func frozenHeadRecoveryFixture(
	t *testing.T,
	f *secretaryLifecycleFixture,
) (domain.SecretaryJob, domain.SummaryVersion) {
	t.Helper()
	first := secretaryNodesReadyJob(t, f.store)
	versions, err := f.store.PublishSecretaryBatch(
		t.Context(),
		first,
		secretaryNodesPublication(first, "baseline"),
	)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	f.addMessage(t)
	secretaryTaskUnknown(t, f, "native outcome unknown")
	task := secretaryReadTask(t, f).Task
	job, err := secretaryTasksStore(
		t,
		f,
	).ClaimSecretaryTaskRecovery(t.Context(), "owner", "1", secretaryTaskObserved(task, "recheck"), time.Minute)
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
	return job, versions[0]
}

func publishFrozenHeadVersion(
	t *testing.T,
	f *secretaryLifecycleFixture,
	baseline domain.SummaryVersion,
	id string,
	revision int64,
) domain.SummaryVersion {
	t.Helper()
	version, err := f.store.(domain.SummaryStore).PublishSummary(
		t.Context(),
		"owner",
		domain.PublishSummaryRequest{
			SummaryID: id, ExpectedRevision: revision, RequestID: id + "-external-" + strconv.FormatInt(revision, 10),
			SubjectType: baseline.SubjectType, SubjectID: baseline.SubjectID, Title: "Independent update", Body: "Updated outside the batch", Coverage: baseline.Coverage,
		},
	)
	require.NoError(t, err)
	return version
}

func TestSecretaryFrozenHeadsGivenAdvanceAfterRecoveryClaimThenRejectsPublicationWithoutReplanning(
	t *testing.T,
) {
	for _, noSummary := range []bool{false, true} {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			job, baseline := frozenHeadRecoveryFixture(t, f)
			publishFrozenHeadVersion(t, f, baseline, baseline.SummaryID, baseline.Revision)
			publication := secretaryNodesPublication(job, "new-node")
			if noSummary {
				publication.Outputs = nil
				for i := range publication.Outcomes {
					publication.Outcomes[i].Action = "no_summary"
					publication.Outcomes[i].SummaryKeys = nil
					publication.Outcomes[i].Reason = "No new conclusion"
				}
			}
			_, err := f.store.PublishSecretaryBatch(t.Context(), job, publication)
			require.ErrorIs(t, err, domain.ErrSummaryVersionMismatch)
			current := secretaryReadTask(t, f).Task
			assert.Equal(t, job.BatchID, current.BatchID)
			assert.Equal(t, job.RetryEpoch, current.RetryEpoch)
			assert.Equal(t, "output_received", current.ExecutionState)
			assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
		})
	}
}

func TestSecretaryFrozenHeadsGivenUnrelatedNewNodeOrHistoricalExtraReadThenStillPublishes(
	t *testing.T,
) {
	for _, historical := range []bool{false, true} {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			job, baseline := frozenHeadRecoveryFixture(t, f)
			old := publishFrozenHeadVersion(t, f, baseline, "independent-node", 0)
			if historical {
				publishFrozenHeadVersion(t, f, baseline, old.SummaryID, old.Revision)
				job.CurrentSummaries = append(job.CurrentSummaries, old)
			}
			results, err := f.store.PublishSecretaryBatch(
				t.Context(),
				job,
				secretaryNodesPublication(job, "new-node"),
			)
			require.NoError(t, err)
			require.Len(t, results, 1)
			assert.Equal(t, "completed", secretaryReadTask(t, f).Task.ExecutionState)
		})
	}
}
