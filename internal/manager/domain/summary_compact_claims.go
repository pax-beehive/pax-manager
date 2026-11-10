package domain

import "encoding/json"

type compactSummaryEvidence struct {
	SourceKey int    `json:"source_key"`
	Quote     string `json:"quote"`
	View      string `json:"view,omitempty"`
}

type compactSummaryClaim struct {
	ID         string                   `json:"id"`
	Kind       string                   `json:"kind,omitempty"`
	Text       string                   `json:"text"`
	Basis      string                   `json:"basis,omitempty"`
	State      string                   `json:"state,omitempty"`
	Supersedes []string                 `json:"supersedes,omitempty"`
	Evidence   []compactSummaryEvidence `json:"evidence,omitempty"`
}

// EncodeCompactSummaryClaims references the exact version at its revision-local
// coverage position. The caller persists that coverage in the same transaction.
func EncodeCompactSummaryClaims(
	claims []SummaryClaim,
	coverage []MessageVersionRef,
) ([]byte, error) {
	positions := make(map[string]int, len(coverage))
	for i, ref := range coverage {
		if _, exists := positions[ref.Identity()]; !ref.Valid() || exists {
			return nil, ErrConflict
		}
		positions[ref.Identity()] = i
	}
	var compact []compactSummaryClaim
	for _, claim := range claims {
		item := compactSummaryClaim{ID: claim.ID, Kind: claim.Kind, Text: claim.Text,
			Basis: claim.Basis, State: claim.State, Supersedes: claim.Supersedes}
		for _, evidence := range claim.Evidence {
			position, ok := positions[evidence.Message.Identity()]
			if !ok {
				return nil, ErrConflict
			}
			item.Evidence = append(item.Evidence, compactSummaryEvidence{
				SourceKey: position, Quote: evidence.Quote, View: evidence.View,
			})
		}
		compact = append(compact, item)
	}
	return json.Marshal(compact)
}
