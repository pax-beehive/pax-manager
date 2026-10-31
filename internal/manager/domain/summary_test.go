package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummaryGivenCoverageWhenValidatedThenClaimsMustUseCoveredVersions(t *testing.T) {
	t.Parallel()
	ref := MessageVersionRef{
		MessageID:               "message_1",
		CanonicalizationVersion: "visible-v1",
		ContentHash:             "hash_1",
	}
	request := PublishSummaryRequest{
		SummaryID: "summary_1", SubjectType: "topic", SubjectID: "topic_1", RequestID: "request_1",
		Body: "A decision", Coverage: []MessageVersionRef{ref},
		Claims: []SummaryClaim{
			{
				ID:       "claim_1",
				Text:     "A decision",
				Evidence: []SummaryEvidence{{Message: ref, Quote: "decision"}},
			},
		},
	}
	assert.NoError(t, request.Validate())
	request.Claims[0].Evidence[0].Message.ContentHash = "different"
	assert.ErrorIs(t, request.Validate(), ErrConflict)
}

func TestSummaryGivenInvalidPublishWhenValidatedThenItFailsClosed(t *testing.T) {
	t.Parallel()
	valid := PublishSummaryRequest{
		SummaryID:   "s",
		SubjectType: "topic",
		SubjectID:   "t",
		RequestID:   "r",
		Body:        "text",
		Coverage: []MessageVersionRef{
			{MessageID: "m", CanonicalizationVersion: "v", ContentHash: "h"},
		},
	}
	for _, mutate := range []func(*PublishSummaryRequest){
		func(r *PublishSummaryRequest) { r.SummaryID = "" },
		func(r *PublishSummaryRequest) { r.SubjectType = "session" },
		func(r *PublishSummaryRequest) { r.SubjectID = "" },
		func(r *PublishSummaryRequest) { r.RequestID = "" },
		func(r *PublishSummaryRequest) { r.Body = "" },
		func(r *PublishSummaryRequest) { r.ExpectedRevision = -1 },
		func(r *PublishSummaryRequest) { r.Coverage = nil },
		func(r *PublishSummaryRequest) { r.Coverage = []MessageVersionRef{{MessageID: "m"}} },
		func(r *PublishSummaryRequest) { r.Coverage = append(r.Coverage, r.Coverage[0]) },
		func(r *PublishSummaryRequest) { r.SummarySources = []SummaryRef{{SummaryID: "s", Revision: 0}} },
		func(r *PublishSummaryRequest) { r.Claims = []SummaryClaim{{Text: "missing id"}} },
		func(r *PublishSummaryRequest) { r.Claims = []SummaryClaim{{ID: "a", Text: "x"}, {ID: "a", Text: "y"}} },
		func(r *PublishSummaryRequest) {
			r.Claims = []SummaryClaim{{ID: "a", Text: "x", Evidence: []SummaryEvidence{{Message: r.Coverage[0]}}}}
		},
	} {
		request := valid
		request.Coverage = append([]MessageVersionRef(nil), valid.Coverage...)
		mutate(&request)
		assert.ErrorIs(t, request.Validate(), ErrConflict)
	}
	valid.Coverage = nil
	valid.SummarySources = []SummaryRef{{SummaryID: "child", Revision: 1}}
	assert.NoError(t, valid.Validate())
}
