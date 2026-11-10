package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryCompactBudgetPublication(job domain.SecretaryJob) domain.SecretaryBatchPublication {
	publication := secretaryNodesPublication(job, "first-issue")
	request := &publication.Outputs[0].Request
	request.Body = strings.Repeat("x", 43*1024)
	request.Claims = []domain.SummaryClaim{
		{
			ID:   "claim_1",
			Text: "Fact 0",
			Evidence: []domain.SummaryEvidence{
				{Message: request.Coverage[0], Quote: job.Messages[0].Body},
			},
		},
	}
	for i := 1; i < 100; i++ {
		claim := request.Claims[0]
		claim.ID = fmt.Sprintf("claim_%d", i+1)
		claim.Text = fmt.Sprintf("Fact %d", i)
		request.Claims = append(request.Claims, claim)
	}
	return publication
}

func TestSecretaryBudgetGivenRepeatedVersionsThenFitsAndPreservesTheOriginalHash(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	publication := secretaryCompactBudgetPublication(job)
	encoded, err := domain.EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	requests, hash, err := secretaryNodePublication(job, publication)
	require.NoError(t, err)
	canonical := struct {
		RequestID string
		Requests  []domain.PublishSummaryRequest
		Outcomes  []domain.SecretaryMessageOutcome
	}{publication.RequestID, requests, publication.Outcomes}
	original, err := json.Marshal(canonical)
	require.NoError(t, err)
	assert.Greater(t, len(original), 64*1024)
	assert.LessOrEqual(t, len(encoded), 64*1024)
	assert.Equal(t, domain.SearchContentHash(string(original)), hash)
	assert.NotEqual(t, domain.SearchContentHash(string(encoded)), hash)
	versions, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Len(t, versions[0].Claims, 100)
}

func TestSecretaryBudgetPostgresGivenRepeatedVersionsThenCommitsCompactClaimsWithin64KiB(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	publication := secretaryCompactBudgetPublication(job)
	_, err := domain.EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	versions, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	var raw []byte
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT claims_json FROM summary_versions WHERE summary_id=$1 AND revision=1`, versions[0].SummaryID).
			Scan(&raw),
	)
	assert.NotContains(t, string(raw), "content_hash")
	assert.Contains(t, string(raw), `"source_key"`)
	read, err := store.GetSummary(t.Context(), job.OwnerUserID, versions[0].SummaryID, 1)
	require.NoError(t, err)
	assert.Equal(t, versions[0].Claims, read.Claims)
	assert.Len(t, read.Claims, 100)
	replay, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	assert.Equal(t, versions, replay)
}

func TestSecretaryBudgetGivenOversizedNodeOrBatchThenRejectsBeforePersistence(t *testing.T) {
	for _, node := range []bool{true, false} {
		t.Run(map[bool]string{true: "node", false: "batch"}[node], func(t *testing.T) {
			store, _ := secretaryMemoryFixture(t)
			secretaryEnable(t, store)
			job := secretaryNodesReadyJob(t, store)
			keys := []string{"one", "two", "three", "four", "five"}
			size, expected := 33*1024, domain.ErrSecretaryBatchPublicationTooLarge
			if node {
				keys, size = keys[:1], 64*1024
			}
			publication := secretaryNodesPublication(job, keys...)
			for i := range publication.Outputs {
				publication.Outputs[i].Request.Body = strings.Repeat("x", size)
			}
			_, sharedErr := domain.EncodeSecretaryPublication(publication)
			assert.ErrorIs(t, sharedErr, expected)
			_, err := store.PublishSecretaryBatch(t.Context(), job, publication)
			assert.ErrorIs(t, err, expected)
			assert.ErrorIs(t, err, ErrConflict)
			assert.Empty(t, store.secretaryLocked().summaries)
			assert.Zero(t, store.secretaryLocked().jobs[job.SourceID].processed)
		})
	}
}

func TestSecretaryBudgetGivenEmptyInvalidationRepairThenMatchesPersistedCanonicalHash(
	t *testing.T,
) {
	job := domain.SecretaryJob{BatchID: "batch", SourceID: "source",
		InvalidatedSummaries: []domain.SummaryRef{{SummaryID: "summary", Revision: 1}}}
	publication := domain.SecretaryBatchPublication{RequestID: job.BatchID,
		Outputs: []domain.SecretarySummaryOutput{{Request: domain.PublishSummaryRequest{
			SummaryID: "summary", ExpectedRevision: 1, SubjectType: "fragment",
			SubjectID: secretarySubject(job.SourceID), Title: "Unavailable summary",
			Body: domain.SecretaryEmptySummaryBody, Coverage: []domain.MessageVersionRef{},
		}}}}
	encoded, err := domain.EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	requests, hash, err := secretaryNodePublication(job, publication)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Nil(t, requests[0].Coverage)
	assert.Equal(t, domain.SearchContentHash(string(encoded)), hash)
}
