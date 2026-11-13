package storage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryIndependentTopicSummaries(
	t *testing.T,
	f *secretaryLifecycleFixture,
) (domain.SummaryVersion, domain.SummaryVersion) {
	t.Helper()
	first := secretaryOperationInitialSummary(t, f)
	secretaryOperationMessage(t, f, "m2", "A second independent topic", 2)
	work := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
	results, err := f.store.PublishSecretaryBatch(
		t.Context(),
		work,
		secretaryNodesPublication(work, "second-topic"),
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	return first, results[0]
}

func TestSecretaryOperationsGivenSharedStaleNodeThenPartialGuidanceDoesNotRequireUnselectedRepair(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretaryOperationMessage(t, f, "m2", "An independent second fact", 2)
		shared := secretaryOperationInitialSummary(t, f)
		secretaryOperationMessage(t, f, "m1", "First fact corrected", 1)
		secretaryOperationMessage(t, f, "m2", "Second fact corrected", 2)
		operation := secretaryOperationDraft(t, f, "guided_summary", "selected", "m1")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID,
			domain.SecretaryOperationSubmit{
				RequestID:        "partial-shared-node",
				ExpectedRevision: operation.Revision,
			})
		require.NoError(t, err)
		work := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
		require.Len(t, work.Messages, 1)
		assert.Equal(t, "m1", work.Messages[0].SourceID)
		assert.Empty(t, work.InvalidatedSummaries)
		assert.False(t, work.ResetRequired)
		for _, summary := range work.CurrentSummaries {
			assert.NotEqual(
				t,
				shared.SummaryID,
				summary.SummaryID,
				"unrepaired stale text must remain suppressed",
			)
		}
		_, err = f.store.PublishSecretaryBatch(
			t.Context(),
			work,
			secretaryNodesPublication(work, "selected-correction"),
		)
		require.NoError(t, err)
		next := secretaryClaim(t, f.store)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "m2", next.Messages[0].SourceID)
		assert.Contains(
			t,
			next.InvalidatedSummaries,
			domain.SummaryRef{SummaryID: shared.SummaryID, Revision: shared.Revision},
		)
	})
}

func TestSecretaryOperationsGivenLaterBatchInvalidationThenItIsMandatoryOnlyWhenThatMessageIsSupplied(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		original := secretaryOperationInitialSummary(t, f)
		for i := 2; i <= 65; i++ {
			secretaryOperationMessage(t, f, fmt.Sprintf("m%d", i), "A new independent fact", i)
		}
		secretaryOperationMessage(
			t,
			f,
			"m1",
			"The original fact is corrected after the newer messages",
			1,
		)
		operation := secretaryOperationDraft(t, f, "guided_summary", "pending")
		require.Len(t, operation.Targets, 65)
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID,
			domain.SecretaryOperationSubmit{
				RequestID:        "batch-scoped-repairs",
				ExpectedRevision: operation.Revision,
			})
		require.NoError(t, err)
		first := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
		require.Len(t, first.Messages, 64)
		for _, message := range first.Messages {
			assert.NotEqual(t, "m1", message.SourceID)
		}
		assert.Empty(t, first.InvalidatedSummaries)
		_, err = f.store.PublishSecretaryBatch(
			t.Context(),
			first,
			secretaryNodesPublication(first, "new-facts"),
		)
		require.NoError(t, err)
		last := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
		require.Len(t, last.Messages, 1)
		assert.Equal(t, "m1", last.Messages[0].SourceID)
		assert.Equal(
			t,
			[]domain.SummaryRef{{SummaryID: original.SummaryID, Revision: original.Revision}},
			last.InvalidatedSummaries,
		)
	})
}

func TestSecretaryOperationsGivenUnrelatedInvalidationThenGuidedSelectionCompletesWithoutExpandingItsScope(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		_, second := secretaryIndependentTopicSummaries(t, f)
		secretaryOperationMessage(t, f, "m2", "The second topic has changed", 2)
		secretaryOperationMessage(t, f, "m3", "A routine confirmation with no new information", 3)
		operation := secretaryOperationDraft(t, f, "guided_summary", "selected", "m3")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID,
			domain.SecretaryOperationSubmit{
				RequestID:        "selected-only",
				ExpectedRevision: operation.Revision,
			})
		require.NoError(t, err)
		work := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
		require.Len(t, work.Messages, 1)
		assert.Equal(t, "m3", work.Messages[0].SourceID)
		assert.Empty(t, work.InvalidatedSummaries)
		assert.Empty(t, work.RemovedMessageIDs)
		assert.False(t, work.ResetRequired)
		_, err = f.store.PublishSecretaryBatch(t.Context(), work, domain.SecretaryBatchPublication{
			RequestID: work.BatchID, Outcomes: []domain.SecretaryMessageOutcome{
				{MessageID: "m3", Action: "no_summary", Reason: "Routine confirmation"},
			},
		})
		require.NoError(t, err)
		completed, err := secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "owner", operation.OperationID)
		require.NoError(t, err)
		assert.Equal(t, "completed", completed.Status)
		next := secretaryClaim(t, f.store)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "m2", next.Messages[0].SourceID)
		assert.Contains(
			t,
			next.InvalidatedSummaries,
			domain.SummaryRef{SummaryID: second.SummaryID, Revision: second.Revision},
		)
	})
}

func TestSecretaryOperationsGivenSelectedRepairThenOtherTopicInvalidationRemainsPending(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		first, second := secretaryIndependentTopicSummaries(t, f)
		secretaryOperationMessage(t, f, "m1", "The first topic is corrected", 1)
		secretaryOperationMessage(t, f, "m2", "The second topic is corrected", 2)
		operation := secretaryOperationDraft(t, f, "guided_summary", "selected", "m1")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID,
			domain.SecretaryOperationSubmit{
				RequestID:        "repair-first-topic",
				ExpectedRevision: operation.Revision,
			})
		require.NoError(t, err)
		work := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
		require.Len(t, work.Messages, 1)
		assert.Equal(t, "m1", work.Messages[0].SourceID)
		assert.Equal(
			t,
			[]domain.SummaryRef{{SummaryID: first.SummaryID, Revision: first.Revision}},
			work.InvalidatedSummaries,
		)
		request := secretaryPublication(work)
		request.SummaryID, request.ExpectedRevision, request.RequestID = first.SummaryID, first.Revision, ""
		_, err = f.store.PublishSecretaryBatch(t.Context(), work, domain.SecretaryBatchPublication{
			RequestID: work.BatchID,
			Outputs:   []domain.SecretarySummaryOutput{{Request: request}},
			Outcomes: []domain.SecretaryMessageOutcome{
				{MessageID: "m1", Action: "summarized", SummaryKeys: []string{first.SummaryID}},
			},
		})
		require.NoError(t, err)
		next := secretaryClaim(t, f.store)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "m2", next.Messages[0].SourceID)
		assert.Contains(
			t,
			next.InvalidatedSummaries,
			domain.SummaryRef{SummaryID: second.SummaryID, Revision: second.Revision},
		)
		assert.NotContains(
			t,
			next.InvalidatedSummaries,
			domain.SummaryRef{SummaryID: first.SummaryID, Revision: first.Revision},
		)
	})
}
