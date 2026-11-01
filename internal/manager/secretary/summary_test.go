package secretary

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func testJob() domain.SecretaryJob {
	body := "Use the database."
	return domain.SecretaryJob{
		SourceID:         "12",
		OwnerUserID:      "owner",
		SourceAgentID:    "source-agent",
		SourceSessionID:  "source-session",
		AgentID:          "secretary-agent",
		LeaseToken:       "lease",
		SettingsRevision: 1,
		ThroughRevision:  2,
		RunID:            "run",
		TargetSummaryID:  "summary-target",
		ExpectedRevision: 1,
		Messages: []domain.SearchDocument{
			{
				Kind:                    "message",
				SourceID:                "message-1",
				OwnerUserID:             "owner",
				AgentID:                 "source-agent",
				SessionID:               "source-session",
				Body:                    body,
				ContentHash:             domain.SearchContentHash(body),
				CanonicalizationVersion: domain.SearchCanonicalizationVersion,
			},
		},
	}
}

func TestSummaryPromptGivenFullSourcesThenCarriesExactBodyAndTreatsHistoryAsData(t *testing.T) {
	job := testJob()
	job.PreviousSummaries = []domain.SummaryVersion{
		{
			Title:       "Earlier",
			Body:        "Earlier progress",
			SummaryID:   job.TargetSummaryID,
			Revision:    1,
			OwnerUserID: job.OwnerUserID,
		},
		{
			Title:       "Private",
			Body:        "Foreign private progress",
			SummaryID:   "foreign",
			Revision:    1,
			OwnerUserID: "other",
		},
	}
	job.CurrentSummary = &job.PreviousSummaries[0]
	prompt, err := BuildPrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, "untrusted data")
	assert.Contains(t, prompt, "Use the database.")
	assert.Contains(t, prompt, "Earlier progress")
	assert.NotContains(t, prompt, "Foreign private progress")
	assert.Contains(t, prompt, "message-1")
	job.PreviousSummaries[0].Body = strings.Repeat("x", 70*1024)
	_, err = BuildPrompt(job)
	require.Error(t, err, "an oversized baseline must not be silently truncated")
}

func TestSummaryPromptGivenInvalidOrOversizedSourcesThenNeverTruncatesCoverage(t *testing.T) {
	for name, mutate := range map[string]func(*domain.SecretaryJob){
		"empty": func(job *domain.SecretaryJob) { job.Messages = nil },
		"too many": func(job *domain.SecretaryJob) {
			for len(job.Messages) <= domain.SecretaryMaxMessages {
				job.Messages = append(job.Messages, job.Messages[0])
			}
		},
		"oversized":       func(job *domain.SecretaryJob) { job.Messages[0].Body = strings.Repeat("x", domain.SecretaryMaxBytes+1) },
		"wrong hash":      func(job *domain.SecretaryJob) { job.Messages[0].ContentHash = "wrong" },
		"foreign owner":   func(job *domain.SecretaryJob) { job.Messages[0].OwnerUserID = "other" },
		"foreign session": func(job *domain.SecretaryJob) { job.Messages[0].SessionID = "other" },
		"deleted":         func(job *domain.SecretaryJob) { job.Messages[0].Deleted = true },
		"duplicate":       func(job *domain.SecretaryJob) { job.Messages = append(job.Messages, job.Messages[0]) },
	} {
		t.Run(
			name,
			func(t *testing.T) { job := testJob(); mutate(&job); _, err := BuildPrompt(job); assert.Error(t, err) },
		)
	}
}

func TestSummaryOutputGivenValidJSONThenManagerBindsEverySourceAndEvidence(t *testing.T) {
	job := testJob()
	request, err := BuildPublication(
		job,
		`{"title":"Decision","body":"Use a database.","claims":[{"text":"Use a database","kind":"decision","evidence":[{"message_id":"message-1","quote":"database"}]}]}`,
	)
	require.NoError(t, err)
	assert.Equal(t, job.TargetSummaryID, request.SummaryID)
	assert.Equal(t, job.ExpectedRevision, request.ExpectedRevision)
	assert.Equal(t, job.RunID, request.RequestID)
	assert.Equal(t, "fragment", request.SubjectType)
	assert.Equal(t, "secretary:session:12", request.SubjectID)
	require.Len(t, request.Coverage, 1)
	assert.Equal(t, job.Messages[0].ContentHash, request.Coverage[0].ContentHash)
	assert.Empty(t, request.SummarySources)
	require.Len(t, request.Claims, 1)
	assert.Equal(t, "claim_1", request.Claims[0].ID)
	assert.Equal(t, request.Coverage[0], request.Claims[0].Evidence[0].Message)
	assert.Equal(t, "full", request.Claims[0].Evidence[0].View)
	require.NoError(t, request.Validate())
}

func TestSummaryOutputGivenInvalidSchemaOrEvidenceThenRejectsPublication(t *testing.T) {
	for _, output := range []string{
		`not json`, `null`, `{}`, `{"title":"x","body":" "}`, `{"body":"x","summary_id":"attacker"}`,
		`{"body":"x"} {"body":"other"}`, "```json\n{\"body\":\"x\"}\n```",
		`{"body":"x","claims":[{"text":""}]}`,
		`{"body":"x","claims":[{"text":"decision","evidence":[{"message_id":"other","quote":"database"}]}]}`,
		`{"body":"x","claims":[{"text":"decision","evidence":[{"message_id":"message-1","quote":"not in source"}]}]}`,
	} {
		_, err := BuildPublication(testJob(), output)
		assert.Error(t, err, output)
	}
	_, err := BuildPublication(testJob(), strings.Repeat("x", 1024*1024+1))
	assert.Error(t, err)
}
