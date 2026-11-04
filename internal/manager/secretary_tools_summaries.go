package manager

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretarySummariesRequest struct {
	SessionID string `json:"session_id"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type secretarySummaryToolItem struct {
	domain.SummaryVersion
	RequiresRead bool
}

func (item secretarySummaryToolItem) MarshalJSON() ([]byte, error) {
	if !item.RequiresRead {
		return json.Marshal(item.SummaryVersion)
	}
	return json.Marshal(struct {
		SummaryID    string `json:"summary_id"`
		Revision     int64  `json:"revision"`
		Title        string `json:"title"`
		RequiresRead bool   `json:"requires_read"`
	}{item.SummaryID, item.Revision, item.Title, true})
}

type secretarySummaryToolPage struct {
	Items      []secretarySummaryToolItem `json:"items"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

func (s *Service) secretaryToolSummaries(
	ctx context.Context,
	grant *secretaryToolGrant,
	arguments string,
) (secretarySummaryToolPage, error) {
	var request secretarySummariesRequest
	if err := decodeSecretaryToolJSON(arguments, &request); err != nil {
		return secretarySummaryToolPage{}, err
	}
	if request.SessionID == "" {
		return secretarySummaryToolPage{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "secretary summary session_id is required",
		}
	}
	if request.SessionID != grant.job.SourceSessionID {
		return secretarySummaryToolPage{}, domain.ErrNotFound
	}
	if request.Limit < 0 || request.Limit > 50 {
		return secretarySummaryToolPage{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "invalid secretary summary page limit",
		}
	}
	if request.Limit == 0 {
		request.Limit = 20
	}
	nodes, ok := s.secretaryStore.(domain.SecretaryNodeStore)
	if !ok {
		return secretarySummaryToolPage{}, domain.ErrNotFound
	}
	for {
		page, err := nodes.ListSecretaryNodeSummaries(
			ctx,
			grant.job.OwnerUserID,
			grant.job.SourceID,
			domain.SummaryListQuery{Cursor: request.Cursor, Limit: request.Limit},
		)
		if err != nil {
			return secretarySummaryToolPage{}, err
		}
		projected := projectSecretarySummaryPage(page, false)
		encoded, err := json.Marshal(projected)
		if err != nil {
			return secretarySummaryToolPage{}, err
		}
		if len(encoded) <= 64*1024 {
			return projected, nil
		}
		if request.Limit == 1 {
			projected = projectSecretarySummaryPage(page, true)
			encoded, err = json.Marshal(projected)
			if err == nil && len(encoded) <= 64*1024 {
				return projected, nil
			}
			return secretarySummaryToolPage{}, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "secretary summary header exceeds the read page budget",
			}
		}
		request.Limit = (request.Limit + 1) / 2
	}
}

func projectSecretarySummaryPage(
	page domain.SecretarySummaryPage,
	headersOnly bool,
) secretarySummaryToolPage {
	result := secretarySummaryToolPage{
		Items:      make([]secretarySummaryToolItem, 0, len(page.Items)),
		NextCursor: page.NextCursor,
	}
	for _, summary := range page.Items {
		encoded, _ := json.Marshal(summary)
		item := secretarySummaryToolItem{
			SummaryVersion: summary,
			RequiresRead:   headersOnly || len(encoded) > 64*1024,
		}
		if item.RequiresRead {
			item.SummaryVersion = domain.SummaryVersion{
				SummaryID: summary.SummaryID,
				Revision:  summary.Revision,
				Title:     summary.Title,
			}
		}
		result.Items = append(result.Items, item)
	}
	return result
}
