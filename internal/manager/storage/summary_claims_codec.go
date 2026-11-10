package storage

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type storedSummaryEvidence struct {
	SourceKey   json.RawMessage `json:"source_key,omitempty"`
	Message     json.RawMessage `json:"message,omitempty"`
	Quote       string          `json:"quote"`
	View        string          `json:"view,omitempty"`
	ReadReceipt string          `json:"read_receipt,omitempty"`
}

type storedSummaryClaim struct {
	domain.SummaryClaim
	Evidence []storedSummaryEvidence `json:"evidence,omitempty"`
}

func decodeSummaryClaims(
	raw []byte,
	coverage []domain.MessageVersionRef,
) ([]domain.SummaryClaim, error) {
	var stored []storedSummaryClaim
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&stored) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrConflict
	}
	var result []domain.SummaryClaim
	if stored != nil {
		result = make([]domain.SummaryClaim, 0, len(stored))
	}
	for _, item := range stored {
		claim := item.SummaryClaim
		for _, encoded := range item.Evidence {
			evidence, err := decodeSummaryEvidence(encoded, coverage)
			if err != nil {
				return nil, err
			}
			claim.Evidence = append(claim.Evidence, evidence)
		}
		result = append(result, claim)
	}
	return result, nil
}

func decodeSummaryEvidence(
	encoded storedSummaryEvidence,
	coverage []domain.MessageVersionRef,
) (domain.SummaryEvidence, error) {
	var ref domain.MessageVersionRef
	if len(encoded.SourceKey) > 0 {
		var position *int
		if len(encoded.Message) > 0 || json.Unmarshal(encoded.SourceKey, &position) != nil ||
			position == nil || *position < 0 || *position >= len(coverage) {
			return domain.SummaryEvidence{}, ErrConflict
		}
		ref = coverage[*position]
	} else {
		if len(encoded.Message) == 0 || json.Unmarshal(encoded.Message, &ref) != nil {
			return domain.SummaryEvidence{}, ErrConflict
		}
		found := false
		for _, source := range coverage {
			found = found || source.Identity() == ref.Identity()
		}
		if !found {
			return domain.SummaryEvidence{}, ErrConflict
		}
	}
	if !ref.Valid() || encoded.Quote == "" {
		return domain.SummaryEvidence{}, ErrConflict
	}
	ref.ReadReceipt = ""
	return domain.SummaryEvidence{Message: ref, Quote: encoded.Quote, View: encoded.View}, nil
}
