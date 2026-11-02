package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestGroupedSearchGivenBackendFailuresThenRejectsPartialResultsAndClosesPIT(t *testing.T) {
	for _, test := range []struct {
		stage, body string
		status      int
	}{
		{"count", `invalid`, 200}, {"count", `{}`, 200}, {"count", `{}`, 503},
		{"pit", `{}`, 200}, {"search", `invalid`, 200}, {"search", `{"timed_out":true}`, 200},
		{"search", `{"_shards":{"failed":1}}`, 200}, {"search", `{}`, 503},
		{"search", `{"hits":{"hits":[{"_source":{"kind":"message","source_id":"m"}}]}}`, 200},
	} {
		t.Run(test.stage+test.body, func(t *testing.T) {
			closed := false
			srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				stage := "search"
				switch r.URL.Path {
				case "/test/_count":
					stage = "count"
				case "/test/_pit":
					stage = "pit"
				case "/_pit":
					closed = true
					_, _ = w.Write([]byte(`{}`))
					return
				}
				if stage == test.stage {
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(test.body))
					return
				}
				switch stage {
				case "count":
					_, _ = w.Write([]byte(`{"count":0}`))
				case "pit":
					_, _ = w.Write([]byte(`{"id":"pit"}`))
				}
			}))
			client, err := NewClient(Config{URL: srv.URL, Index: "test", HTTPClient: srv.Client()})
			require.NoError(t, err)
			_, err = client.SearchGroups(
				t.Context(),
				domain.SearchQuery{OwnerUserID: "owner", Query: "port", GroupBy: "session"},
			)
			require.Error(t, err)
			assert.Equal(t, test.stage == "search", closed)
		})
	}
}

func TestGroupedSearchGivenLastWindowThenReturnsExplicitPaginationLimit(t *testing.T) {
	query := domain.SearchQuery{OwnerUserID: "owner", Query: "port", GroupBy: "session", Limit: 20}
	raw, _ := json.Marshal(
		groupCursor{Fingerprint: groupFingerprint(query), PIT: "pit", Offset: 9999},
	)
	query.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_pit" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.EqualValues(t, 1, body["size"])
		_, _ = w.Write(
			[]byte(
				`{"hits":{"hits":[{"_source":{"group_key":"a","kind":"message","source_id":"m"}}]}}`,
			),
		)
	}))
	client, err := NewClient(Config{URL: srv.URL, Index: "test", HTTPClient: srv.Client()})
	require.NoError(t, err)
	page, err := client.SearchGroups(t.Context(), query)
	require.NoError(t, err)
	assert.True(t, page.PaginationLimited)
	assert.Empty(t, page.NextCursor)
	for _, q := range []domain.SearchQuery{{OwnerUserID: "owner", Query: "port"}, {OwnerUserID: "owner", Query: "port", GroupBy: "session", GroupKey: "both"}} {
		_, err = client.SearchGroups(t.Context(), q)
		assert.ErrorIs(t, err, ErrInvalidQuery)
	}
}

func TestGroupedSearchGivenLegacyDocumentsThenRequiresExplicitMigration(t *testing.T) {
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/test/_count", r.URL.Path)
		_, _ = w.Write([]byte(`{"count":1}`))
	}))
	defer srv.Close()
	client, err := NewClient(Config{URL: srv.URL, Index: "test", HTTPClient: srv.Client()})
	require.NoError(t, err)
	_, err = client.SearchGroups(
		context.Background(),
		domain.SearchQuery{OwnerUserID: "owner", Query: "port", GroupBy: "session"},
	)
	assert.ErrorIs(t, err, ErrGroupMigrationRequired)
}

func TestGroupedSearchGivenMatchesThenCollapsesWithOffsetPITAndBoundedInnerHits(t *testing.T) {
	searches := 0
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/_count":
			_, _ = w.Write([]byte(`{"count":0}`))
		case "/test/_pit":
			_, _ = w.Write([]byte(`{"id":"pit"}`))
		case "/_pit":
			_, _ = w.Write([]byte(`{}`))
		case "/_search":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.NotContains(t, body, "search_after")
			assert.Equal(t, "group_key", body["collapse"].(map[string]any)["field"])
			assert.EqualValues(t, searches, body["from"])
			searches++
			if searches == 1 {
				_, _ = w.Write(
					[]byte(
						`{"pit_id":"pit2","hits":{"hits":[{"_source":{"group_key":"a","kind":"message","source_id":"m","owner_user_id":"owner"},"inner_hits":{"matches":{"hits":{"hits":[{"_source":{"kind":"message","source_id":"m","owner_user_id":"owner","group_key":"a"}}]}}}},{"_source":{"group_key":"b","kind":"summary","source_id":"s","owner_user_id":"owner"}}]}}`,
					),
				)
			} else {
				assert.Equal(t, "pit2", body["pit"].(map[string]any)["id"])
				_, _ = w.Write([]byte(`{"hits":{"hits":[{"_source":{"group_key":"b","kind":"summary","source_id":"s","owner_user_id":"owner"}}]}}`))
			}
		}
	}))
	defer srv.Close()
	client, err := NewClient(Config{URL: srv.URL, Index: "test", HTTPClient: srv.Client()})
	require.NoError(t, err)
	query := domain.SearchQuery{OwnerUserID: "owner", Query: "port", GroupBy: "session", Limit: 1}
	page, err := client.SearchGroups(context.Background(), query)
	require.NoError(t, err)
	require.Len(t, page.Groups, 1)
	assert.Equal(t, "a", page.Groups[0].Key)
	require.NotEmpty(t, page.NextCursor)
	query.Cursor = page.NextCursor
	_, err = client.Search(context.Background(), query)
	assert.Error(t, err)
	query.OwnerUserID = "foreign"
	_, err = client.SearchGroups(context.Background(), query)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	query.OwnerUserID = "owner"
	page, err = client.SearchGroups(context.Background(), query)
	require.NoError(t, err)
	require.Len(t, page.Groups, 1)
	assert.Equal(t, "b", page.Groups[0].Key)
	assert.Empty(t, page.NextCursor)
	assert.False(t, strings.Contains(query.Cursor, "owner"))
}
