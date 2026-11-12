package secretary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func longUserPolicyJob(policy string) domain.SecretaryJob {
	job := testJob()
	job.BatchID = "long-user-batch"
	job.InputPolicyVersion = policy
	message := &job.Messages[0]
	message.Role = "user"
	message.Body = "Opening request. " + strings.Repeat("x", 20*1024) +
		" Hidden source conclusion. " + strings.Repeat("y", 20*1024) + " Closing question."
	message.ContentHash = domain.SearchContentHash(message.Body)
	return job
}

func longUserPolicyOutput(t *testing.T, evidence generatedEvidence) string {
	t.Helper()
	summary := newNodeProposal()
	summary.Claims = []generatedClaim{{
		Text:     "Supported conclusion",
		Evidence: []generatedEvidence{evidence},
	}}
	return nodeBatchOutput(t, []generatedNodeSummary{summary}, summarizedNodeOutcome())
}

func TestNodePromptGivenFrozenLongUserPolicyThenPreservesItsOriginalVisibility(t *testing.T) {
	for name, policy := range map[string]string{
		"unversioned frozen batch": "",
		"legacy frozen batch":      domain.SecretaryLegacyInputPolicyVersion,
		"selective frozen batch":   domain.SecretaryInputPolicyVersion,
	} {
		t.Run(name, func(t *testing.T) {
			job := longUserPolicyJob(policy)
			prompt, err := BuildNodePrompt(job)
			require.NoError(t, err)
			var payload struct {
				Messages []struct {
					Text string `json:"text"`
					View string `json:"view"`
					Hash string `json:"content_hash"`
				} `json:"changed_messages"`
			}
			require.NoError(
				t,
				json.Unmarshal(
					[]byte(strings.TrimPrefix(prompt, nodeSummaryInstructions)),
					&payload,
				),
			)
			require.Len(t, payload.Messages, 1)
			message := payload.Messages[0]
			assert.Equal(t, job.Messages[0].ContentHash, message.Hash)
			assert.Contains(t, message.Text, "Opening request.")
			assert.Contains(t, message.Text, "Closing question.")
			if policy == domain.SecretaryInputPolicyVersion {
				assert.Equal(t, "head_tail", message.View)
				assert.NotContains(t, message.Text, "Hidden source conclusion.")
				assert.LessOrEqual(t, len(message.Text), domain.SecretaryUserPreviewBytes)
			} else {
				assert.Equal(t, "full", message.View)
				assert.Equal(t, job.Messages[0].Body, message.Text)
			}
		})
	}
}

func TestNodePublicationGivenLegacyFrozenPolicyThenAcceptsPreviouslySuppliedMiddleEvidence(
	t *testing.T,
) {
	for name, policy := range map[string]string{
		"unversioned":     "",
		"explicit legacy": domain.SecretaryLegacyInputPolicyVersion,
	} {
		t.Run(name, func(t *testing.T) {
			job := longUserPolicyJob(policy)
			publication, err := BuildNodePublication(
				job,
				longUserPolicyOutput(
					t,
					generatedEvidence{MessageID: "message-1", Quote: "Hidden source conclusion."},
				),
				domain.SecretaryToolReads{},
			)
			require.NoError(t, err)
			require.Len(t, publication.Outputs, 1)
			evidence := publication.Outputs[0].Request.Claims[0].Evidence[0]
			assert.Equal(t, "full", evidence.View)
			assert.Equal(t, messageReference(job.Messages[0]), evidence.Message)
		})
	}
}

