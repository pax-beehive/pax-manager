package secretary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestRollingPromptGivenStoredClaimsThenUsesTheRequestedOutputEvidenceShape(t *testing.T) {
	job := rollingTestJob()
	prompt, err := BuildPrompt(job)
	require.NoError(t, err)
	var payload struct {
		Current struct {
			Claims []map[string]json.RawMessage `json:"claims"`
		} `json:"current_summary"`
	}
	require.NoError(
		t,
		json.Unmarshal([]byte(strings.TrimPrefix(prompt, rollingSummaryInstructions)), &payload),
	)
	require.Len(t, payload.Current.Claims, 1)
	claim := payload.Current.Claims[0]
	assert.NotContains(t, claim, "id")
	var evidence []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(claim["evidence"], &evidence))
	require.Len(t, evidence, 1)
	assert.JSONEq(
		t,
		`[{"message_id":"old-message","quote":"Earlier decision"}]`,
		string(claim["evidence"]),
	)
	assert.NotContains(t, evidence[0], "message")
	assert.NotContains(t, evidence[0], "view")
	assert.Equal(t, "old-claim", job.CurrentSummary.Claims[0].ID)
	assert.Equal(t, "full", job.CurrentSummary.Claims[0].Evidence[0].View)
	job.CurrentSummary.Claims[0].ID = strings.Repeat("x", 70*1024)
	_, err = BuildPrompt(job)
	assert.Error(
		t,
		err,
		"projecting wire fields must not bypass the authoritative baseline size limit",
	)
}

func copiedSummaryOutput(t *testing.T, claim domain.SummaryClaim) string {
	t.Helper()
	raw, err := json.Marshal(
		map[string]any{
			"body":   "Keep the earlier decision.",
			"claims": []domain.SummaryClaim{claim},
		},
	)
	require.NoError(t, err)
	return string(raw)
}

func TestRollingPublicationGivenCompletedStoredClaimEchoThenRecoversWithoutTrustingItsID(
	t *testing.T,
) {
	job := rollingTestJob()
	output := copiedSummaryOutput(t, job.CurrentSummary.Claims[0])
	request, err := BuildPublication(job, output)
	require.NoError(t, err)
	require.Len(t, request.Claims, 1)
	assert.Equal(t, "claim_1", request.Claims[0].ID)
	assert.Equal(t, job.CurrentSummary.Claims[0].Evidence, request.Claims[0].Evidence)
	selected, err := RequestedRelatedSources(output)
	require.NoError(t, err)
	assert.Empty(t, selected)
}

func TestRollingPublicationGivenConflictingStoredEvidenceEchoThenFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*domain.SecretaryJob, *domain.SummaryClaim){
		"unknown claim id": func(_ *domain.SecretaryJob, claim *domain.SummaryClaim) { claim.ID = "invented" },
		"wrong hash": func(_ *domain.SecretaryJob, claim *domain.SummaryClaim) {
			claim.Evidence[0].Message.ContentHash = "wrong"
		},
		"wrong canonicalization": func(_ *domain.SecretaryJob, claim *domain.SummaryClaim) {
			claim.Evidence[0].Message.CanonicalizationVersion = "wrong"
		},
		"incomplete reference": func(_ *domain.SecretaryJob, claim *domain.SummaryClaim) { claim.Evidence[0].Message.ContentHash = "" },
		"wrong view":           func(_ *domain.SecretaryJob, claim *domain.SummaryClaim) { claim.Evidence[0].View = "last" },
		"unexpected receipt":   func(_ *domain.SecretaryJob, claim *domain.SummaryClaim) { claim.Evidence[0].ReadReceipt = "forged" },
		"unexpected reference receipt": func(_ *domain.SecretaryJob, claim *domain.SummaryClaim) {
			claim.Evidence[0].Message.ReadReceipt = "forged"
		},
		"changed source": func(job *domain.SecretaryJob, _ *domain.SummaryClaim) {
			message := job.Messages[0]
			message.SourceID = "old-message"
			message.Body = "Earlier decision has changed"
			message.ContentHash = domain.SearchContentHash(message.Body)
			job.Messages = append(job.Messages, message)
		},
		"removed source": func(job *domain.SecretaryJob, _ *domain.SummaryClaim) {
			job.RemovedMessageIDs = []string{"old-message"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			job := rollingTestJob()
			claim := job.CurrentSummary.Claims[0]
			claim.Evidence = append([]domain.SummaryEvidence(nil), claim.Evidence...)
			mutate(&job, &claim)
			_, err := BuildPublication(job, copiedSummaryOutput(t, claim))
			assert.Error(t, err)
		})
	}
}

