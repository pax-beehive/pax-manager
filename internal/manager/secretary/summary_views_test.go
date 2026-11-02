package secretary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func paragraphJob() domain.SecretaryJob {
	job := testJob()
	job.Messages[0].Role = "assistant"
	job.Messages[0].Body = "Earlier detailed explanation.\n\nFinal decision."
	job.Messages[0].ContentHash = domain.SearchContentHash(job.Messages[0].Body)
	return job
}

func TestRollingPromptGivenDefaultViewsThenSendsUserFullAndAssistantLast(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			job := paragraphJob()
			job.Messages[0].Role = role
			prompt, err := BuildPrompt(job)
			require.NoError(t, err)
			var payload struct {
				Messages []struct {
					Text  string   `json:"text"`
					View  string   `json:"view"`
					Views []string `json:"available_views"`
					Hash  string   `json:"content_hash"`
				} `json:"changed_messages"`
			}
			require.NoError(
				t,
				json.Unmarshal(
					[]byte(strings.TrimPrefix(prompt, rollingSummaryInstructions)),
					&payload,
				),
			)
			require.Len(t, payload.Messages, 1)
			message := payload.Messages[0]
			assert.Equal(t, job.Messages[0].ContentHash, message.Hash)
			assert.Equal(t, []string{"first", "last", "full"}, message.Views)
			if role == "assistant" {
				assert.Equal(t, "last", message.View)
				assert.Equal(t, "Final decision.", message.Text)
			} else {
				assert.Equal(t, "full", message.View)
				assert.Equal(t, job.Messages[0].Body, message.Text)
			}
		})
	}
}

func TestRollingPublicationGivenCompactPromptThenRejectsUnseenQuoteWithoutExpansion(t *testing.T) {
	job := paragraphJob()
	_, err := BuildPublication(
		job,
		`{"body":"Updated","claims":[{"text":"Earlier","evidence":[{"message_id":"message-1","quote":"Earlier detailed"}]}]}`,
	)
	assert.Error(t, err)
	request, err := BuildPublication(
		job,
		`{"body":"Updated","claims":[{"text":"Decision","evidence":[{"message_id":"message-1","quote":"Final decision"}]}]}`,
	)
	require.NoError(t, err)
	assert.Equal(t, "last", request.Claims[0].Evidence[0].View)
	assert.Equal(t, messageReference(job.Messages[0]), request.Coverage[0])
}

func TestRollingPublicationGivenOwnDeltaExpansionThenRequiresExactReadProof(t *testing.T) {
	job := paragraphJob()
	ref := messageReference(job.Messages[0])
	ref.ReadReceipt = "signed-full-page"
	output := `{"body":"Updated","claims":[{"text":"Earlier","evidence":[{"message_id":"message-1","quote":"Earlier detailed","view":"full","read_receipt":"signed-full-page"}]}]}`
	requested, err := RequestedSourceReads(job, output)
	require.NoError(t, err)
	assert.Equal(t, []domain.MessageVersionRef{ref}, requested)
	reads := domain.SecretaryToolReads{
		Messages: []domain.SecretaryReadMessage{
			{Ref: ref, Text: job.Messages[0].Body, View: "full", ReadReceipt: ref.ReadReceipt},
		},
	}
	request, err := BuildPublicationWithReads(job, output, reads)
	require.NoError(t, err)
	assert.Len(t, request.Coverage, 1)
	assert.Equal(t, "full", request.Claims[0].Evidence[0].View)
	assert.Equal(t, ref.ReadReceipt, request.Claims[0].Evidence[0].ReadReceipt)
	_, err = BuildPublication(job, output)
	assert.Error(t, err)
	reads.Messages[0].Ref.ContentHash = "wrong"
	_, err = BuildPublicationWithReads(job, output, reads)
	assert.Error(t, err)
	reads.Messages[0].Ref = ref
	reads.Messages[0].Text = "Final decision."
	_, err = BuildPublicationWithReads(job, output, reads)
	assert.Error(t, err)
}

func TestRollingPromptGivenLargeHiddenBodyThenBudgetsExposedViewSeparately(t *testing.T) {
	job := paragraphJob()
	job.Messages[0].Body = strings.Repeat("x", domain.SecretaryMaxBytes+1) + "\n\nFinal decision."
	job.Messages[0].ContentHash = domain.SearchContentHash(job.Messages[0].Body)
	prompt, err := BuildPrompt(job)
	require.NoError(t, err)
	assert.Less(t, len(prompt), domain.SecretaryMaxBytes)
	job.Messages[0].Role = "user"
	_, err = BuildPrompt(job)
	assert.Error(t, err)
	job.Messages[0].Role = "assistant"
	job.Messages[0].Body = strings.Repeat(
		"x",
		domain.SecretaryMaxSourceBytes+1,
	) + "\n\nFinal decision."
	job.Messages[0].ContentHash = domain.SearchContentHash(job.Messages[0].Body)
	_, err = BuildPrompt(job)
	assert.Error(t, err)
}
