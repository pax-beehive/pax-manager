package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const rankingVersion = "keyword-coverage-boundary-v3"

// Stay within Elasticsearch's default regexp length without restricting recall.
const maxRankingRegexpBytes = 1000

type cursor struct {
	Fingerprint string            `json:"fingerprint"`
	PIT         string            `json:"pit"`
	After       []json.RawMessage `json:"after"`
}

func normalizeQuery(q domain.SearchQuery) (domain.SearchQuery, error) {
	terms := domain.SearchTerms(q.Query)
	if strings.TrimSpace(q.OwnerUserID) == "" || strings.TrimSpace(q.Query) == "" ||
		len(q.Query) > 4096 ||
		len(terms) == 0 || len(terms) > domain.MaxSearchTerms ||
		len(q.Cursor) > 16384 ||
		len(q.Kinds) > 10 ||
		len(q.SessionIDs) > 100 {
		return q, fmt.Errorf("%w: owner and bounded nonempty query required", ErrInvalidQuery)
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 100 {
		return q, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidQuery)
	}
	q.Kinds = append([]string(nil), q.Kinds...)
	q.SessionIDs = append([]string(nil), q.SessionIDs...)
	sort.Strings(q.Kinds)
	sort.Strings(q.SessionIDs)
	return q, nil
}
func queryFingerprint(q domain.SearchQuery) string {
	raw, _ := json.Marshal(
		[]any{rankingVersion, q.OwnerUserID, domain.SearchTerms(q.Query), q.Kinds, q.SessionIDs},
	)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func readCursor(q domain.SearchQuery) (cursor, error) {
	var cur cursor
	raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
	if err != nil || json.Unmarshal(raw, &cur) != nil || cur.Fingerprint != queryFingerprint(q) ||
		cur.PIT == "" ||
		!validSearchSort(cur.After, len(domain.SearchTerms(q.Query))) {
		return cur, fmt.Errorf("%w: invalid or expired cursor", ErrInvalidQuery)
	}
	return cur, nil
}

func validSearchSort(values []json.RawMessage, termCount int) bool {
	if len(values) != 4 {
		return false
	}
	for _, value := range values {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	var score float64
	var createdAt, shardDoc int64
	var docID string
	return json.Unmarshal(values[0], &score) == nil && score >= float64(termCount+1) &&
		score <= float64(termCount*(termCount+2)) &&
		score == float64(int(score)) &&
		json.Unmarshal(values[1], &createdAt) == nil &&
		json.Unmarshal(values[2], &docID) == nil &&
		docID != "" &&
		json.Unmarshal(values[3], &shardDoc) == nil &&
		shardDoc >= 0
}

func escapeLiteral(query string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`).Replace(query)
}

func escapeRegexpLiteral(query string) string {
	var result strings.Builder
	for _, char := range query {
		if strings.ContainsRune(`.?+*|{}[]()"\#@&<>~^$-`, char) {
			result.WriteByte('\\')
		}
		result.WriteRune(char)
	}
	return result.String()
}

func fieldAlternatives(operator string, options map[string]any) map[string]any {
	should := make([]any, 0, 2)
	for _, field := range []string{"title", "body"} {
		should = append(should, map[string]any{operator: map[string]any{field: options}})
	}
	return map[string]any{"bool": map[string]any{"should": should, "minimum_should_match": 1}}
}

