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

func TestGroupedSearchElasticsearchGivenMultipleKindsThenPagesUniqueSessionsAndScopesExpansion(
	t *testing.T,
) {
	endpoint := os.Getenv("SEARCH_TEST_URL")
	if endpoint == "" {
		t.Skip("SEARCH_TEST_URL is required")
	}
	client, err := NewClient(
		Config{URL: endpoint, Index: fmt.Sprintf("pax-grouped-test-%d", time.Now().UnixNano())},
	)
	require.NoError(t, err)
	require.NoError(t, client.EnsureIndex(t.Context()))
	t.Cleanup(func() {
		_, cleanupErr := client.request(
			context.Background(),
			http.MethodDelete,
			"/"+client.index,
			nil,
		)
		assert.NoError(t, cleanupErr)
	})
	now := time.Now().UTC()
	for i := range 22 {
		for _, kind := range []string{"message", "summary", "session"} {
			doc := domain.SearchDocument{
				Kind:        kind,
				SourceID:    fmt.Sprintf("%s-%d", kind, i),
				OwnerUserID: "owner",
				AgentID:     "agent",
				SessionID:   fmt.Sprintf("session-%d", i),
				Body:        "port",
				Revision:    1,
				CreatedAt:   now.Add(-time.Duration(i) * time.Minute),
			}
			require.NoError(t, client.Index(t.Context(), doc))
		}
	}
	foreign := domain.SearchDocument{
		Kind:        "message",
		SourceID:    "foreign",
		OwnerUserID: "foreign",
		AgentID:     "agent",
		SessionID:   "session-0",
		Body:        "port",
		Revision:    1,
		CreatedAt:   now.Add(time.Hour),
	}
	require.NoError(t, client.Index(t.Context(), foreign))
	_, err = client.request(t.Context(), http.MethodPost, "/"+client.index+"/_refresh", nil)
	require.NoError(t, err)
	query := domain.SearchQuery{OwnerUserID: "owner", Query: "port", GroupBy: "session"}
	first, err := client.SearchGroups(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, first.Groups, 20)
	require.NotEmpty(t, first.NextCursor)
	seen := map[string]bool{}
	for _, group := range first.Groups {
		assert.False(t, seen[group.Key])
		seen[group.Key] = true
		assert.Len(t, group.Matches, 3)
		for _, hit := range group.Matches {
			assert.Empty(t, hit.Body, "grouped transport must exclude source bodies")
		}
	}
	query.Cursor = first.NextCursor
	last, err := client.SearchGroups(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, last.Groups, 2)
	assert.Empty(t, last.NextCursor)
	for _, group := range last.Groups {
		assert.False(t, seen[group.Key])
	}
	expanded, err := client.Search(
		t.Context(),
		domain.SearchQuery{OwnerUserID: "owner", Query: "port", GroupKey: first.Groups[0].Key},
	)
	require.NoError(t, err)
	require.Len(t, expanded.Hits, 3)
	for _, hit := range expanded.Hits {
		assert.Equal(t, first.Groups[0].Key, hit.GroupKey)
	}
	_, err = client.request(
		t.Context(),
		http.MethodPut,
		"/"+client.index+"/_doc/legacy?refresh=true",
		map[string]any{
			"doc_id":          "message:legacy",
			"kind":            "message",
			"source_id":       "legacy",
			"owner_user_id":   "owner",
			"body":            "port",
			"deleted":         false,
			"source_revision": 1,
		},
	)
	require.NoError(t, err)
	query.Cursor = ""
	_, err = client.SearchGroups(t.Context(), query)
	assert.ErrorIs(t, err, ErrGroupMigrationRequired)
	foreignPage, err := client.SearchGroups(
		t.Context(),
		domain.SearchQuery{OwnerUserID: "foreign", Query: "port", GroupBy: "session"},
	)
	require.NoError(t, err)
	assert.Len(t, foreignPage.Groups, 1)
}
