package storage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryRetainedQuoteGivenShorterVerifiedSubstringThenPersistsExactVersionWithoutChangingHistory(
	t *testing.T,
) {
	const longQuote = "The verified decision is to use a durable queue for retries."
	const shortQuote = "use a durable queue"
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		body := "Synthetic setup paragraph.\n\n" + longQuote
		if f.memory != nil {
			secretaryMemoryMessage(f.memory, "m1", body, 1)
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(),
				`UPDATE message_parts SET text=$1 WHERE message_id='m1'`, body)
			require.NoError(t, err)
		}
		firstJob := secretaryNodesReadyJob(t, f.store)
		firstPublication, err := secretary.BuildNodePublication(firstJob,
			retainedQuoteNodeOutput(t, "", 0, longQuote, "m1"), domain.SecretaryToolReads{})
		require.NoError(t, err)
		firstVersions, err := f.store.PublishSecretaryBatch(t.Context(), firstJob, firstPublication)
		require.NoError(t, err)
		require.Len(t, firstVersions, 1)
		original := firstVersions[0]
		require.Len(t, original.Claims, 1)
		require.Len(t, original.Claims[0].Evidence, 1)
		oldEvidence := original.Claims[0].Evidence[0]
		require.Equal(t, longQuote, oldEvidence.Quote)
		require.Equal(t, "last", oldEvidence.View)

		f.addMessage(t)
		next := secretaryNodesReadyJob(t, f.store)
		require.Len(t, next.Messages, 1)
		require.Equal(t, "m2", next.Messages[0].SourceID,
			"the original message must only be available through its persisted baseline")
		require.Len(t, next.CurrentSummaries, 1)
		require.Equal(t, original.Claims, next.CurrentSummaries[0].Claims)
		_, err = secretary.BuildNodePublication(next,
			retainedQuoteNodeOutput(t, original.SummaryID, original.Revision,
				"use an ephemeral queue", "m2"), domain.SecretaryToolReads{})
		assert.ErrorContains(
			t,
			err,
			"secretary evidence was not present in the supplied or verified text",
			"a paraphrase must not inherit the old proof",
		)

		publication, err := secretary.BuildNodePublication(next,
			retainedQuoteNodeOutput(t, original.SummaryID, original.Revision, shortQuote, "m2"),
			domain.SecretaryToolReads{})
		require.NoError(
			t,
			err,
			"a contiguous substring of already verified evidence remains verified",
		)
		versions, err := f.store.PublishSecretaryBatch(t.Context(), next, publication)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		assert.EqualValues(t, 2, versions[0].Revision)
		reader := f.store.(domain.SummaryStore)
		latest, err := reader.GetSummary(t.Context(), next.OwnerUserID, original.SummaryID, 0)
		require.NoError(t, err)
		require.Len(t, latest.Claims, 1)
		require.Len(t, latest.Claims[0].Evidence, 1)
		want := oldEvidence
		want.Quote = shortQuote
		assert.Equal(t, want, latest.Claims[0].Evidence[0])
		assert.Equal(t, original.Coverage, latest.Coverage)
		historical, err := reader.GetSummary(t.Context(), next.OwnerUserID,
			original.SummaryID, original.Revision)
		require.NoError(t, err)
		assert.Equal(t, original.Claims, historical.Claims)
		assert.Equal(t, original.Coverage, historical.Coverage)
		assert.Equal(t, longQuote, historical.Claims[0].Evidence[0].Quote)
	})
}

func retainedQuoteNodeOutput(
	t *testing.T,
	id string,
	revision int64,
	quote, changed string,
) string {
	t.Helper()
	summary := map[string]any{
		"action": "create", "proposal_key": "decision", "title": "Verified decision", "body": "Use a durable queue.",
		"source_message_ids": []string{"m1"},
		"claims": []any{map[string]any{"text": "Use a durable queue.",
			"evidence": []any{map[string]string{"message_id": "m1", "quote": quote}}}},
	}
	outcome := map[string]any{
		"message_id":   changed,
		"action":       "summarized",
		"summary_keys": []string{"decision"},
	}
	if id != "" {
		delete(summary, "proposal_key")
		summary["action"], summary["summary_id"], summary["expected_revision"] = "revise", id, revision
		outcome = map[string]any{
			"message_id": changed,
			"action":     "no_summary",
			"reason":     "No additional supported conclusion.",
		}
	}
	raw, err := json.Marshal(
		map[string]any{"summaries": []any{summary}, "outcomes": []any{outcome}},
	)
	require.NoError(t, err)
	return string(raw)
}
