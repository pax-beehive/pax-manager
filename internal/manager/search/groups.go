package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const groupedResultWindow = 10000

var ErrGroupMigrationRequired = errors.New("grouped search index migration is required")

type groupCursor struct {
	Fingerprint string `json:"fingerprint"`
	PIT         string `json:"pit"`
	Offset      int    `json:"offset"`
}

func groupFingerprint(query domain.SearchQuery) string {
	return "session-groups-v1:" + queryFingerprint(query)
}

func readGroupCursor(query domain.SearchQuery) (groupCursor, error) {
	var cur groupCursor
	raw, err := base64.RawURLEncoding.Strict().DecodeString(query.Cursor)
	if err != nil || json.Unmarshal(raw, &cur) != nil || cur.PIT == "" ||
		cur.Fingerprint != groupFingerprint(
			query,
		) || cur.Offset < 0 || cur.Offset >= groupedResultWindow {
		return cur, ErrInvalidQuery
	}
	return cur, nil
}

func (c *Client) checkGroupMigration(ctx context.Context, owner string) error {
	data, err := c.request(ctx, http.MethodPost, "/"+c.index+"/_count", map[string]any{
		"query": map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"term": map[string]any{"owner_user_id": owner}},
				map[string]any{"term": map[string]any{"deleted": false}}},
			"must_not": []any{map[string]any{"exists": map[string]any{"field": "group_key"}}},
		}},
	})
	if err != nil {
		return err
	}
	var result struct {
		Count *int64 `json:"count"`
	}
	if json.Unmarshal(data, &result) != nil || result.Count == nil {
		return fmt.Errorf("invalid grouped search readiness response")
	}
	if *result.Count > 0 {
		return ErrGroupMigrationRequired
	}
	return nil
}

func groupedSearchBody(query domain.SearchQuery, cur groupCursor) map[string]any {
	body := searchBody(query, cursor{PIT: cur.PIT})
	fields := groupSourceFields()
	body["_source"] = fields
	body["from"] = cur.Offset
	body["size"] = min(query.Limit+1, groupedResultWindow-cur.Offset)
	body["collapse"] = map[string]any{"field": "group_key", "inner_hits": map[string]any{
		"name": "matches", "size": 4,
		"_source": fields,
		"sort": []any{
			map[string]any{"_score": "desc"},
			map[string]any{"created_at": "desc"},
			map[string]any{"doc_id": "asc"},
		},
	}}
	return body
}

func groupSourceFields() []string {
	return []string{
		"group_key",
		"kind",
		"source_id",
		"owner_user_id",
		"node_id",
		"agent_id",
		"session_id",
		"summary_revision",
		"content_hash",
		"canonicalization_version",
	}
}

type groupedHit struct {
	Source record `json:"_source"`
	Inner  map[string]struct {
		Hits struct {
			Hits []struct {
				Source record `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	} `json:"inner_hits"`
}

func (c *Client) SearchGroups(
	ctx context.Context,
	query domain.SearchQuery,
) (page domain.SearchGroupPage, err error) {
	query, err = normalizeQuery(query)
	if err != nil || query.GroupBy != "session" {
		return page, ErrInvalidQuery
	}
	cur := groupCursor{Fingerprint: groupFingerprint(query)}
	if query.Cursor != "" {
		cur, err = readGroupCursor(query)
	} else {
		err = c.checkGroupMigration(ctx, query.OwnerUserID)
		if err == nil {
			cur.PIT, err = c.openPIT(ctx)
		}
	}
	if err != nil {
		return page, err
	}
	defer func() {
		if err != nil {
			c.closePIT(ctx, cur.PIT)
		}
	}()
	data, err := c.request(ctx, http.MethodPost, "/_search", groupedSearchBody(query, cur))
	if err != nil {
		return page, err
	}
	var result struct {
		PIT  string `json:"pit_id"`
		Hits struct {
			Hits []groupedHit `json:"hits"`
		} `json:"hits"`
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
	}
	if json.Unmarshal(data, &result) != nil || result.TimedOut || result.Shards.Failed > 0 {
		return page, fmt.Errorf("incomplete grouped search response")
	}
	if result.PIT != "" {
		cur.PIT = result.PIT
	}
	hits := result.Hits.Hits
	more := len(hits) > query.Limit
	if more {
		hits = hits[:query.Limit]
	}
	page.Groups = make([]domain.SearchGroup, 0, len(hits))
	for _, hit := range hits {
		if hit.Source.GroupKey == "" {
			return domain.SearchGroupPage{}, ErrGroupMigrationRequired
		}
		group := domain.SearchGroup{Key: hit.Source.GroupKey}
		for _, inner := range hit.Inner["matches"].Hits.Hits {
			group.Matches = append(group.Matches, inner.Source.document())
		}
		if len(group.Matches) == 0 {
			group.Matches = append(group.Matches, hit.Source.document())
		}
		group.HasMore = len(group.Matches) > 3
		page.Groups = append(page.Groups, group)
	}
	cur.Offset += len(hits)
	if more && cur.Offset < groupedResultWindow {
		raw, _ := json.Marshal(cur)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	} else {
		page.PaginationLimited = cur.Offset >= groupedResultWindow && len(hits) > 0
		c.closePIT(ctx, cur.PIT)
	}
	return page, nil
}
