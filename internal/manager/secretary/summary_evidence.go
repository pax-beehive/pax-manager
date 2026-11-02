package secretary

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func bindRollingClaims(
	generated []generatedClaim, job domain.SecretaryJob,
	selected []domain.MessageVersionRef, reads []domain.SecretaryReadMessage,
) ([]domain.SummaryClaim, error) {
	claims := make([]domain.SummaryClaim, 0, len(generated))
	count := 0
	for index, item := range generated {
		if strings.TrimSpace(item.Text) == "" {
			return nil, errors.New("secretary claim text is empty")
		}
		if !knownClaimID(item.ID, job.CurrentSummary) {
			return nil, errors.New("secretary response contains an unknown baseline claim id")
		}
		claim := domain.SummaryClaim{ID: fmt.Sprintf("claim_%d", index+1), Text: item.Text,
			Kind: item.Kind, Basis: item.Basis, State: item.State}
		count += len(item.Evidence)
		if count > 512 {
			return nil, errors.New("secretary response exceeds the evidence limit")
		}
		for _, evidence := range item.Evidence {
			bound, err := bindRollingEvidence(evidence, job, selected, reads)
			if err != nil {
				return nil, err
			}
			claim.Evidence = append(claim.Evidence, bound)
		}
		claims = append(claims, claim)
	}
	return claims, nil
}

func knownClaimID(id string, baseline *domain.SummaryVersion) bool {
	if id == "" {
		return true
	}
	if baseline != nil {
		for _, claim := range baseline.Claims {
			if claim.ID == id {
				return true
			}
		}
	}
	return false
}

func bindRollingEvidence(
	evidence generatedEvidence, job domain.SecretaryJob,
	selected []domain.MessageVersionRef, reads []domain.SecretaryReadMessage,
) (domain.SummaryEvidence, error) {
	// Older prompts exposed stored evidence. Treat echoed metadata as assertions,
	// never as authority for resolving a source or accepting a quote.
	if evidence.Message != nil {
		ref := *evidence.Message
		if !ref.Valid() || evidence.MessageID != "" && evidence.MessageID != ref.MessageID {
			return domain.SummaryEvidence{}, errors.New(
				"secretary evidence reference is invalid or conflicting",
			)
		}
		evidence.MessageID = ref.MessageID
		if evidence.ReadReceipt == "" {
			evidence.ReadReceipt = ref.ReadReceipt
		}
	}
	bound, err := bindTrustedEvidence(evidence, job, selected, reads)
	if err != nil {
		return domain.SummaryEvidence{}, err
	}
	if evidence.Message != nil && *evidence.Message != bound.Message ||
		evidence.View != "" && evidence.View != bound.View ||
		evidence.ReadReceipt != "" && evidence.ReadReceipt != bound.ReadReceipt {
		return domain.SummaryEvidence{}, errors.New(
			"secretary evidence does not match its trusted version or view",
		)
	}
	return bound, nil
}

func bindTrustedEvidence(
	evidence generatedEvidence, job domain.SecretaryJob,
	selected []domain.MessageVersionRef, reads []domain.SecretaryReadMessage,
) (domain.SummaryEvidence, error) {
	if evidence.Quote == "" {
		return domain.SummaryEvidence{}, errors.New("secretary evidence quote is empty")
	}
	for _, message := range job.Messages {
		view := domain.DefaultSearchMessageView(message.Role)
		text, _ := domain.SearchMessageView(message.Body, view)
		if message.SourceID == evidence.MessageID && evidence.ReadReceipt == "" &&
			strings.Contains(text, evidence.Quote) {
			return domain.SummaryEvidence{
				Message: messageReference(message),
				Quote:   evidence.Quote,
				View:    view,
			}, nil
		}
	}
	for _, message := range job.Messages {
		if message.SourceID != evidence.MessageID || evidence.ReadReceipt == "" {
			continue
		}
		ref := messageReference(message)
		ref.ReadReceipt = evidence.ReadReceipt
		if bound, ok := evidenceFromReads(evidence, ref, reads); ok {
			return bound, nil
		}
	}
	for _, ref := range selected {
		if ref.MessageID != evidence.MessageID {
			continue
		}
		if bound, ok := evidenceFromReads(evidence, ref, reads); ok {
			return bound, nil
		}
	}
	if retained, ok := retainedEvidence(evidence, job); ok {
		return retained, nil
	}
	return domain.SummaryEvidence{}, errors.New(
		"secretary evidence was not present in the supplied or verified text",
	)
}

func evidenceFromReads(
	evidence generatedEvidence,
	ref domain.MessageVersionRef,
	reads []domain.SecretaryReadMessage,
) (domain.SummaryEvidence, bool) {
	for _, read := range reads {
		if read.Ref.Identity() == ref.Identity() && read.ReadReceipt == ref.ReadReceipt &&
			(evidence.ReadReceipt == "" || evidence.ReadReceipt == read.ReadReceipt) &&
			(evidence.View == "" || evidence.View == read.View) &&
			strings.Contains(read.Text, evidence.Quote) {
			return domain.SummaryEvidence{Message: ref, Quote: evidence.Quote,
				View: read.View, ReadReceipt: read.ReadReceipt}, true
		}
	}
	return domain.SummaryEvidence{}, false
}

func retainedEvidence(
	evidence generatedEvidence,
	job domain.SecretaryJob,
) (domain.SummaryEvidence, bool) {
	if job.CurrentSummary == nil || job.ResetRequired {
		return domain.SummaryEvidence{}, false
	}
	for _, message := range job.Messages {
		if message.SourceID == evidence.MessageID {
			return domain.SummaryEvidence{}, false
		}
	}
	for _, id := range job.RemovedMessageIDs {
		if id == evidence.MessageID {
			return domain.SummaryEvidence{}, false
		}
	}
	for _, claim := range job.CurrentSummary.Claims {
		for _, old := range claim.Evidence {
			if old.Message.MessageID == evidence.MessageID && old.Quote == evidence.Quote {
				return old, true
			}
		}
	}
	return domain.SummaryEvidence{}, false
}
