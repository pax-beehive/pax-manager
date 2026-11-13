package storage

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryBatchID(job domain.SecretaryJob) string {
	refs := make([]string, 0, len(job.Messages))
	for _, message := range job.Messages {
		refs = append(refs, secretaryReference(message).Identity())
	}
	sort.Strings(refs)
	removed := append([]string(nil), job.RemovedMessageIDs...)
	sort.Strings(removed)
	heads := append([]domain.SummaryRef(nil), job.InvalidatedSummaries...)
	for _, summary := range job.CurrentSummaries {
		heads = append(
			heads,
			domain.SummaryRef{SummaryID: summary.SummaryID, Revision: summary.Revision},
		)
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i].SummaryID < heads[j].SummaryID })
	input, _ := json.Marshal(struct {
		Owner, Source, Agent, Policy, Invalidation string
		Settings, Epoch                            int64
		Messages, Removed                          []string
		Heads                                      []domain.SummaryRef
		InputPolicy                                string `json:",omitempty"`
		Instructions                               string `json:",omitempty"`
		Operation                                  string `json:",omitempty"`
		OutputPolicy                               string `json:",omitempty"`
	}{job.OwnerUserID, job.SourceID, job.AgentID, domain.SecretaryNodePolicyVersion, job.InvalidationKey,
		job.SettingsRevision, job.RetryEpoch, refs, removed, heads, job.InputPolicyVersion, secretaryInstructionsHash(job.Instructions), job.OperationID, job.OutputPolicyVersion})
	return "batch_" + strings.TrimPrefix(domain.SearchContentHash(string(input)), "sha256:")[:32]
}

func secretaryBatchInput(job domain.SecretaryJob) []byte {
	job.Attempt, job.AttemptID, job.LeaseToken = nil, "", ""
	raw, _ := json.Marshal(job)
	return raw
}

func secretaryRestoreBatch(raw []byte, current domain.SecretaryJob) (domain.SecretaryJob, error) {
	var job domain.SecretaryJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return job, err
	}
	if !domain.SecretaryOutputPolicySupported(job.OutputPolicyVersion) {
		return job, domain.ErrSecretaryFenced
	}
	job.LeaseToken = current.LeaseToken
	job.ValidationFeedback = current.ValidationFeedback
	job.RetryGeneration = current.RetryGeneration
	return job, nil
}

func secretaryAttemptFailure(failure error, count int) (string, string) {
	if errors.Is(failure, domain.ErrSourceVersionUnavailable) ||
		errors.Is(failure, domain.ErrSummaryVersionMismatch) {
		return "failed", "superseded"
	}
	if errors.Is(failure, domain.ErrSecretaryFenced) {
		return "canceled", "blocked"
	}
	if errors.Is(failure, domain.ErrSecretaryExecutionUnknown) {
		return "unknown", "unknown"
	}
	state := "failed"
	if domain.SecretaryValidationFailureCode(failure) != "" {
		state = "invalid"
	}
	if count >= domain.SecretaryMaxValidationRetries+1 {
		return state, "blocked"
	}
	return state, "pending"
}

func secretaryOutputBytes(output string, reads domain.SecretaryToolReads) ([]byte, error) {
	if len(output) > 1024*1024 {
		return nil, domain.SecretaryOutputValidationError{
			Code: "schema",
			Err:  errors.New("secretary output exceeds limit"),
		}
	}
	raw, err := json.Marshal(reads)
	if err != nil || len(raw) > domain.SecretaryMaxSourceBytes {
		return nil, domain.ErrConflict
	}
	return raw, nil
}
