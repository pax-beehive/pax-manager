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

func TestElasticsearchNodeScopeExcludesForeignAndUnknownNodes(t *testing.T) {
	endpoint := os.Getenv("SEARCH_TEST_URL")
	if endpoint == "" {
		t.Skip("SEARCH_TEST_URL is required for the isolated Elasticsearch test")
	}
	client, err := NewClient(
		Config{URL: endpoint, Index: fmt.Sprintf("pax-search-node-%d", time.Now().UnixNano())},
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
	docs := []domain.SearchDocument{
		{
			Kind:        "message",
			SourceID:    "one",
			OwnerUserID: "owner",
			NodeID:      "node-a",
			AgentID:     "agent-a",
			SessionID:   "one",
		},
		{
			Kind:        "message",
			SourceID:    "two",
			OwnerUserID: "owner",
			NodeID:      "node-a",
			AgentID:     "agent-b",
			SessionID:   "two",
		},
		{
			Kind:        "message",
			SourceID:    "three",
			OwnerUserID: "owner",
			NodeID:      "node-b",
			AgentID:     "agent-a",
			SessionID:   "three",
		},
		{Kind: "summary", SourceID: "unknown", OwnerUserID: "owner"},
		{
			Kind:        "message",
			SourceID:    "foreign",
			OwnerUserID: "other",
			NodeID:      "node-a",
			AgentID:     "other",
			SessionID:   "other",
		},
	}
	for i := range docs {
		docs[i].Body, docs[i].Revision, docs[i].CreatedAt = "port data", 1, now
		require.NoError(t, client.Index(t.Context(), docs[i]))
	}
	_, err = client.request(t.Context(), http.MethodPost, "/"+client.index+"/_refresh", nil)
	require.NoError(t, err)
	query := domain.SearchQuery{OwnerUserID: "owner", Query: "port", NodeIDs: []string{"node-a"}}
	page, err := client.Search(t.Context(), query)
	require.NoError(t, err)
	require.Len(
		t,
		page.Hits,
		2,
		"node scope includes multiple agents but no unknown/foreign nodes or owners",
	)
	for _, hit := range page.Hits {
		assert.Equal(t, "node-a", hit.NodeID)
	}
	query.GroupBy = "session"
	groups, err := client.SearchGroups(t.Context(), query)
	require.NoError(t, err)
	assert.Len(t, groups.Groups, 2)
	query.GroupBy, query.GroupKey = "", domain.SearchDocumentGroupKey(docs[0])
	query.NodeIDs = []string{"node-b"}
	page, err = client.Search(t.Context(), query)
	require.NoError(t, err)
	assert.Empty(t, page.Hits, "group expansion must preserve node scope")
	query.GroupKey, query.NodeIDs = "", nil
	page, err = client.Search(t.Context(), query)
	require.NoError(t, err)
	assert.Len(t, page.Hits, 4, "omitted node filter preserves the owner-wide API")
}

func TestElasticsearchNodeProjectionRequiresNewExternalRevision(t *testing.T) {
	endpoint := os.Getenv("SEARCH_TEST_URL")
	if endpoint == "" {
		t.Skip("SEARCH_TEST_URL is required for the isolated Elasticsearch test")
	}
	client, err := NewClient(
		Config{
			URL:   endpoint,
			Index: fmt.Sprintf("pax-search-node-upgrade-%d", time.Now().UnixNano()),
		},
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
	doc := domain.SearchDocument{
		Kind:        "message",
		SourceID:    "old",
		OwnerUserID: "owner",
		Body:        "port data",
		Revision:    7,
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, client.Index(t.Context(), doc))
	doc.NodeID = "node"
	require.NoError(t, client.Index(t.Context(), doc))
	_, err = client.request(t.Context(), http.MethodPost, "/"+client.index+"/_refresh", nil)
	require.NoError(t, err)
	query := domain.SearchQuery{OwnerUserID: "owner", Query: "port", NodeIDs: []string{"node"}}
	page, err := client.Search(t.Context(), query)
	require.NoError(t, err)
	assert.Empty(t, page.Hits, "same-version writes cannot migrate existing documents")
	doc.Revision++
	require.NoError(t, client.Index(t.Context(), doc))
	_, err = client.request(t.Context(), http.MethodPost, "/"+client.index+"/_refresh", nil)
	require.NoError(t, err)
	page, err = client.Search(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.Equal(t, "node", page.Hits[0].NodeID)
}
