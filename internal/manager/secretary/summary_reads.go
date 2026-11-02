package secretary

import (
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// RequestedSourceReads returns proof-bearing reads that must be reverified,
// including expansion of the current delta when recovering a completed run.
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
			return nil, errors.New("secretary response exceeds the evidence limit")
		}
		for _, evidence := range claim.Evidence {
			ref, ok, refErr := requestedDeltaRead(job, evidence)
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

func requestedDeltaRead(
	job domain.SecretaryJob,
	evidence generatedEvidence,
) (domain.MessageVersionRef, bool, error) {
	id, receipt := evidence.MessageID, evidence.ReadReceipt
	if evidence.Message != nil {
		if id != "" && id != evidence.Message.MessageID ||
			receipt != "" && evidence.Message.ReadReceipt != "" &&
				receipt != evidence.Message.ReadReceipt {
			return domain.MessageVersionRef{}, false, errors.New(
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
			return domain.MessageVersionRef{}, false, errors.New(
				"secretary evidence has a different source version",
			)
		}
		ref.ReadReceipt = receipt
		return ref, true, nil
	}
	return domain.MessageVersionRef{}, false, nil
}
