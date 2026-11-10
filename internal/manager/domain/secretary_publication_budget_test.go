package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func publicationBudgetFixture() SecretaryBatchPublication {
	ref := MessageVersionRef{
		MessageID:               "message",
		ContentHash:             SearchContentHash("source"),
		CanonicalizationVersion: SearchCanonicalizationVersion,
		ReadReceipt:             "coverage-proof",
	}
	return SecretaryBatchPublication{
		RequestID: "batch",
		Outputs: []SecretarySummaryOutput{
			{
				ProposalKey: "issue",
				Request: PublishSummaryRequest{
					SubjectType: "fragment",
					SubjectID:   "secretary:session:1",
					RequestID:   "untrusted-request",
					Title:       "Issue",
					Body:        "Summary",
					Coverage:    []MessageVersionRef{ref},
					Claims: []SummaryClaim{
						{
							ID:         "claim_1",
							Text:       "Fact",
							Kind:       "decision",
							Basis:      "user_explicit",
							State:      "active",
							Supersedes: []string{"claim_0"},
							Evidence: []SummaryEvidence{
								{
									Message:     ref,
									Quote:       "source",
									View:        "full",
									ReadReceipt: "evidence-proof",
								},
							},
						},
					},
				},
			},
		},
		Outcomes: []SecretaryMessageOutcome{
			{MessageID: "message", Action: "summarized", SummaryKeys: []string{"issue"}},
		},
	}
}

func TestSecretaryPublicationBudgetGivenBoundReferencesThenStoresEachVersionOnce(t *testing.T) {
	publication := publicationBudgetFixture()
	raw, err := EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	var actual struct {
		RequestID string
		Requests  []struct {
			SummaryID string              `json:"summary_id"`
			RequestID string              `json:"request_id"`
			Coverage  []MessageVersionRef `json:"coverage"`
			Claims    []struct {
				ID       string           `json:"id"`
				Evidence []map[string]any `json:"evidence"`
			} `json:"claims"`
		}
		Outcomes []SecretaryMessageOutcome
	}
	require.NoError(t, json.Unmarshal(raw, &actual))
	require.Len(t, actual.Requests, 1)
	request := actual.Requests[0]
	assert.Equal(t, SecretaryNodeID("batch", "issue"), request.SummaryID)
	assert.Equal(t, "batch:"+request.SummaryID, request.RequestID)
	assert.Empty(t, request.Coverage[0].ReadReceipt)
	assert.Equal(t, float64(0), request.Claims[0].Evidence[0]["source_key"])
	assert.NotContains(t, request.Claims[0].Evidence[0], "message")
	assert.NotContains(t, request.Claims[0].Evidence[0], "read_receipt")
	assert.Equal(t, "full", request.Claims[0].Evidence[0]["view"])
	assert.Equal(t, "coverage-proof", publication.Outputs[0].Request.Coverage[0].ReadReceipt)
	assert.Equal(
		t,
		"evidence-proof",
		publication.Outputs[0].Request.Claims[0].Evidence[0].ReadReceipt,
	)
	publication.Outputs[0].Request.Coverage[0].ReadReceipt = strings.Repeat("proof", 100000)
	publication.Outputs[0].Request.Claims[0].Evidence[0].ReadReceipt = strings.Repeat(
		"proof",
		100000,
	)
	publication.Outputs[0].Request.Claims[0].Evidence[0].Message.ReadReceipt = strings.Repeat(
		"proof",
		100000,
	)
	again, err := EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	assert.Equal(t, string(raw), string(again))
}

func TestSecretaryPublicationBudgetGivenEncodedBoundaryThenKeepsOriginal64KiBLimit(t *testing.T) {
	publication := publicationBudgetFixture()
	publication.Outputs[0].Request.Body = ""
	base, err := EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	token := "<>&\"\\\u6570\u636e\u2028"
	encoded, err := json.Marshal(token)
	require.NoError(t, err)
	unit := len(encoded) - 2
	available := 64*1024 - len(base)
	publication.Outputs[0].Request.Body = strings.Repeat(
		token,
		available/unit,
	) + strings.Repeat(
		"x",
		available%unit,
	)
	raw, err := EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	assert.Len(t, raw, 64*1024)
	publication.Outputs[0].Request.Body += "x"
	_, err = EncodeSecretaryPublication(publication)
	assert.ErrorIs(t, err, ErrSecretaryBatchPublicationTooLarge)
}

func TestSecretaryPublicationBudgetGivenOnlyOutcomesThenCountsTheirBytes(t *testing.T) {
	publication := SecretaryBatchPublication{RequestID: "batch"}
	for i := 0; i < 17; i++ {
		publication.Outcomes = append(
			publication.Outcomes,
			SecretaryMessageOutcome{
				MessageID: fmt.Sprintf("message-%d", i),
				Action:    "no_summary",
				Reason:    strings.Repeat("x", 4096),
			},
		)
	}
	_, err := EncodeSecretaryPublication(publication)
	assert.ErrorIs(t, err, ErrSecretaryBatchPublicationTooLarge)
}

func TestCompactSummaryClaimsGivenUnknownOrAmbiguousVersionThenRejects(t *testing.T) {
	for _, mode := range []string{"missing", "wrong_version", "duplicate_coverage"} {
		t.Run(mode, func(t *testing.T) {
			request := publicationBudgetFixture().Outputs[0].Request
			switch mode {
			case "missing":
				request.Coverage = nil
			case "wrong_version":
				request.Coverage[0].ContentHash = SearchContentHash("changed")
			case "duplicate_coverage":
				request.Coverage = append(request.Coverage, request.Coverage[0])
			}
			_, err := EncodeCompactSummaryClaims(request.Claims, request.Coverage)
			assert.ErrorIs(t, err, ErrConflict)
		})
	}
}

func TestSecretaryPublicationBudgetGivenEmptyCoverageThenUsesPersistedNullEncoding(t *testing.T) {
	publication := publicationBudgetFixture()
	publication.Outputs[0].Request.Claims = nil
	publication.Outputs[0].Request.Coverage = nil
	nilCoverage, err := EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	publication.Outputs[0].Request.Coverage = []MessageVersionRef{}
	emptyCoverage, err := EncodeSecretaryPublication(publication)
	require.NoError(t, err)
	assert.Equal(t, string(nilCoverage), string(emptyCoverage))
	assert.NotNil(t, publication.Outputs[0].Request.Coverage)
}
