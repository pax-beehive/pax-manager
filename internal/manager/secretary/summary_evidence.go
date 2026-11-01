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

func bindRollingEvidence(
	evidence generatedEvidence, job domain.SecretaryJob,
	selected []domain.MessageVersionRef, reads []domain.SecretaryReadMessage,
) (domain.SummaryEvidence, error) {
	if evidence.Quote == "" {
		return domain.SummaryEvidence{}, errors.New("secretary evidence quote is empty")
	}
	for _, message := range job.Messages {
		if message.SourceID == evidence.MessageID &&
			strings.Contains(message.Body, evidence.Quote) {
			return domain.SummaryEvidence{
				Message: messageReference(message),
				Quote:   evidence.Quote,
				View:    "full",
			}, nil
		}
	}
	for _, ref := range selected {
		if ref.MessageID != evidence.MessageID {
			continue
		}
		for _, read := range reads {
			if read.Ref.Identity() == ref.Identity() && read.ReadReceipt == ref.ReadReceipt &&
				strings.Contains(read.Text, evidence.Quote) {
				return domain.SummaryEvidence{
					Message:     ref,
					Quote:       evidence.Quote,
					View:        read.View,
					ReadReceipt: read.ReadReceipt,
				}, nil
			}
		}
	}
	if retained, ok := retainedEvidence(evidence, job); ok {
		return retained, nil
	}
	return domain.SummaryEvidence{}, errors.New(
		"secretary evidence was not present in the supplied or verified text",
	)
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
