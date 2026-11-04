package storage

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretaryNodeStateBytes = 64 * 1024

type secretaryNodePublicationContext struct {
	job         domain.SecretaryJob
	allowed     map[string]domain.MessageVersionRef
	baseline    map[string]domain.SummaryVersion
	invalidated map[string]int64
}

func secretaryNodePublication(
	job domain.SecretaryJob,
	publication domain.SecretaryBatchPublication,
) ([]domain.PublishSummaryRequest, string, error) {
	if publication.RequestID == "" || publication.RequestID != job.BatchID {
		return nil, "", ErrConflict
	}
	binding, err := newSecretaryNodePublicationContext(job)
	if err != nil {
		return nil, "", err
	}
	outputs := make(map[string]domain.PublishSummaryRequest, len(publication.Outputs))
	requests := make([]domain.PublishSummaryRequest, 0, len(publication.Outputs))
	for _, output := range publication.Outputs {
		key, request, bindErr := binding.bind(output)
		if bindErr != nil {
			return nil, "", bindErr
		}
		if _, exists := outputs[key]; exists {
			return nil, "", ErrConflict
		}
		outputs[key] = request
		delete(binding.invalidated, request.SummaryID)
		requests = append(requests, request)
	}
	if len(binding.invalidated) > 0 {
		return nil, "", ErrConflict
	}
	if err = validateSecretaryOutcomes(job, publication.Outcomes, outputs); err != nil {
		return nil, "", err
	}
	if err = secretaryNodeClaimsBudget(requests); err != nil {
		return nil, "", err
	}
	canonical := struct {
		RequestID string
		Requests  []domain.PublishSummaryRequest
		Outcomes  []domain.SecretaryMessageOutcome
	}{publication.RequestID, requests, publication.Outcomes}
	raw, _ := json.Marshal(canonical)
	if len(raw) > secretaryNodeStateBytes {
		return nil, "", fmt.Errorf("summary batch exceeds the 64 KiB budget: %w", ErrConflict)
	}
	return requests, domain.SearchContentHash(string(raw)), nil
}

func newSecretaryNodePublicationContext(
	job domain.SecretaryJob,
) (secretaryNodePublicationContext, error) {
	binding := secretaryNodePublicationContext{
		job:         job,
		allowed:     map[string]domain.MessageVersionRef{},
		baseline:    map[string]domain.SummaryVersion{},
		invalidated: map[string]int64{},
	}
	for _, ref := range job.InvalidatedSummaries {
		binding.invalidated[ref.SummaryID] = ref.Revision
	}
	for _, summary := range job.CurrentSummaries {
		if summary.OwnerUserID != job.OwnerUserID {
			return binding, ErrConflict
		}
		binding.baseline[summary.SummaryID] = summary
		for _, ref := range summary.Coverage {
			binding.allowed[ref.Identity()] = ref
		}
	}
	changed := map[string]bool{}
	for _, message := range job.Messages {
		changed[message.SourceID] = true
	}
	for _, id := range job.RemovedMessageIDs {
		changed[id] = true
	}
	for identity, ref := range binding.allowed {
		if changed[ref.MessageID] {
			delete(binding.allowed, identity)
		}
	}
	for _, message := range job.Messages {
		ref := secretaryReference(message)
		binding.allowed[ref.Identity()] = ref
	}
	for _, ref := range job.AllowedRelatedMessages {
		binding.allowed[ref.Identity()] = ref
	}
	return binding, nil
}

func (binding secretaryNodePublicationContext) bind(
	output domain.SecretarySummaryOutput,
) (string, domain.PublishSummaryRequest, error) {
	request := output.Request
	key := request.SummaryID
	if request.SummaryID == "" {
		key = output.ProposalKey
		if key == "" || request.ExpectedRevision != 0 {
			return key, request, ErrConflict
		}
		request.SummaryID = domain.SecretaryNodeID(binding.job.BatchID, key)
	} else {
		previous, ok := binding.baseline[request.SummaryID]
		invalidatedRevision, invalidated := binding.invalidated[request.SummaryID]
		if (!ok || previous.Revision != request.ExpectedRevision) && (!invalidated || invalidatedRevision != request.ExpectedRevision) {
			return key, request, domain.ErrSummaryVersionMismatch
		}
	}
	request.RequestID = binding.job.BatchID + ":" + request.SummaryID
	request.SubjectType, request.SubjectID = "fragment", secretarySubject(binding.job.SourceID)
	for _, ref := range request.Coverage {
		if _, ok := binding.allowed[ref.Identity()]; !ok {
			return key, request, ErrConflict
		}
	}
	if err := secretaryValidateChildren(binding.job, request.SummarySources); err != nil {
		return key, request, err
	}
	empty := len(request.Coverage)+len(request.SummarySources) == 0
	if empty && !binding.safeEmpty(request) {
		return key, request, ErrConflict
	}
	if err := validateSecretaryExpanded(request, empty); err != nil {
		return key, request, err
	}
	return key, summaryWithoutReceipts(request), nil
}

