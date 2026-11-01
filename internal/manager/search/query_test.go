package search

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSearchRanksOneBoundedLiteralTierBeforeRecency(t *testing.T) {
	q := domain.SearchQuery{OwnerUserID: "owner", Query: "port", Limit: 20}
	body := searchBody(q, cursor{PIT: "pit"})
	assert.Equal(t, []any{
		map[string]any{"_score": "desc"}, map[string]any{"created_at": "desc"},
		map[string]any{"doc_id": "asc"}, map[string]any{"_shard_doc": "asc"},
	}, body["sort"])
	query := body["query"].(map[string]any)["bool"].(map[string]any)
	filters := query["filter"].([]any)
	require.Len(t, filters, 2)
	assert.Equal(t, map[string]any{"term": map[string]any{"owner_user_id": "owner"}}, filters[0])
	required := query["must"].([]any)[0].(map[string]any)["bool"].(map[string]any)
	assert.Equal(t, 1, required["minimum_should_match"])
	literalScore := required["should"].([]any)[0].(map[string]any)["constant_score"].(map[string]any)
	assert.Equal(t, 2, literalScore["boost"])
	literal := literalScore["filter"].(map[string]any)["bool"].(map[string]any)
	assert.Equal(t, 1, literal["minimum_should_match"])
	assert.Len(t, literal["should"], 2)
	bonus := query["should"].([]any)
	require.Len(t, bonus, 1, "both fields must share one binary bonus")
	constant := bonus[0].(map[string]any)["constant_score"].(map[string]any)
	assert.Equal(t, 1, constant["boost"])
	bounded := constant["filter"].(map[string]any)["bool"].(map[string]any)
	assert.Equal(t, 1, bounded["minimum_should_match"])
	for _, clause := range bounded["should"].([]any) {
		for _, value := range clause.(map[string]any)["regexp"].(map[string]any) {
			assert.Equal(t, map[string]any{
				"value": "(.*[^A-Za-z0-9_])?port([^A-Za-z0-9_].*)?",
				"flags": "NONE", "case_insensitive": true,
			}, value)
		}
	}
}

func TestSearchEscapesLuceneOperatorsAndKeepsLongLiteralRecall(t *testing.T) {
	query := `. ?+*|{}[]()"\#@&<>~^$-`
	assert.Equal(t, `\. \?\+\*\|\{\}\[\]\(\)\"\\\#\@\&\<\>\~\^\$\-`, escapeRegexpLiteral(query))
	for _, query := range []string{strings.Repeat("x", 1001), strings.Repeat(`*`, 600)} {
		body := searchBody(domain.SearchQuery{OwnerUserID: "owner", Query: query}, cursor{})
		queryBody := body["query"].(map[string]any)["bool"].(map[string]any)
		assert.NotContains(
			t,
			queryBody,
			"should",
			"optional ranking must not reject a legal long query",
		)
		encoded, err := json.Marshal(queryBody["must"])
		require.NoError(t, err)
		assert.Contains(t, string(encoded), "wildcard")
	}
}

func TestRankedCursorRequiresCurrentFingerprintAndTypedFourValueSort(t *testing.T) {
	q := domain.SearchQuery{OwnerUserID: "owner", Query: "port"}
	encode := func(fingerprint string, sort string) string {
		return base64.RawURLEncoding.EncodeToString(
			[]byte(`{"fingerprint":"` + fingerprint + `","pit":"pit","after":` + sort + `}`),
		)
	}
	for _, sort := range []string{`[3,1700000000000,"message:one",2]`, `[2,-62135596800000,"message:one",0]`} {
		q.Cursor = encode(queryFingerprint(q), sort)
		_, err := readCursor(q)
		require.NoError(t, err)
	}
	for _, sort := range []string{
		`[12,"message:one",1]`, `[3,12,"message:one"]`, `[3,12,"message:one",1,2]`,
		`["1",12,"message:one",1]`, `[null,12,"message:one",1]`, `[4,12,"message:one",1]`,
		`[1,"12","message:one",1]`, `[1,null,"message:one",1]`, `[1,12.5,"message:one",1]`,
		`[3,12,null,1]`, `[3,12,"",1]`, `[3,12,42,1]`,
		`[3,12,"message:one",-1]`, `[3,12,"message:one",null]`, `[3,12,"message:one","1"]`,
	} {
		t.Run(sort, func(t *testing.T) {
			q.Cursor = encode(queryFingerprint(q), sort)
			_, err := readCursor(q)
			assert.ErrorIs(t, err, ErrInvalidQuery)
		})
	}
	legacy, err := json.Marshal([]any{q.OwnerUserID, q.Query, q.Kinds, q.SessionIDs})
	require.NoError(t, err)
	sum := sha256.Sum256(legacy)
	q.Cursor = encode(hex.EncodeToString(sum[:]), `[3,12,"message:one",1]`)
	_, err = readCursor(q)
	assert.ErrorIs(
		t,
		err,
		ErrInvalidQuery,
		"pre-ranking cursors must not change query semantics mid-PIT",
	)
}

