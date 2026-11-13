package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryOperationInitialSummary(
	t *testing.T,
	f *secretaryLifecycleFixture,
) domain.SummaryVersion {
	t.Helper()
	work := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
	results, err := f.store.PublishSecretaryBatch(
		t.Context(),
		work,
		secretaryNodesPublication(work, "initial"),
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	return results[0]
}

func TestSecretaryOperationsGivenManualReplacementForChangedSourceThenRetainsStaleHistoryWithoutAutomaticRepair(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		original := secretaryOperationInitialSummary(t, f)
		secretaryOperationMessage(t, f, "m1", "A corrected source", 1)
		operation := secretaryOperationDraft(t, f, "human_summary", "pending")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, secretaryHumanInput(operation))
		require.NoError(t, err)
		previous, err := f.store.(domain.SummaryStore).GetSummary(
			t.Context(),
			"owner",
			original.SummaryID,
			original.Revision,
		)
		require.NoError(t, err)
		assert.Equal(t, original.Body, previous.Body)
		assert.Equal(t, original.Revision, previous.Revision)
		assert.Equal(t, "stale", previous.NodeStatus)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs, "a user-completed correction must not require another model run")
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Zero(t, status.PendingCount)
		secretaryOperationMessage(t, f, "m1", "Another later correction", 1)
		next := secretaryClaim(t, f.store)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "m1", next.Messages[0].SourceID)
		assert.Contains(
			t,
			next.InvalidatedSummaries,
			domain.SummaryRef{SummaryID: original.SummaryID, Revision: original.Revision},
		)
	})
}

func TestSecretaryOperationsGivenUnselectedInvalidationThenManualCompletionDoesNotAcknowledgeOtherMessages(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretaryOperationMessage(t, f, "m2", "Second topic", 2)
		original := secretaryOperationInitialSummary(t, f)
		secretaryOperationMessage(t, f, "m1", "First topic corrected", 1)
		secretaryOperationMessage(t, f, "m2", "Second topic corrected", 2)
		operation := secretaryOperationDraft(t, f, "human_summary", "selected", "m1")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, secretaryHumanInput(operation))
		require.NoError(t, err)
		next := secretaryClaim(t, f.store)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "m2", next.Messages[0].SourceID)
		assert.Contains(
			t,
			next.InvalidatedSummaries,
			domain.SummaryRef{SummaryID: original.SummaryID, Revision: original.Revision},
		)
	})
}

func TestSecretaryOperationsGivenHumanEditThenDoesNotAcknowledgeChangedSource(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		original := secretaryOperationInitialSummary(t, f)
		secretaryOperationMessage(t, f, "m1", "Changed source still requires handling", 1)
		operation, err := secretaryOperationStore(
			f,
		).PrepareSecretaryOperation(t.Context(), "owner", domain.SecretaryOperationPrepare{
			SourceID: "1", Kind: "human_edit", SummaryID: original.SummaryID})
		require.NoError(t, err)
		input := secretaryHumanInput(operation)
		input.SummaryID, input.ExpectedSummaryRevision = original.SummaryID, original.Revision
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		next := secretaryClaim(t, f.store)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "m1", next.Messages[0].SourceID)
	})
}
