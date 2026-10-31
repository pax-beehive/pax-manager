//go:build integration

package search

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestElasticsearchLiteralSearchVersionsAndPagination(t *testing.T) {
	endpoint := os.Getenv("SEARCH_TEST_URL")
	if endpoint == "" {
		t.Skip("SEARCH_TEST_URL is required for the isolated Elasticsearch test")
	}
	ctx := context.Background()
	client, err := NewClient(
		Config{URL: endpoint, Index: fmt.Sprintf("pax-search-test-%d", time.Now().UnixNano())},
	)
	require.NoError(t, err)
	require.NoError(t, client.EnsureIndex(ctx))
	t.Cleanup(func() {
		_, cleanupErr := client.request(ctx, http.MethodDelete, "/"+client.index, nil)
		assert.NoError(t, cleanupErr)
	})
	require.NoError(t, client.EnsureIndex(ctx))
	now := time.Now().UTC()
	docs := []domain.SearchDocument{
		{
			Kind:        "message",
			SourceID:    "one",
			OwnerUserID: "a",
			SessionID:   "session",
			Body:        "\u82f9\u679c\u7b14\u8bb0\u672c \u6570\u636e\u5e93 *?\\literal Database DB",
			Revision:    1,
			CreatedAt:   now,
		},
		{
			Kind:        "message",
			SourceID:    "two",
			OwnerUserID: "a",
			SessionID:   "session",
			Body:        "\u7b14\u8bb0\u672c",
			Revision:    1,
			CreatedAt:   now,
		},
		{
			Kind:        "message",
			SourceID:    "three",
			OwnerUserID: "b",
			SessionID:   "session",
			Body:        "\u7b14\u8bb0\u672c",
			Revision:    1,
			CreatedAt:   now,
		},
		{
			Kind:        "session",
			SourceID:    "title",
			OwnerUserID: "a",
			SessionID:   "other",
			Title:       "\u6570\u636e\u5e93",
			Revision:    1,
			CreatedAt:   now,
		},
	}
	for _, doc := range docs {
		require.NoError(t, client.Index(ctx, doc))
	}
	refresh := func() {
		_, refreshErr := client.request(ctx, http.MethodPost, "/"+client.index+"/_refresh", nil)
		require.NoError(t, refreshErr)
	}
	refresh()
	for _, tc := range []struct {
		query string
		want  int
	}{{"\u6570\u636e", 2}, {"\u672c", 2}, {"\u534e\u4e3a\u7b14\u8bb0\u672c", 0}, {"*?\\", 1}, {"database", 1}, {"DATABASE", 1}, {"db", 1}} {
		page, searchErr := client.Search(ctx, domain.SearchQuery{Query: tc.query, OwnerUserID: "a"})
		require.NoError(t, searchErr)
		assert.Len(t, page.Hits, tc.want)
	}
	query := domain.SearchQuery{
		Query:       "\u672c",
		OwnerUserID: "a",
		Kinds:       []string{"message"},
		SessionIDs:  []string{"session"},
		Limit:       1,
	}
	page, err := client.Search(ctx, query)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	require.NotEmpty(t, page.NextCursor)
	firstID := page.Hits[0].SourceID
	// Mutations after the first page do not change its point-in-time result set.
	docs[1].Deleted = true
	docs[1].Revision = 2
	require.NoError(t, client.Index(ctx, docs[1]))
	refresh()
	query.Cursor = page.NextCursor
	page, err = client.Search(ctx, query)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.NotEqual(t, firstID, page.Hits[0].SourceID)
	assert.Empty(t, page.NextCursor)
	// A delayed old write cannot resurrect the tombstone.
	docs[1].Deleted = false
	docs[1].Revision = 1
	require.NoError(t, client.Index(ctx, docs[1]))
	refresh()
	query.Cursor = ""
	query.Limit = 20
	page, err = client.Search(ctx, query)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.Equal(t, "one", page.Hits[0].SourceID)
}
