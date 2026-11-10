package secretary

import (
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// BuildNodePublication binds a whole batch without carrying forward unused sources.
func BuildNodePublication(
	job domain.SecretaryJob,
	output string,
	reads domain.SecretaryToolReads,
) (domain.SecretaryBatchPublication, error) {
	publication := domain.SecretaryBatchPublication{RequestID: job.BatchID}
	if err := validateMessages(job); err != nil {
		return publication, err
	}
	generated, err := decodeGeneratedNodeBatch(output)
	if err != nil {
		return publication, err
	}
	baselines, err := nodeBaselines(job, reads)
	if err != nil {
		return publication, err
	}
	for _, summary := range generated.Summaries {
		bound, bindErr := buildNodeSummary(job, summary, baselines, reads)
		if bindErr != nil {
			return publication, bindErr
		}
		publication.Outputs = append(publication.Outputs, bound)
	}
	if err = bindNodeOutcomes(job, generated, &publication); err != nil {
		return publication, err
	}
	for _, invalidated := range job.InvalidatedSummaries {
		found := false
		for _, item := range publication.Outputs {
			found = found ||
				item.Request.SummaryID == invalidated.SummaryID &&
					item.Request.ExpectedRevision == invalidated.Revision
		}
		if !found {
			return publication, generatedOutputError(
				"evidence",
				"every invalidated summary must be explicitly revised",
			)
		}
	}
	if err = validateNodePublicationBudget(publication); err != nil {
		return publication, err
	}
	return publication, nil
}

func nodeBaselines(
	job domain.SecretaryJob,
	reads domain.SecretaryToolReads,
) (map[string]domain.SummaryVersion, error) {
	baselines := map[string]domain.SummaryVersion{}
	for _, baseline := range append(append([]domain.SummaryVersion(nil), job.CurrentSummaries...), reads.SummaryVersions...) {
		if baseline.OwnerUserID != job.OwnerUserID || baseline.SummaryID == "" ||
			baseline.Revision <= 0 {
			return nil, generatedOutputError("evidence", "summary baseline is unavailable")
		}
		if old, ok := baselines[baseline.SummaryID]; ok && old.Revision != baseline.Revision {
			return nil, domain.ErrSummaryVersionMismatch
		}
		baselines[baseline.SummaryID] = baseline
	}
	return baselines, nil
}

func buildNodeSummary(
	job domain.SecretaryJob,
	summary generatedNodeSummary,
	baselines map[string]domain.SummaryVersion,
	reads domain.SecretaryToolReads,
) (domain.SecretarySummaryOutput, error) {
	item := domain.SecretarySummaryOutput{ProposalKey: summary.ProposalKey}
	request := domain.PublishSummaryRequest{
		SummaryID:   summary.SummaryID,
		SubjectType: "fragment",
		SubjectID:   "secretary:session:" + job.SourceID,
		Title:       strings.TrimSpace(summary.Title),
		Body:        strings.TrimSpace(summary.Body),
	}
	var baseline *domain.SummaryVersion
	if summary.Action == "create" {
		request.SummaryID = ""
	} else {
		current, ok := baselines[summary.SummaryID]
		if !ok {
			for _, invalidated := range job.InvalidatedSummaries {
				if invalidated.SummaryID == summary.SummaryID && invalidated.Revision == *summary.ExpectedRevision {
					current = domain.SummaryVersion{SummaryID: invalidated.SummaryID, Revision: invalidated.Revision, OwnerUserID: job.OwnerUserID}
					ok = true
				}
			}
		}
		if !ok || current.Revision != *summary.ExpectedRevision {
			return item, generatedOutputError("evidence", "summary revision requires its exact read baseline")
		}
		baseline = &current
		request.ExpectedRevision = current.Revision
	}
	key := summary.SummaryID
	if summary.Action == "create" {
		key = summary.ProposalKey
	}
	request.RequestID = job.BatchID + ":" + key
	coverage, err := nodeCoverage(job, summary, baseline, reads)
	if err != nil {
		return item, err
	}
	request.Coverage = coverage
	claimJob := job
	claimJob.CurrentSummary = baseline
	claimJob.ResetRequired = baseline == nil
	request.Claims, err = bindRollingClaims(
		summary.Claims,
		claimJob,
		summary.RelatedSources,
		reads.Messages,
	)
	if err != nil {
		return item, err
	}
	covered := map[string]bool{}
	for _, ref := range coverage {
		covered[ref.Identity()] = true
	}
	for _, claim := range request.Claims {
		for _, evidence := range claim.Evidence {
			if !covered[evidence.Message.Identity()] {
				return item, generatedOutputError(
					"evidence",
					"claim evidence must belong to the selected summary coverage",
				)
			}
		}
	}
	if len(coverage) == 0 {
		if summary.Action == "create" || !invalidatedNode(job, request.SummaryID) {
			return item, generatedOutputError(
				"evidence",
				"summary requires selected source messages",
			)
		}
		if request.Body != domain.SecretaryEmptySummaryBody ||
			request.Title != "Unavailable summary" ||
			len(request.Claims) != 0 {
			return item, generatedOutputError(
				"evidence",
				"empty invalidated summaries must use the fixed neutral title and body",
			)
		}
	}
	item.Request = request
	return item, nil
}

func invalidatedNode(job domain.SecretaryJob, id string) bool {
	for _, ref := range job.InvalidatedSummaries {
		if ref.SummaryID == id {
			return true
		}
	}
	return false
}

func nodeCoverage(
	job domain.SecretaryJob,
	summary generatedNodeSummary,
	baseline *domain.SummaryVersion,
	reads domain.SecretaryToolReads,
) ([]domain.MessageVersionRef, error) {
	available := map[string]domain.MessageVersionRef{}
	if baseline != nil {
		for _, ref := range baseline.Coverage {
			available[ref.MessageID] = ref
		}
	}
	for _, id := range job.RemovedMessageIDs {
		delete(available, id)
	}
	for _, message := range job.Messages {
		available[message.SourceID] = messageReference(message)
	}
	var coverage []domain.MessageVersionRef
	seen := map[string]bool{}
	for _, id := range summary.SourceMessageIDs {
		ref, ok := available[id]
		if !ok {
			return nil, generatedOutputError(
				"evidence",
				"summary selected an unavailable source message",
			)
		}
		coverage = append(coverage, ref)
		seen[ref.Identity()] = true
	}
	for _, ref := range summary.RelatedSources {
		if !hasTrustedRead(ref, reads.Messages) {
			return nil, generatedOutputError(
				"evidence",
				"related source was not exposed by a verified read",
			)
		}
		if !seen[ref.Identity()] {
			coverage = append(coverage, ref)
			seen[ref.Identity()] = true
		}
	}
	return coverage, nil
}

func bindNodeOutcomes(
	job domain.SecretaryJob,
	generated generatedNodeBatch,
	publication *domain.SecretaryBatchPublication,
) error {
	inputs := map[string]bool{}
	removed := map[string]bool{}
	for _, message := range job.Messages {
		inputs[message.SourceID] = true
	}
	for _, id := range job.RemovedMessageIDs {
		inputs[id] = true
		removed[id] = true
	}
	outputs := map[string]domain.PublishSummaryRequest{}
	for index, summary := range generated.Summaries {
		key := summary.SummaryID
		if summary.Action == "create" {
			key = summary.ProposalKey
		}
		outputs[key] = publication.Outputs[index].Request
	}
	seen := map[string]bool{}
	for _, outcome := range generated.Outcomes {
		if !inputs[outcome.MessageID] || seen[outcome.MessageID] {
			return generatedOutputError(
				"schema",
				"each input requires exactly one processing outcome",
			)
		}
		seen[outcome.MessageID] = true
		if err := validateNodeOutcome(outcome, outputs, removed[outcome.MessageID]); err != nil {
			return err
		}
		publication.Outcomes = append(
			publication.Outcomes,
			domain.SecretaryMessageOutcome{
				MessageID:   outcome.MessageID,
				Action:      outcome.Action,
				SummaryKeys: outcome.SummaryKeys,
				Reason:      outcome.Reason,
			},
		)
	}
	if len(seen) != len(inputs) {
		return generatedOutputError(
			"schema",
			"every input message or removal requires a processing outcome",
		)
	}
	return nil
}

func validateNodeOutcome(
	outcome generatedNodeOutcome,
	outputs map[string]domain.PublishSummaryRequest,
	removed bool,
) error {
	switch outcome.Action {
	case "no_summary":
		if len(outcome.SummaryKeys) != 0 || strings.TrimSpace(outcome.Reason) == "" ||
			len(outcome.Reason) > 4096 {
			return generatedOutputError(
				"schema",
				"no_summary requires a reason and cannot reference outputs",
			)
		}
		for _, request := range outputs {
			for _, ref := range request.Coverage {
				if ref.MessageID == outcome.MessageID {
					return generatedOutputError(
						"evidence",
						"no_summary outcome cannot also be used by a published summary",
					)
				}
			}
		}
	case "summarized":
		if len(outcome.SummaryKeys) == 0 {
			return generatedOutputError(
				"schema",
				"summarized outcomes require selected output keys",
			)
		}
		seen := map[string]bool{}
		for _, key := range outcome.SummaryKeys {
			request, ok := outputs[key]
			covered := removed && request.ExpectedRevision > 0
			for _, ref := range request.Coverage {
				covered = covered || ref.MessageID == outcome.MessageID
			}
			if !ok || !covered || seen[key] {
				return generatedOutputError(
					"evidence",
					"processing outcome does not match its summary coverage",
				)
			}
			seen[key] = true
		}
	default:
		return generatedOutputError("schema", "unknown message processing outcome")
	}
	return nil
}

func validateNodePublicationBudget(publication domain.SecretaryBatchPublication) error {
	if _, err := domain.EncodeSecretaryPublication(publication); err != nil {
		return domain.SecretaryOutputValidationError{Code: "schema", Err: err}
	}
	return nil
}
