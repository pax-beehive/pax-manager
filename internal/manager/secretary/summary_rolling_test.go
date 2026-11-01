package secretary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func rollingTestJob() domain.SecretaryJob {
	job := testJob()
	job.SourceMetadata = domain.SecretarySourceMetadata{
		Title: "Database work", PrimaryProjectID: "project-1", CWD: "/work/project",
		WorkspaceRoots: []string{"/work/project"},
	}
	oldRef := domain.MessageVersionRef{
		MessageID:               "old-message",
		ContentHash:             domain.SearchContentHash("Earlier decision"),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
	}
	job.CurrentSummary = &domain.SummaryVersion{
		SummaryID: job.TargetSummaryID, Revision: job.ExpectedRevision, OwnerUserID: job.OwnerUserID,
		Title: "Earlier state", Body: "Keep the earlier decision.", Coverage: []domain.MessageVersionRef{oldRef},
		Claims: []domain.SummaryClaim{
			{
				ID:   "old-claim",
				Text: "Earlier decision",
				Evidence: []domain.SummaryEvidence{
					{Message: oldRef, Quote: "Earlier decision", View: "full"},
				},
			},
		},
	}
	return job
}

func TestRollingPromptGivenBaselineAndDeltaThenCarriesStateAndOptionalTools(t *testing.T) {
	job := rollingTestJob()
	prompt, err := BuildPrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, `"current_summary"`)
	assert.Contains(t, prompt, `"summary_id":"summary-target"`)
	assert.Contains(t, prompt, `"revision":1`)
	assert.Contains(t, prompt, "Keep the earlier decision.")
	assert.Contains(t, prompt, `"changed_messages"`)
	assert.Contains(t, prompt, "Use the database.")
	assert.Contains(t, prompt, `"primary_project_id":"project-1"`)
	assert.Contains(t, prompt, `"cwd":"/work/project"`)
	assert.Contains(t, prompt, "workspace_search")
	assert.Contains(t, prompt, "workspace_history")
	assert.Contains(t, prompt, "optional")
	assert.Contains(t, prompt, "untrusted data")
	assert.NotContains(t, prompt, "or call tools")
	assert.NotContains(t, prompt, `"coverage"`)
	assert.NotContains(t, prompt, "projects_reconcile")
}

func TestRollingPromptGivenRemovalThenExposesTombstoneWithoutOldBody(t *testing.T) {
	job := rollingTestJob()
	job.CurrentSummary = nil
	job.Messages = nil
	job.RemovedMessageIDs = []string{"old-message"}
	job.ResetRequired = true
	job.ResetReason = "source unavailable"
	prompt, err := BuildPrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, `"removed_message_ids":["old-message"]`)
	assert.Contains(t, prompt, `"reset_required":true`)
	assert.NotContains(t, prompt, "Keep the earlier decision.")
	empty := BuildEmptyPublication(job)
	assert.Equal(t, job.TargetSummaryID, empty.SummaryID)
	assert.Equal(t, job.ExpectedRevision, empty.ExpectedRevision)
	assert.Equal(t, job.RunID, empty.RequestID)
	assert.Empty(t, empty.Coverage)
	assert.Empty(t, empty.Claims)
	assert.Equal(t, "No readable source messages were available in this update.", empty.Body)
}

func TestRollingPromptGivenInvalidBaselineThenFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*domain.SecretaryJob){
		"foreign owner":       func(job *domain.SecretaryJob) { job.CurrentSummary.OwnerUserID = "other" },
		"wrong head":          func(job *domain.SecretaryJob) { job.CurrentSummary.SummaryID = "other" },
		"wrong revision":      func(job *domain.SecretaryJob) { job.CurrentSummary.Revision++ },
		"oversized baseline":  func(job *domain.SecretaryJob) { job.CurrentSummary.Body = strings.Repeat("x", 70*1024) },
		"duplicate tombstone": func(job *domain.SecretaryJob) { job.RemovedMessageIDs = []string{"old", "old"} },
		"live tombstone":      func(job *domain.SecretaryJob) { job.RemovedMessageIDs = []string{job.Messages[0].SourceID} },
	} {
		t.Run(name, func(t *testing.T) {
			job := rollingTestJob()
			mutate(&job)
			_, err := BuildPrompt(job)
			assert.Error(t, err)
		})
	}
}