func TestRankedCursorCannotChangeOwnerQueryOrFilters(t *testing.T) {
	q := domain.SearchQuery{
		OwnerUserID: "owner",
		Query:       "port",
		Kinds:       []string{"message"},
		SessionIDs:  []string{"session"},
	}
	raw, err := json.Marshal(
		cursor{Fingerprint: queryFingerprint(q), PIT: "pit", After: []json.RawMessage{
			json.RawMessage(
				`1`,
			), json.RawMessage(`12`), json.RawMessage(`"message:one"`), json.RawMessage(`1`),
		}},
	)
	require.NoError(t, err)
	q.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	for _, changed := range []domain.SearchQuery{
		{OwnerUserID: "other", Query: q.Query, Kinds: q.Kinds, SessionIDs: q.SessionIDs},
		{OwnerUserID: q.OwnerUserID, Query: "transport", Kinds: q.Kinds, SessionIDs: q.SessionIDs},
		{OwnerUserID: q.OwnerUserID, Query: q.Query, Kinds: []string{"session"}, SessionIDs: q.SessionIDs},
		{OwnerUserID: q.OwnerUserID, Query: q.Query, Kinds: q.Kinds, SessionIDs: []string{"other"}},
	} {
		changed.Cursor = q.Cursor
		_, err = readCursor(changed)
		assert.ErrorIs(t, err, ErrInvalidQuery)
	}
}

func TestSearchGivenDistinctKeywordsThenScoresCoverageBeforeBoundaryBonuses(t *testing.T) {
	q := domain.SearchQuery{OwnerUserID: "owner", Query: " PORT\tapi port ", Limit: 20}
	query := searchBody(q, cursor{})["query"].(map[string]any)["bool"].(map[string]any)
	require.Len(t, query["filter"].([]any), 2, "owner and deletion filters remain mandatory")
	must, ok := query["must"].([]any)
	require.True(t, ok, "literal OR must be mandatory separately from boundary bonuses")
	require.Len(t, must, 1)
	literal := must[0].(map[string]any)["bool"].(map[string]any)
	assert.Equal(t, 1, literal["minimum_should_match"])
	clauses := literal["should"].([]any)
	require.Len(t, clauses, 2)
	for i, term := range []string{"api", "port"} {
		score := clauses[i].(map[string]any)["constant_score"].(map[string]any)
		assert.Equal(
			t,
			3,
			score["boost"],
			"one extra matched term must outweigh every boundary bonus",
		)
		encoded, err := json.Marshal(score["filter"])
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"value":"*`+term+`*"`)
	}
	require.Len(t, query["should"].([]any), 2, "duplicates cannot inflate boundary score")
	canonical := domain.SearchQuery{OwnerUserID: "owner", Query: "api port", Limit: 20}
	assert.Equal(t, queryFingerprint(canonical), queryFingerprint(q))
	assert.Equal(t, searchBody(canonical, cursor{}), searchBody(q, cursor{}))
	q.Query = strings.TrimSpace(strings.Repeat("duplicate ", 20))
	_, err := normalizeQuery(q)
	require.NoError(t, err)
	q.Query = "a b c d e f g h i j k l m n o p q"
	_, err = normalizeQuery(q)
	assert.ErrorIs(t, err, ErrInvalidQuery)
}

func TestMultiKeywordCursorGivenScoreThenRequiresIntegerWithinDistinctTermCount(t *testing.T) {
	q := domain.SearchQuery{OwnerUserID: "owner", Query: "port api port"}
	for _, tc := range []struct {
		score string
		valid bool
	}{{"3", true}, {"4", true}, {"6", true}, {"8.0", true}, {"0", false}, {"2", false}, {"9", false}, {"-1", false}, {"0.5", false}, {"3.5", false}, {"\"2\"", false}, {"null", false}} {
		raw := `{"fingerprint":"` + queryFingerprint(
			q,
		) + `","pit":"pit","after":[` + tc.score + `,12,"message:one",1]}`
		q.Cursor = base64.RawURLEncoding.EncodeToString([]byte(raw))
		_, err := readCursor(q)
		if tc.valid {
			assert.NoError(t, err, tc.score)
		} else {
			assert.ErrorIs(t, err, ErrInvalidQuery, tc.score)
		}
	}
	old, err := json.Marshal(
		[]any{"ascii-boundary-v1", q.OwnerUserID, q.Query, q.Kinds, q.SessionIDs},
	)
	require.NoError(t, err)
	sum := sha256.Sum256(old)
	q.Cursor = base64.RawURLEncoding.EncodeToString(
		[]byte(
			`{"fingerprint":"` + hex.EncodeToString(
				sum[:],
			) + `","pit":"pit","after":[1,12,"message:one",1]}`,
		),
	)
	_, err = readCursor(q)
	assert.ErrorIs(t, err, ErrInvalidQuery)
}
