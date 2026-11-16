package search

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeSearchScopeNormalizesAndBindsEveryQueryAndCursor(t *testing.T) {
	query := domain.SearchQuery{OwnerUserID: "owner", Query: "port", NodeIDs: []string{"z", "a"}}
	normalized, err := normalizeQuery(query)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "z"}, normalized.NodeIDs)
	assert.Equal(t, []string{"z", "a"}, query.NodeIDs)
	tooMany := query
	tooMany.NodeIDs = make([]string, 101)
	_, err = normalizeQuery(tooMany)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	for _, mode := range []string{"flat", "grouped", "expansion"} {
		t.Run(mode, func(t *testing.T) {
			current := normalized
			var body map[string]any
			if mode == "grouped" {
				current.GroupBy = "session"
				body = groupedSearchBody(current, groupCursor{PIT: "pit"})
			} else {
				if mode == "expansion" {
					current.GroupKey = "selected-group"
				}
				body = searchBody(current, cursor{PIT: "pit"})
			}
			filters := body["query"].(map[string]any)["bool"].(map[string]any)["filter"]
			assert.Contains(
				t,
				filters,
				map[string]any{"term": map[string]any{"owner_user_id": "owner"}},
			)
			assert.Contains(
				t,
				filters,
				map[string]any{"terms": map[string]any{"node_id": []string{"a", "z"}}},
			)
		})
	}
	flat, err := json.Marshal(cursor{Fingerprint: queryFingerprint(normalized), PIT: "pit",
		After: []json.RawMessage{
			json.RawMessage(`2`),
			json.RawMessage(`12`),
			json.RawMessage(`"message:m"`),
			json.RawMessage(`1`),
		}})
	require.NoError(t, err)
	grouped, err := json.Marshal(
		groupCursor{Fingerprint: groupFingerprint(normalized), PIT: "pit", Offset: 1},
	)
	require.NoError(t, err)
	for _, nodes := range [][]string{{"other"}, {"a"}, nil} {
		changed := normalized
		changed.NodeIDs = nodes
		changed.Cursor = base64.RawURLEncoding.EncodeToString(flat)
		_, err = readCursor(changed)
		assert.ErrorIs(t, err, ErrInvalidQuery)
		changed.Cursor = base64.RawURLEncoding.EncodeToString(grouped)
		_, err = readGroupCursor(changed)
		assert.ErrorIs(t, err, ErrInvalidQuery)
	}
}

func TestNodeSearchProjectionUpgradesExistingMappingAndRoundTripsNode(t *testing.T) {
	mappingUpdated := false
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if r.URL.Path == "/test/_mapping" {
			mappingUpdated = true
			assert.Equal(
				t,
				map[string]any{"type": "keyword"},
				body["properties"].(map[string]any)["node_id"],
			)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		assert.Equal(
			t,
			map[string]any{"type": "keyword"},
			body["mappings"].(map[string]any)["properties"].(map[string]any)["node_id"],
		)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"resource_already_exists_exception"}}`))
	}))
	client, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client(), Index: "test"})
	require.NoError(t, err)
	require.NoError(t, client.EnsureIndex(t.Context()))
	assert.True(t, mappingUpdated)
	doc := domain.SearchDocument{
		Kind:        "message",
		SourceID:    "message",
		OwnerUserID: "owner",
		NodeID:      "node",
	}
	assert.Equal(t, "node", toRecord(doc).document().NodeID)
	assert.Contains(t, groupSourceFields(), "node_id")
	doc.Deleted = true
	assert.Empty(t, toRecord(doc).NodeID)
}
