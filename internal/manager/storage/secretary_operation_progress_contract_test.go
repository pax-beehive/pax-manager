package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryOperationProgressGivenCompletedHistoryThenExplicitGuidedWorkRemainsVisibleWithoutResettingCounts(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryClaim(t, f.store)
		_, err := f.store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
		require.NoError(t, err)
		operations := f.store.(domain.SecretaryOperationStore)
		op, err := operations.PrepareSecretaryOperation(
			t.Context(),
			"owner",
			domain.SecretaryOperationPrepare{SourceID: "1", Kind: "guided_summary", Scope: "all"},
		)
		require.NoError(t, err)
		_, err = operations.SubmitSecretaryOperation(
			t.Context(),
			"owner",
			op.OperationID,
			domain.SecretaryOperationSubmit{
				RequestID:        "reprocess",
				ExpectedRevision: op.Revision,
				RunInstruction:   "Keep only decisions.",
			},
		)
		require.NoError(t, err)
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(
			t,
			1,
			status.PendingCount,
			"explicit work remains pending even with a completed source watermark",
		)
		tasks, err := f.store.(domain.SecretaryTaskStore).ListSecretaryTasks(
			t.Context(),
			"owner",
			domain.SecretaryTaskQuery{},
		)
		require.NoError(t, err)
		require.Len(t, tasks.Items, 1)
		assert.Equal(t, op.OperationID, tasks.Items[0].OperationID)
		assert.NotEqual(t, "completed", tasks.Items[0].ExecutionState)
		progress, err := f.store.(domain.SecretaryProgressStore).ReadSecretaryProgress(
			t.Context(),
			"owner",
			domain.SecretaryProgressQuery{},
		)
		require.NoError(t, err)
		assert.Equal(t, 1, progress.Counts.CompletedMessages)
		assert.Zero(
			t,
			progress.Counts.PendingMessages,
			"reprocessing does not manufacture incomplete messages",
		)
	})
}

func TestSecretaryOperationProgressGivenPartialHumanCompletionThenPreservesEarlierHole(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		f.addMessage(t)
		operations := f.store.(domain.SecretaryOperationStore)
		op, err := operations.PrepareSecretaryOperation(
			t.Context(),
			"owner",
			domain.SecretaryOperationPrepare{
				SourceID:   "1",
				Kind:       "human_summary",
				Scope:      "selected",
				MessageIDs: []string{"m2"},
			},
		)
		require.NoError(t, err)
		_, err = operations.SubmitSecretaryOperation(
			t.Context(),
			"owner",
			op.OperationID,
			domain.SecretaryOperationSubmit{
				RequestID:        "complete-later",
				ExpectedRevision: op.Revision,
				Title:            "Later turn",
				Body:             "A later turn",
			},
		)
		require.NoError(t, err)
		progress, err := f.store.(domain.SecretaryProgressStore).ReadSecretaryProgress(
			t.Context(),
			"owner",
			domain.SecretaryProgressQuery{},
		)
		require.NoError(t, err)
		assert.Equal(t, 1, progress.Counts.CompletedMessages)
		assert.Equal(t, 1, progress.Counts.PendingMessages)
		job := secretaryClaim(t, f.store)
		require.Len(t, job.Messages, 1)
		assert.Equal(t, "m1", job.Messages[0].SourceID)
	})
}
