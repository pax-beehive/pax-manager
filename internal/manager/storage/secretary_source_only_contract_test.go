package storage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretarySourceOnlyGivenFreshBatchThenPublishesExactCoverageWithoutClaims(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.Equal(t, domain.SecretarySourceOnlyOutputPolicyVersion, job.OutputPolicyVersion)
		const output = `{"summaries":[{"action":"create","proposal_key":"decision","title":"Decision","body":"A concise summary.","source_message_ids":["m1"]}],"outcomes":[{"message_id":"m1","action":"summarized","summary_keys":["decision"]}]}`
		publication, err := secretary.BuildNodePublication(job, output, domain.SecretaryToolReads{})
		require.NoError(t, err)
		require.Len(t, publication.Outputs, 1)
		assert.Empty(t, publication.Outputs[0].Request.Claims)
		assert.Equal(
			t,
			[]domain.MessageVersionRef{secretaryReference(job.Messages[0])},
			publication.Outputs[0].Request.Coverage,
		)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				output,
				domain.SecretaryToolReads{},
			),
		)
		versions, err := f.store.PublishSecretaryBatch(t.Context(), job, publication)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		reader := f.store.(domain.SummaryStore)
		current, err := reader.GetSummary(t.Context(), "owner", versions[0].SummaryID, 0)
		require.NoError(t, err)
		assert.Equal(t, "A concise summary.", current.Body)
		assert.Empty(t, current.Claims)
		require.Len(t, current.Coverage, 1)
		assert.Equal(t, secretaryReference(job.Messages[0]), current.Coverage[0])
		frozen, err := reader.ReadMessageVersion(t.Context(), "owner", current.Coverage[0])
		require.NoError(t, err)
		assert.Equal(t, "original", frozen.Body)
		assert.Equal(t, domain.SearchContentHash("original"), frozen.ContentHash)
		_, err = reader.GetSummary(t.Context(), "other", current.SummaryID, 0)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		_, err = reader.ReadMessageVersion(t.Context(), "other", current.Coverage[0])
		assert.ErrorIs(t, err, domain.ErrNotFound)
		progress := secretaryProgress(t, f.store.(domain.SecretaryProgressStore))
		assert.Equal(t, 1, progress.Counts.CompletedMessages)
		assert.Zero(t, progress.Counts.PendingMessages)
	})
}

func TestSecretarySourceOnlyGivenLegacySummaryThenNewRevisionKeepsItsHistoryAndExactSources(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		work := secretaryClaim(t, f.store)
		work.OutputPolicyVersion = domain.SecretaryLegacyOutputPolicyVersion
		legacy, err := f.store.PrepareSecretaryBatch(t.Context(), work)
		require.NoError(t, err)
		legacy = f.begin(t, legacy)
		const firstOutput = `{"summaries":[{"action":"create","proposal_key":"decision","title":"Decision","body":"An earlier decision.","source_message_ids":["m1"],"claims":[{"text":"An earlier decision.","evidence":[{"message_id":"m1","quote":"original"}]}]}],"outcomes":[{"message_id":"m1","action":"summarized","summary_keys":["decision"]}]}`
		firstPublication, err := secretary.BuildNodePublication(
			legacy,
			firstOutput,
			domain.SecretaryToolReads{},
		)
		require.NoError(t, err)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				legacy,
				legacy.AttemptID,
				firstOutput,
				domain.SecretaryToolReads{},
			),
		)
		first, err := f.store.PublishSecretaryBatch(t.Context(), legacy, firstPublication)
		require.NoError(t, err)
		require.Len(t, first, 1)
		require.Len(t, first[0].Claims, 1)
		f.addMessage(t)
		currentJob := f.begin(t, f.prepare(t))
		require.Equal(
			t,
			domain.SecretarySourceOnlyOutputPolicyVersion,
			currentJob.OutputPolicyVersion,
		)
		require.Len(t, currentJob.CurrentSummaries, 1)
		assert.Equal(t, first[0].Claims, currentJob.CurrentSummaries[0].Claims)
		output := fmt.Sprintf(
			`{"summaries":[{"action":"revise","summary_id":%q,"expected_revision":%d,"title":"Decision","body":"An updated concise summary.","source_message_ids":["m1","m2"]}],"outcomes":[{"message_id":"m2","action":"summarized","summary_keys":[%q]}]}`,
			first[0].SummaryID,
			first[0].Revision,
			first[0].SummaryID,
		)
		publication, err := secretary.BuildNodePublication(
			currentJob,
			output,
			domain.SecretaryToolReads{},
		)
		require.NoError(t, err)
		require.Len(t, publication.Outputs, 1)
		assert.Empty(t, publication.Outputs[0].Request.Claims)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				currentJob,
				currentJob.AttemptID,
				output,
				domain.SecretaryToolReads{},
			),
		)
		versions, err := f.store.PublishSecretaryBatch(t.Context(), currentJob, publication)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		reader := f.store.(domain.SummaryStore)
		current, err := reader.GetSummary(t.Context(), "owner", first[0].SummaryID, 0)
		require.NoError(t, err)
		assert.Equal(t, first[0].Revision+1, current.Revision)
		assert.Empty(t, current.Claims)
		require.Len(t, current.Coverage, 2)
		old, err := reader.GetSummary(t.Context(), "owner", first[0].SummaryID, first[0].Revision)
		require.NoError(t, err)
		assert.Equal(t, first[0].Claims, old.Claims)
		assert.Equal(t, first[0].Coverage, old.Coverage)
		for _, ref := range current.Coverage {
			frozen, readErr := reader.ReadMessageVersion(t.Context(), "owner", ref)
			require.NoError(t, readErr)
			assert.Equal(t, ref.ContentHash, domain.SearchContentHash(frozen.Body))
		}
		progress := secretaryProgress(t, f.store.(domain.SecretaryProgressStore))
		assert.Equal(t, 2, progress.Counts.CompletedMessages)
		assert.Zero(t, progress.Counts.PendingMessages)
	})
}