func (binding secretaryNodePublicationContext) safeEmpty(
	request domain.PublishSummaryRequest,
) bool {
	return binding.invalidated[request.SummaryID] > 0 &&
		request.Body == domain.SecretaryEmptySummaryBody &&
		request.Title == "Unavailable summary" &&
		len(request.Claims) == 0
}

func secretaryNodeClaimsBudget(requests []domain.PublishSummaryRequest) error {
	claims, evidence := 0, 0
	for _, request := range requests {
		claims += len(request.Claims)
		for _, claim := range request.Claims {
			evidence += len(claim.Evidence)
		}
	}
	if claims > 100 || evidence > 512 {
		return ErrConflict
	}
	return nil
}

func validateSecretaryOutcomes(
	job domain.SecretaryJob,
	outcomes []domain.SecretaryMessageOutcome,
	outputs map[string]domain.PublishSummaryRequest,
) error {
	inputs := map[string]bool{}
	removed := map[string]bool{}
	for _, message := range job.Messages {
		inputs[message.SourceID] = true
	}
	for _, id := range job.RemovedMessageIDs {
		inputs[id], removed[id] = true, true
	}
	for _, outcome := range outcomes {
		if !inputs[outcome.MessageID] {
			return ErrConflict
		}
		delete(inputs, outcome.MessageID)
		switch outcome.Action {
		case "no_summary":
			if strings.TrimSpace(outcome.Reason) == "" || len(outcome.SummaryKeys) != 0 {
				return ErrConflict
			}
			for _, request := range outputs {
				for _, ref := range request.Coverage {
					if ref.MessageID == outcome.MessageID {
						return ErrConflict
					}
				}
			}
		case "summarized":
			if len(outcome.SummaryKeys) == 0 {
				return ErrConflict
			}
			seen := map[string]bool{}
			for _, key := range outcome.SummaryKeys {
				request, ok := outputs[key]
				if !ok || seen[key] {
					return ErrConflict
				}
				seen[key] = true
				covered := removed[outcome.MessageID]
				for _, ref := range request.Coverage {
					covered = covered || ref.MessageID == outcome.MessageID
				}
				if !covered {
					return ErrConflict
				}
			}
		default:
			return ErrConflict
		}
	}
	if len(inputs) != 0 {
		return ErrConflict
	}
	return nil
}

func secretarySummaryRefs(versions []domain.SummaryVersion) []domain.SummaryRef {
	refs := make([]domain.SummaryRef, 0, len(versions))
	for _, version := range versions {
		refs = append(
			refs,
			domain.SummaryRef{SummaryID: version.SummaryID, Revision: version.Revision},
		)
	}
	return refs
}

func sortedSecretaryNodeIDs(
	nodes map[string]memoryKnowledgeNode,
	owner, source, after string,
) []string {
	var ids []string
	for id, node := range nodes {
		if node.OwnerUserID == owner && node.Type == "summary" && node.OriginSourceID == source &&
			id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func secretaryChangedSummary(job domain.SecretaryJob, summary domain.SummaryVersion) bool {
	for _, ref := range summary.Coverage {
		for _, id := range job.RemovedMessageIDs {
			if ref.MessageID == id {
				return true
			}
		}
		for _, message := range job.Messages {
			if ref.MessageID == message.SourceID &&
				ref.Identity() != secretaryReference(message).Identity() {
				return true
			}
		}
	}
	return false
}

func secretaryAddInvalidated(job *domain.SecretaryJob, ref domain.SummaryRef) {
	for _, previous := range job.InvalidatedSummaries {
		if previous == ref {
			return
		}
	}
	job.InvalidatedSummaries = append(job.InvalidatedSummaries, ref)
}

func secretaryHideInvalidatedBodies(job *domain.SecretaryJob) {
	invalidated := map[string]bool{}
	for _, ref := range job.InvalidatedSummaries {
		invalidated[ref.SummaryID] = true
	}
	visible := job.CurrentSummaries[:0]
	for _, summary := range job.CurrentSummaries {
		if !invalidated[summary.SummaryID] {
			visible = append(visible, summary)
		}
	}
	job.CurrentSummaries = visible
}

func secretaryInvalidationKey(values []string) string {
	if len(values) == 0 {
		return ""
	}
	sort.Strings(values)
	return domain.SearchContentHash(strings.Join(values, "\x00"))
}
