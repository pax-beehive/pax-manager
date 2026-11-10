package domain

import (
	"encoding/json"
	"errors"
)

const SecretaryPublicationBatchMaxBytes = 64 * 1024

var ErrSecretaryBatchPublicationTooLarge = errors.New(
	"secretary batch publication exceeds 64 KiB after source binding",
)

type compactPublicationRequest struct {
	PublishSummaryRequest
	Claims json.RawMessage `json:"claims,omitempty"`
}

// EncodeSecretaryPublication measures persisted requests, excluding transient read proofs.
// Authorization, coverage and outcome validation remain the caller's responsibility.
func EncodeSecretaryPublication(publication SecretaryBatchPublication) ([]byte, error) {
	requests := make([]compactPublicationRequest, 0, len(publication.Outputs))
	for _, output := range publication.Outputs {
		request := publicationBudgetRequest(publication.RequestID, output)
		var claims json.RawMessage
		if len(request.Claims) > 0 {
			var err error
			claims, err = EncodeCompactSummaryClaims(request.Claims, request.Coverage)
			if err != nil {
				return nil, err
			}
		}
		requests = append(requests, compactPublicationRequest{request, claims})
	}
	canonical := struct {
		RequestID string
		Requests  []compactPublicationRequest
		Outcomes  []SecretaryMessageOutcome
	}{publication.RequestID, requests, publication.Outcomes}
	raw, _ := json.Marshal(canonical)
	if len(raw) > SecretaryPublicationBatchMaxBytes {
		return nil, ErrSecretaryBatchPublicationTooLarge
	}
	return raw, nil
}

func publicationBudgetRequest(batchID string, output SecretarySummaryOutput) PublishSummaryRequest {
	request := output.Request
	if request.SummaryID == "" {
		request.SummaryID = SecretaryNodeID(batchID, output.ProposalKey)
	}
	request.RequestID = batchID + ":" + request.SummaryID
	request.Coverage = append([]MessageVersionRef(nil), request.Coverage...)
	for i := range request.Coverage {
		request.Coverage[i].ReadReceipt = ""
	}
	return request
}
