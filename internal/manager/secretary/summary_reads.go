package secretary

import (
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// RequestedSourceReads returns proof-bearing reads that must be reverified,
// including independent pages or views of selected sources on completed runs.
func RequestedSourceReads(
	job domain.SecretaryJob,
	output string,
) ([]domain.MessageVersionRef, error) {
	generated, err := decodeGeneratedSummary(output)
	if err != nil {
		return nil, err
	}
	refs := append([]domain.MessageVersionRef(nil), generated.RelatedSources...)
	seen := map[string]bool{}
	for _, ref := range refs {
		seen[ref.Identity()+"\x00"+ref.ReadReceipt] = true
	}
	count := 0
	for _, claim := range generated.Claims {
		count += len(claim.Evidence)
		if count > 512 {
			return nil, generatedOutputError(
				"schema",
				"secretary response exceeds the evidence limit",
			)
		}
		for _, evidence := range claim.Evidence {
			ref, ok, refErr := requestedDeltaRead(job, evidence)
			if refErr == nil && !ok {
				ref, ok, refErr = requestedRelatedRead(generated.RelatedSources, evidence)
			}
			if refErr != nil {
				return nil, refErr
			}
			key := ref.Identity() + "\x00" + ref.ReadReceipt
			if ok && !seen[key] {
				refs = append(refs, ref)
				seen[key] = true
			}
		}
	}
	return refs, nil
}

func requestedRelatedRead(
	selected []domain.MessageVersionRef,
	evidence generatedEvidence,
) (domain.MessageVersionRef, bool, error) {
	id, receipt := evidence.MessageID, evidence.ReadReceipt
	if evidence.Message != nil {
		id = evidence.Message.MessageID
		if receipt == "" {
			receipt = evidence.Message.ReadReceipt
		}
	}
	if receipt == "" {
		return domain.MessageVersionRef{}, false, nil
	}
	var candidate domain.MessageVersionRef
	found, ambiguous, matchedID := false, false, false
	for _, ref := range selected {
		if ref.MessageID != id {
			continue
		}
		matchedID = true
		if evidence.Message != nil && evidence.Message.Identity() != ref.Identity() {
			continue
		}
		if ref.ReadReceipt == receipt {
			return ref, true, nil
		}
		ambiguous = ambiguous || found && candidate.Identity() != ref.Identity()
		candidate, found = ref, true
	}
	if ambiguous || matchedID && !found {
		return domain.MessageVersionRef{}, false, generatedOutputError("evidence",
			"secretary evidence has an ambiguous or different source version",
		)
	}
	if found {
		candidate.ReadReceipt = receipt
	}
	return candidate, found, nil
}

func requestedDeltaRead(
	job domain.SecretaryJob,
	evidence generatedEvidence,
) (domain.MessageVersionRef, bool, error) {
	id, receipt := evidence.MessageID, evidence.ReadReceipt
	if evidence.Message != nil {
		if id != "" && id != evidence.Message.MessageID ||
			receipt != "" && evidence.Message.ReadReceipt != "" &&
				receipt != evidence.Message.ReadReceipt {
			return domain.MessageVersionRef{}, false, generatedOutputError("evidence",
				"secretary evidence reference is conflicting",
			)
		}
		id = evidence.Message.MessageID
		if receipt == "" {
			receipt = evidence.Message.ReadReceipt
		}
	}
	if receipt == "" {
		return domain.MessageVersionRef{}, false, nil
	}
	for _, message := range job.Messages {
		if message.SourceID != id {
			continue
		}
		ref := messageReference(message)
		if evidence.Message != nil && evidence.Message.Identity() != ref.Identity() {
			return domain.MessageVersionRef{}, false, generatedOutputError("evidence",
				"secretary evidence has a different source version",
			)
		}
		ref.ReadReceipt = receipt
		return ref, true, nil
	}
	return domain.MessageVersionRef{}, false, nil
}
