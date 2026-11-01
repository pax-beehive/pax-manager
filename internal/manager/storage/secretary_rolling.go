package storage

import (
	"fmt"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretaryCoverageLimit = 4096

func secretaryBoundRemoved(ids []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, min(len(ids), domain.SecretaryMaxMessages))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
		if len(result) == domain.SecretaryMaxMessages {
			break
		}
	}
	sort.Strings(result)
	return result
}

func secretaryFinalizeDelta(work *domain.SecretaryJob) {
	work.RemovedMessageIDs = secretaryBoundRemoved(work.RemovedMessageIDs)
	if work.ResetRequired || len(work.Messages) > 0 || len(work.RemovedMessageIDs) == 0 {
		return
	}
	removed := map[string]bool{}
	for _, id := range work.RemovedMessageIDs {
		removed[id] = true
	}
	if work.CurrentSummary != nil {
		for _, ref := range work.CurrentSummary.Coverage {
			if !removed[ref.MessageID] {
				return
			}
		}
	}
	work.ResetRequired = true
	work.ResetReason = "source_membership_changed"
	work.CurrentSummary = nil
}

func secretarySummaryID(owner, source string) string {
	return domain.SecretarySummaryID(owner, source)
}

func secretaryReference(doc domain.SearchDocument) domain.MessageVersionRef {
	return domain.MessageVersionRef{MessageID: doc.SourceID, ContentHash: doc.ContentHash,
		CanonicalizationVersion: doc.CanonicalizationVersion}
}

func secretaryComposePublication(
	work domain.SecretaryJob, request domain.PublishSummaryRequest,
) (domain.PublishSummaryRequest, error) {
	if request.SummaryID != work.TargetSummaryID || request.RequestID != work.RunID ||
		request.ExpectedRevision != work.ExpectedRevision || request.SubjectType != "fragment" ||
		request.SubjectID != secretarySubject(work.SourceID) {
		return request, ErrConflict
	}
	refs, err := secretaryBaselineCoverage(work)
	if err != nil {
		return request, err
	}
	if err = secretaryBindCoverage(work, request.Coverage, refs); err != nil {
		return request, err
	}
	if err = secretaryValidateChildren(work, request.SummarySources); err != nil {
		return request, err
	}
	request.Coverage = make([]domain.MessageVersionRef, 0, len(refs))
	for _, ref := range refs {
		request.Coverage = append(request.Coverage, ref)
	}
	sort.Slice(request.Coverage, func(i, j int) bool {
		return request.Coverage[i].Identity() < request.Coverage[j].Identity()
	})
	return request, validateSecretaryExpanded(request, work.ResetRequired)
}

func secretaryBaselineCoverage(
	work domain.SecretaryJob,
) (map[string]domain.MessageVersionRef, error) {
	refs := map[string]domain.MessageVersionRef{}
	if !work.ResetRequired && work.CurrentSummary != nil {
		if work.CurrentSummary.SummaryID != work.TargetSummaryID ||
			work.CurrentSummary.Revision != work.ExpectedRevision ||
			work.CurrentSummary.OwnerUserID != work.OwnerUserID {
			return nil, ErrConflict
		}
		for _, ref := range work.CurrentSummary.Coverage {
			refs[ref.Identity()] = ref
		}
	}
	for _, id := range work.RemovedMessageIDs {
		for key, ref := range refs {
			if ref.MessageID == id {
				delete(refs, key)
			}
		}
	}
	for _, doc := range work.Messages {
		for key, ref := range refs {
			if ref.MessageID == doc.SourceID {
				delete(refs, key)
			}
		}
	}
	return refs, nil
}

func secretaryBindCoverage(
	work domain.SecretaryJob,
	coverage []domain.MessageVersionRef,
	refs map[string]domain.MessageVersionRef,
) error {
	allowed := map[string]bool{}
	for _, ref := range refs {
		allowed[ref.Identity()] = true
	}
	for _, doc := range work.Messages {
		ref := secretaryReference(doc)
		refs[ref.Identity()] = ref
		allowed[ref.Identity()] = true
	}
	for _, ref := range work.AllowedRelatedMessages {
		allowed[ref.Identity()] = true
	}
	provided := map[string]bool{}
	for _, ref := range coverage {
		if !ref.Valid() || !allowed[ref.Identity()] || provided[ref.Identity()] {
			return ErrConflict
		}
		provided[ref.Identity()] = true
		refs[ref.Identity()] = ref
	}
	for _, doc := range work.Messages {
		if !provided[secretaryReference(doc).Identity()] {
			return ErrConflict
		}
	}
	return nil
}

func secretaryValidateChildren(work domain.SecretaryJob, sources []domain.SummaryRef) error {
	children := map[domain.SummaryRef]bool{}
	for _, ref := range work.AllowedRelatedSummaries {
		children[ref] = true
	}
	for _, ref := range sources {
		if !children[ref] {
			return ErrConflict
		}
	}
	return nil
}

func validateSecretaryExpanded(request domain.PublishSummaryRequest, reset bool) error {
	if len(request.Coverage)+len(request.SummarySources) > secretaryCoverageLimit {
		return fmt.Errorf("rolling summary exceeds the 4096-source budget: %w", ErrConflict)
	}
	covered := map[string]domain.MessageVersionRef{}
	for _, ref := range request.Coverage {
		if !ref.Valid() {
			return ErrConflict
		}
		covered[ref.Identity()] = ref
	}
	validation := request
	// Public requests retain the 512-source limit. Trusted rolling publications
	// carry existing coverage server-side; validate all evidence and source refs
	// while using only its evidence subset for the public shape validator.
	if len(request.Coverage)+len(request.SummarySources) > 512 {
		validation.Coverage = nil
		seen := map[string]bool{}
		for _, claim := range request.Claims {
			for _, evidence := range claim.Evidence {
				key := evidence.Message.Identity()
				ref, ok := covered[key]
				if !ok {
					return ErrConflict
				}
				if !seen[key] {
					validation.Coverage = append(validation.Coverage, ref)
					seen[key] = true
				}
			}
		}
		if len(validation.Coverage)+len(validation.SummarySources) == 0 {
			validation.Coverage = request.Coverage[:1]
		}
	}
	if reset && len(request.Coverage)+len(request.SummarySources) == 0 && len(request.Claims) == 0 {
		validation.Coverage = []domain.MessageVersionRef{{MessageID: "empty", ContentHash: "empty",
			CanonicalizationVersion: domain.SearchCanonicalizationVersion}}
	}
	return validation.Validate()
}
