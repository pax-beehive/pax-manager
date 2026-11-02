package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type summaryRecentCursor struct {
	Version int       `json:"v"`
	Owner   string    `json:"o"`
	After   string    `json:"a"`
	Time    time.Time `json:"t"`
}

type summaryRecentRef struct {
	domain.SummaryRef
	CreatedAt time.Time
}

func summaryRecentQuery(
	owner string,
	query domain.SummaryListQuery,
) (summaryRecentCursor, int, error) {
	if owner == "" {
		return summaryRecentCursor{}, 0, ErrUnauthorized
	}
	if query.Limit < 0 || query.Limit > 100 {
		return summaryRecentCursor{}, 0, domain.ErrInvalidCursor
	}
	limit := query.Limit
	if limit == 0 {
		limit = 20
	}
	if query.Cursor == "" {
		return summaryRecentCursor{}, limit, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(query.Cursor)
	if err != nil {
		return summaryRecentCursor{}, 0, domain.ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor summaryRecentCursor
	if err = decoder.Decode(&cursor); err != nil || decoder.Decode(new(any)) != io.EOF ||
		cursor.Version != 2 || cursor.Owner != domain.SearchContentHash(owner) || strings.TrimSpace(cursor.After) == "" {
		return summaryRecentCursor{}, 0, domain.ErrInvalidCursor
	}
	return cursor, limit, nil
}

func summaryRecentNextCursor(owner string, ref summaryRecentRef) string {
	raw, _ := json.Marshal(summaryRecentCursor{Version: 2, Owner: domain.SearchContentHash(owner),
		After: ref.SummaryID, Time: ref.CreatedAt})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func summaryRecentAfter(ref summaryRecentRef, cursor summaryRecentCursor) bool {
	return cursor.After == "" || ref.CreatedAt.Before(cursor.Time) ||
		ref.CreatedAt.Equal(cursor.Time) && ref.SummaryID < cursor.After
}