func TestNodePublicationGivenSelectiveLongUserPolicyThenRequiresActualReadForMiddleEvidence(
	t *testing.T,
) {
	job := longUserPolicyJob(domain.SecretaryInputPolicyVersion)
	evidence := generatedEvidence{MessageID: "message-1", Quote: "Hidden source conclusion."}
	_, err := BuildNodePublication(
		job,
		longUserPolicyOutput(t, evidence),
		domain.SecretaryToolReads{},
	)
	assert.Equal(t, "evidence", domain.SecretaryValidationFailureCode(err))

	ref := messageReference(job.Messages[0])
	ref.ReadReceipt = "verified-full-source-page"
	evidence.View, evidence.ReadReceipt = "full", ref.ReadReceipt
	reads := domain.SecretaryToolReads{Messages: []domain.SecretaryReadMessage{{
		Ref: ref, Text: job.Messages[0].Body, View: "full", ReadReceipt: ref.ReadReceipt,
	}}}
	output := longUserPolicyOutput(t, evidence)
	requested, err := RequestedNodeSourceReads(job, output)
	require.NoError(t, err)
	assert.Equal(t, []domain.MessageVersionRef{ref}, requested)
	publication, err := BuildNodePublication(job, output, reads)
	require.NoError(t, err)
	require.Len(t, publication.Outputs, 1)
	bound := publication.Outputs[0].Request.Claims[0].Evidence[0]
	assert.Equal(t, "full", bound.View)
	assert.Equal(t, ref.ReadReceipt, bound.ReadReceipt)
	assert.Equal(t, ref, bound.Message)

	for name, mutate := range map[string]func(*domain.SecretaryToolReads){
		"missing proof":     func(reads *domain.SecretaryToolReads) { reads.Messages = nil },
		"different version": func(reads *domain.SecretaryToolReads) { reads.Messages[0].Ref.ContentHash = "different" },
		"different view":    func(reads *domain.SecretaryToolReads) { reads.Messages[0].View = "last" },
		"unread middle":     func(reads *domain.SecretaryToolReads) { reads.Messages[0].Text = "Closing question." },
	} {
		t.Run(name, func(t *testing.T) {
			changed := domain.SecretaryToolReads{
				Messages: append([]domain.SecretaryReadMessage(nil), reads.Messages...),
			}
			mutate(&changed)
			_, err := BuildNodePublication(job, output, changed)
			assert.Equal(t, "evidence", domain.SecretaryValidationFailureCode(err))
		})
	}
}

func TestNodePublicationGivenSelectiveLongUserPolicyThenAcceptsRealEndsAndRejectsSyntheticQuotes(
	t *testing.T,
) {
	job := longUserPolicyJob(domain.SecretaryInputPolicyVersion)
	preview, _ := domain.SecretaryMessageView(job.Messages[0].Body, "user")
	markerStart := strings.Index(preview, "\n\n[Middle omitted;")
	require.Greater(t, markerStart, 8)
	markerEnd := markerStart + 2 + strings.Index(preview[markerStart+2:], "]\n\n") + len("]\n\n")
	require.Greater(t, markerEnd, markerStart)
	for name, quote := range map[string]string{
		"opening": "Opening request.",
		"closing": "Closing question.",
	} {
		t.Run(name, func(t *testing.T) {
			publication, err := BuildNodePublication(job,
				longUserPolicyOutput(t, generatedEvidence{MessageID: "message-1", Quote: quote}),
				domain.SecretaryToolReads{})
			require.NoError(t, err)
			assert.Equal(t, "head_tail", publication.Outputs[0].Request.Claims[0].Evidence[0].View)
		})
	}
	for name, quote := range map[string]string{
		"marker":           "[Middle omitted;",
		"head into marker": preview[markerStart-8 : markerStart+14],
		"marker into tail": preview[markerEnd-8 : markerEnd+8],
		"synthetic join":   preview[markerStart-8 : markerEnd+8],
	} {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, preview, quote)
			assert.NotContains(t, job.Messages[0].Body, quote)
			_, err := BuildNodePublication(job,
				longUserPolicyOutput(t, generatedEvidence{MessageID: "message-1", Quote: quote}),
				domain.SecretaryToolReads{})
			assert.Equal(t, "evidence", domain.SecretaryValidationFailureCode(err))
		})
	}
}

func TestNodePromptAndPublicationGivenUnknownInputPolicyThenRejectInsteadOfReinterpreting(
	t *testing.T,
) {
	job := longUserPolicyJob("future-policy")
	_, err := BuildNodePrompt(job)
	assert.Error(t, err)
	_, err = BuildNodePublication(
		job,
		longUserPolicyOutput(
			t,
			generatedEvidence{MessageID: "message-1", Quote: "Opening request."},
		),
		domain.SecretaryToolReads{},
	)
	assert.Error(t, err)
}
