package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type summaryListCursor struct {
	Version int    `json:"v"`
	Owner   string `json:"o"`
	After   string `json:"a"`
}

func summaryListQuery(owner string, query domain.SummaryListQuery) (string, int, error) {
	if owner == "" {
		return "", 0, ErrUnauthorized
	}
	if query.Limit < 0 || query.Limit > 100 {
		return "", 0, domain.ErrInvalidCursor
	}
	limit := query.Limit
	if limit == 0 {
		limit = 20
	}
	if query.Cursor == "" {
		return "", limit, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(query.Cursor)
	if err != nil {
		return "", 0, domain.ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor summaryListCursor
	if err = decoder.Decode(&cursor); err != nil {
		return "", 0, domain.ErrInvalidCursor
	}
	if decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 ||
		cursor.Owner != domain.SearchContentHash(
			owner,
		) || strings.TrimSpace(cursor.After) == "" {
		return "", 0, domain.ErrInvalidCursor
	}
	return cursor.After, limit, nil
}

func summaryNextCursor(owner, after string) string {
	raw, _ := json.Marshal(
		summaryListCursor{Version: 1, Owner: domain.SearchContentHash(owner), After: after},
	)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func summaryListItem(summary domain.SummaryVersion) domain.SummaryListItem {
	body := []rune(summary.Body)
	return domain.SummaryListItem{SummaryID: summary.SummaryID, Revision: summary.Revision,
		SubjectType: summary.SubjectType, SubjectID: summary.SubjectID, Title: summary.Title,
		BodyPreview: string(body[:min(len(body), 400)]), ClaimCount: len(summary.Claims),
		CoverageCount: len(
			summary.Coverage,
		), SummarySourceCount: len(summary.SummarySources), CreatedAt: summary.CreatedAt}
}