func TestRollingPublicationGivenRichDeltaEvidenceThenChecksItsExactBoundVersion(t *testing.T) {
	job := testJob()
	claim := domain.SummaryClaim{
		Text: "Use the database",
		Evidence: []domain.SummaryEvidence{
			{Message: messageReference(job.Messages[0]), Quote: "database", View: "full"},
		},
	}
	request, err := BuildPublication(job, copiedSummaryOutput(t, claim))
	require.NoError(t, err)
	assert.Equal(t, claim.Evidence, request.Claims[0].Evidence)
	claim.Evidence[0].Message.ContentHash = "wrong"
	_, err = BuildPublication(job, copiedSummaryOutput(t, claim))
	assert.Error(t, err)
}

func TestRollingPublicationGivenRichVerifiedReadThenChecksVersionViewAndReceipts(t *testing.T) {
	job := testJob()
	ref := domain.MessageVersionRef{
		MessageID:               "related",
		ContentHash:             domain.SearchContentHash("Related text"),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		ReadReceipt:             "signed",
	}
	reads := domain.SecretaryToolReads{
		Messages: []domain.SecretaryReadMessage{
			{Ref: ref, Text: "Related text", View: "last", ReadReceipt: "signed"},
		},
	}
	for name, mutate := range map[string]func(*domain.SummaryEvidence){
		"matching":                func(*domain.SummaryEvidence) {},
		"wrong view":              func(e *domain.SummaryEvidence) { e.View = "full" },
		"wrong receipt":           func(e *domain.SummaryEvidence) { e.ReadReceipt = "wrong" },
		"wrong reference receipt": func(e *domain.SummaryEvidence) { e.Message.ReadReceipt = "wrong" },
	} {
		t.Run(name, func(t *testing.T) {
			evidence := domain.SummaryEvidence{
				Message:     ref,
				Quote:       "Related",
				View:        "last",
				ReadReceipt: "signed",
			}
			mutate(&evidence)
			output, err := json.Marshal(
				map[string]any{
					"body": "Related",
					"claims": []domain.SummaryClaim{
						{Text: "Related", Evidence: []domain.SummaryEvidence{evidence}},
					},
					"related_sources": []domain.MessageVersionRef{ref},
				},
			)
			require.NoError(t, err)
			_, err = BuildPublicationWithReads(job, string(output), reads)
			if name == "matching" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestRollingPublicationGivenConflictingAliasOrUnknownFieldsThenRejects(t *testing.T) {
	for _, output := range []string{
		`{"body":"x","claims":[{"id":"unknown","text":"x"}]}`,
		`{"body":"x","claims":[{"text":"x","evidence":[{"message_id":"other","message":{"message_id":"message-1","content_hash":"x","canonicalization_version":"v1"},"quote":"database"}]}]}`,
		`{"body":"x","claims":[{"text":"x","authority":"system"}]}`,
		`{"body":"x","claims":[{"text":"x","evidence":[{"message":{"message_id":"message-1","content_hash":"x","canonicalization_version":"v1","authority":"system"},"quote":"database"}]}]}`,
		`{"body":"x","claims":[{"text":"x","evidence":[{"message_id":"message-1","quote":"database","authority":"system"}]}]}`,
	} {
		_, err := BuildPublication(testJob(), output)
		assert.Error(t, err)
	}
}