func searchBody(q domain.SearchQuery, cur cursor) map[string]any {
	filter := []any{
		map[string]any{"term": map[string]any{"owner_user_id": q.OwnerUserID}},
		map[string]any{"term": map[string]any{"deleted": false}},
	}
	if len(q.Kinds) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"kind": q.Kinds}})
	}
	if len(q.SessionIDs) > 0 {
		filter = append(filter, map[string]any{"terms": map[string]any{"session_id": q.SessionIDs}})
	}
	terms := domain.SearchTerms(q.Query)
	literalMatches := make([]any, 0, len(terms))
	var bonuses []any
	for _, term := range terms {
		pattern := "*" + escapeLiteral(term) + "*"
		literalMatches = append(literalMatches, map[string]any{"constant_score": map[string]any{
			"filter": fieldAlternatives(
				"wildcard",
				map[string]any{"value": pattern, "case_insensitive": true},
			),
			"boost": len(terms) + 1,
		}})
		// Each distinct term contributes at most one point across both fields.
		bounded := "(.*[^A-Za-z0-9_])?" + escapeRegexpLiteral(term) + "([^A-Za-z0-9_].*)?"
		if len(bounded) <= maxRankingRegexpBytes {
			bonuses = append(bonuses, map[string]any{"constant_score": map[string]any{
				"filter": fieldAlternatives(
					"regexp",
					map[string]any{"value": bounded, "case_insensitive": true, "flags": "NONE"},
				),
				"boost": 1,
			}})
		}
	}
	query := map[string]any{
		"filter": filter,
		"must": []any{
			map[string]any{
				"bool": map[string]any{"should": literalMatches, "minimum_should_match": 1},
			},
		},
	}
	if len(bonuses) > 0 {
		query["should"] = bonuses
	}
	body := map[string]any{
		"size":             q.Limit + 1,
		"track_total_hits": false,
		"pit":              map[string]any{"id": cur.PIT, "keep_alive": "1m"},
		"sort": []any{
			map[string]any{"_score": "desc"},
			map[string]any{"created_at": "desc"},
			map[string]any{"doc_id": "asc"},
			map[string]any{"_shard_doc": "asc"},
		},
		"query": map[string]any{"bool": query},
	}
	if len(cur.After) > 0 {
		body["search_after"] = cur.After
	}
	return body
}

func (c *Client) openPIT(ctx context.Context) (string, error) {
	data, err := c.request(ctx, http.MethodPost, "/"+c.index+"/_pit?keep_alive=1m", nil)
	if err != nil {
		return "", err
	}
	var result struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &result) != nil || result.ID == "" {
		return "", fmt.Errorf("invalid search point-in-time response")
	}
	return result.ID, nil
}
func (c *Client) closePIT(ctx context.Context, id string) {
	_, _ = c.request(ctx, http.MethodDelete, "/_pit", map[string]string{"id": id})
}

func (c *Client) Search(ctx context.Context, q domain.SearchQuery) (domain.SearchPage, error) {
	var page domain.SearchPage
	q, err := normalizeQuery(q)
	if err != nil {
		return page, err
	}
	cur := cursor{Fingerprint: queryFingerprint(q)}
	if q.Cursor != "" {
		cur, err = readCursor(q)
	} else {
		cur.PIT, err = c.openPIT(ctx)
	}
	if err != nil {
		return page, err
	}
	data, err := c.request(ctx, http.MethodPost, "/_search", searchBody(q, cur))
	if err != nil {
		c.closePIT(ctx, cur.PIT)
		return page, err
	}
	var result struct {
		PIT  string `json:"pit_id"`
		Hits struct {
			Hits []struct {
				Source record            `json:"_source"`
				Sort   []json.RawMessage `json:"sort"`
			} `json:"hits"`
		} `json:"hits"`
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
	}
	if json.Unmarshal(data, &result) != nil || result.TimedOut || result.Shards.Failed > 0 {
		c.closePIT(ctx, cur.PIT)
		return page, fmt.Errorf("incomplete search response")
	}
	if result.PIT != "" {
		cur.PIT = result.PIT
	}
	hits := result.Hits.Hits
	more := len(hits) > q.Limit
	if more {
		hits = hits[:q.Limit]
	}
	page.Hits = make([]domain.SearchDocument, 0, len(hits))
	for _, hit := range hits {
		page.Hits = append(page.Hits, hit.Source.document())
	}
	if more {
		cur.After = hits[len(hits)-1].Sort
		if !validSearchSort(cur.After, len(domain.SearchTerms(q.Query))) {
			c.closePIT(ctx, cur.PIT)
			return domain.SearchPage{}, fmt.Errorf("invalid search sort response")
		}
		raw, _ := json.Marshal(cur)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	} else {
		c.closePIT(ctx, cur.PIT)
	}
	return page, nil
}