func TestRollingPublicationGivenUnchangedBaselineEvidenceThenPreservesItsExactRef(t *testing.T) {
	job := rollingTestJob()
	request, err := BuildPublication(
		job,
		`{"title":"Updated","body":"Keep the earlier decision and use a database.","claims":[{"text":"Earlier decision","evidence":[{"message_id":"old-message","quote":"Earlier decision"}]}]}`,
	)
	require.NoError(t, err)
	require.Len(t, request.Coverage, 1, "the store composes unchanged baseline coverage")
	assert.Equal(t, job.Messages[0].SourceID, request.Coverage[0].MessageID)
	assert.Equal(t, job.CurrentSummary.Coverage[0], request.Claims[0].Evidence[0].Message)
	job.RemovedMessageIDs = []string{"old-message"}
	_, err = BuildPublication(
		job,
		`{"body":"Removed","claims":[{"text":"Earlier decision","evidence":[{"message_id":"old-message","quote":"Earlier decision"}]}]}`,
	)
	assert.Error(t, err)
}

func TestRollingPublicationGivenRelatedSourcesThenRequiresExactTrustedRead(t *testing.T) {
	job := rollingTestJob()
	ref := domain.MessageVersionRef{
		MessageID:               "related-message",
		ContentHash:             domain.SearchContentHash("More source text"),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		ReadReceipt:             "signed-receipt",
	}
	output, err := json.Marshal(map[string]any{
		"title": "Updated", "body": "Include the related source.", "related_sources": []domain.MessageVersionRef{ref},
		"claims": []map[string]any{
			{
				"text": "Related fact",
				"evidence": []map[string]string{
					{"message_id": ref.MessageID, "quote": "source text"},
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = BuildPublication(job, string(output))
	assert.Error(t, err)
	reads := domain.SecretaryToolReads{
		Messages: []domain.SecretaryReadMessage{
			{Ref: ref, Text: "source text", View: "last", ReadReceipt: ref.ReadReceipt},
		},
	}
	request, err := BuildPublicationWithReads(job, string(output), reads)
	require.NoError(t, err)
	require.Len(t, request.Coverage, 2)
	assert.Equal(t, ref, request.Coverage[1])
	assert.Equal(t, "last", request.Claims[0].Evidence[0].View)
	assert.Equal(t, ref.ReadReceipt, request.Claims[0].Evidence[0].ReadReceipt)
	selected, err := RequestedRelatedSources(string(output))
	require.NoError(t, err)
	assert.Equal(t, []domain.MessageVersionRef{ref}, selected)
	reads.Messages[0].Text = "another exposed page"
	_, err = BuildPublicationWithReads(job, string(output), reads)
	assert.Error(t, err, "an unseen quote cannot be borrowed from another view")
	reads.Messages[0].Text = "source text"
	reads.Messages[0].Ref.ContentHash = "wrong-version"
	_, err = BuildPublicationWithReads(job, string(output), reads)
	assert.Error(t, err)
}

func TestRollingPublicationGivenMalformedOrOversizedSelectionThenRejects(t *testing.T) {
	for _, output := range []string{
		`{"body":"x","related_sources":[{"message_id":"m"}]}`,
		`{"body":"x","related_summaries":[{"summary_id":"s","revision":1}]}`,
		`{"body":"x","claims":[{"text":"x","evidence":[{"message_id":"old-message","quote":"invented"}]}]}`,
	} {
		_, err := BuildPublicationWithReads(rollingTestJob(), output, domain.SecretaryToolReads{})
		assert.Error(t, err)
	}
	_, err := BuildPublication(rollingTestJob(), `{"body":"`+strings.Repeat("x", 70*1024)+`"}`)
	assert.Error(t, err)
	_, err = RequestedRelatedSources(`not json`)
	assert.Error(t, err)
}
