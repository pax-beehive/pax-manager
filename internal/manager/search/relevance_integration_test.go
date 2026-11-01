//go:build integration

package search

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestElasticsearchRanksASCIIBoundariesThenRecencyWithStablePagination(t *testing.T) {
	ctx, client := relevanceTestClient(t)
	now := time.Now().UTC().Truncate(time.Second)
	cases := []struct {
		id, title, body string
		minutes         int
	}{
		{"plain", "", "port", 1},
		{"both-fields", "port port", "port port port", 2},
		{"newer-single", "", "port", 3},
		{"uppercase", "", "PORT", 4},
		{"colon", "", "port:9879", 5},
		{"parens", "", "(port)", 6},
		{"chinese-prefix", "", "\u4e2d\u6587port", 7},
		{"accent-prefix", "", "\u00e9port", 8},
		{"tie-a", "", "port", 9},
		{"tie-b", "", "port", 9},
		{"underscore", "", "port_name", 20},
		{"digits", "", "port123", 21},
		{"transport", "", "transport", 22},
		{"viewport", "", "viewport", 23},
		{"substring-both", "transport transport", "transport port_name", 24},
	}
	for _, tc := range cases {
		require.NoError(t, client.Index(ctx, domain.SearchDocument{
			Kind: "message", SourceID: tc.id, OwnerUserID: "owner", SessionID: "boundaries",
			Title: tc.title, Body: tc.body, Revision: 1, CreatedAt: now.Add(time.Duration(tc.minutes) * time.Minute),
		}))
	}
	require.NoError(t, client.Index(ctx, domain.SearchDocument{
		Kind: "message", SourceID: "other-owner", OwnerUserID: "other", SessionID: "boundaries",
		Body: "port", Revision: 1, CreatedAt: now.Add(time.Hour),
	}))
	_, err := client.request(ctx, http.MethodPost, "/"+client.index+"/_refresh", nil)
	require.NoError(t, err)
	beforeMapping, err := client.request(ctx, http.MethodGet, "/"+client.index+"/_mapping", nil)
	require.NoError(t, err)
	query := domain.SearchQuery{
		Query:       "port",
		OwnerUserID: "owner",
		SessionIDs:  []string{"boundaries"},
		Limit:       3,
	}
	var got []string
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, searchErr := client.Search(ctx, query)
		require.NoError(t, searchErr)
		for _, hit := range page.Hits {
			got = append(got, hit.SourceID)
		}
		if pageNumber == 0 {
			require.NoError(t, client.Index(ctx, domain.SearchDocument{
				Kind: "message", SourceID: "after-pit", OwnerUserID: "owner", SessionID: "boundaries",
				Body: "port", Revision: 1, CreatedAt: now.Add(2 * time.Hour),
			}))
			_, err = client.request(ctx, http.MethodPost, "/"+client.index+"/_refresh", nil)
			require.NoError(t, err)
		}
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
	}
	assert.Equal(t, []string{
		"tie-a", "tie-b", "accent-prefix", "chinese-prefix", "parens", "colon", "uppercase",
		"newer-single", "both-fields", "plain", "substring-both", "viewport", "transport", "digits", "underscore",
	}, got, "one bounded tier outranks all substrings; recency and ID break ties within each tier")
	afterMapping, err := client.request(ctx, http.MethodGet, "/"+client.index+"/_mapping", nil)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		string(beforeMapping),
		string(afterMapping),
		"query-only ranking must reuse the existing wildcard mapping",
	)
}

func TestElasticsearchRankingKeepsMetacharacterAndLongLiteralRecall(t *testing.T) {
	ctx, client := relevanceTestClient(t)
	var longLiteral strings.Builder
	for char := rune(0x1f300); char < 0x1f400; char++ {
		longLiteral.WriteRune(char)
	}
	for i, query := range []string{`. ?+*|{}[]()"\#@&<>~^$-`, longLiteral.String()} {
		sessionID := fmt.Sprintf("literal-%d", i)
		for j, body := range []string{"before " + query + " after", "unrelated content"} {
			require.NoError(t, client.Index(ctx, domain.SearchDocument{
				Kind: "message", SourceID: fmt.Sprintf("%s-%d", sessionID, j), OwnerUserID: "owner", SessionID: sessionID,
				Body: body, Revision: 1, CreatedAt: time.Now().UTC(),
			}))
		}
		_, err := client.request(ctx, http.MethodPost, "/"+client.index+"/_refresh", nil)
		require.NoError(t, err)
		page, err := client.Search(
			ctx,
			domain.SearchQuery{Query: query, OwnerUserID: "owner", SessionIDs: []string{sessionID}},
		)
		require.NoError(t, err)
		require.Len(t, page.Hits, 1)
		assert.Equal(t, sessionID+"-0", page.Hits[0].SourceID)
	}
}

func relevanceTestClient(t *testing.T) (context.Context, *Client) {
	t.Helper()
	endpoint := os.Getenv("SEARCH_TEST_URL")
	if endpoint == "" {
		t.Skip("SEARCH_TEST_URL is required for the isolated Elasticsearch test")
	}
	ctx := context.Background()
	client, err := NewClient(
		Config{
			URL:   endpoint,
			Index: fmt.Sprintf("pax-search-relevance-test-%d", time.Now().UnixNano()),
		},
	)
	require.NoError(t, err)
	require.NoError(t, client.EnsureIndex(ctx))
	t.Cleanup(func() {
		_, cleanupErr := client.request(ctx, http.MethodDelete, "/"+client.index, nil)
		assert.NoError(t, cleanupErr)
	})
	return ctx, client
}
