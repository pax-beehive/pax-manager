package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestClientRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []Config{{URL: "ftp://localhost", Index: "test"}, {URL: "http://user:secret@localhost", Index: "test"}, {URL: "http://localhost?secret=yes", Index: "test"}, {URL: "http://localhost", Index: "../test"}, {URL: "http://localhost", Index: "UPPER"}, {URL: "http://localhost", Index: ""}} {
		_, err := NewClient(tc)
		assert.Error(t, err)
	}
}

func TestEnsureIndexUsesWildcardMappingAndHandlesExistingIndex(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprint(exists), func(t *testing.T) {
			srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPut, r.Method)
				assert.Equal(t, "/test", r.URL.Path)
				assert.Equal(t, "ApiKey test-key", r.Header.Get("Authorization"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				properties := body["mappings"].(map[string]any)["properties"].(map[string]any)
				assert.Equal(t, "wildcard", properties["body"].(map[string]any)["type"])
				assert.Equal(t, "wildcard", properties["title"].(map[string]any)["type"])
				if exists {
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"error":{"type":"resource_already_exists_exception"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"acknowledged":true}`))
			}))
			defer srv.Close()
			client, err := NewClient(
				Config{URL: srv.URL, HTTPClient: srv.Client(), Index: "test", APIKey: "test-key"},
			)
			require.NoError(t, err)
			assert.NoError(t, client.EnsureIndex(context.Background()))
		})
	}
}

func TestIndexUsesExternalVersionsAndKeepsTombstones(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			doc := domain.SearchDocument{
				Kind:        "message",
				SourceID:    "msg/one",
				OwnerUserID: "owner",
				Body:        "secret body",
				Title:       "title",
				Revision:    7,
				Deleted:     deleted,
				CreatedAt:   time.Now().UTC(),
			}
			srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "external", r.URL.Query().Get("version_type"))
				assert.Equal(t, "7", r.URL.Query().Get("version"))
				assert.Contains(t, r.URL.Path, "/_doc/")
				var record map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&record))
				assert.Equal(t, deleted, record["deleted"])
				assert.Equal(t, doc.DocumentID(), record["doc_id"])
				if deleted {
					assert.Empty(t, record["body"])
					assert.Empty(t, record["title"])
				} else {
					assert.Equal(t, doc.Body, record["body"])
				}
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"error":{"type":"version_conflict_engine_exception"}}`))
			}))
			defer srv.Close()
			client, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client(), Index: "test"})
			require.NoError(t, err)
			assert.NoError(t, client.Index(context.Background(), doc))
		})
	}
}

func TestSearchIsLiteralOwnerScopedAndPaginatesWithinPIT(t *testing.T) {
	calls := 0
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/_pit":
			assert.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte(`{"id":"pit-one"}`))
		case "/_pit":
			assert.Equal(t, http.MethodDelete, r.Method)
			_, _ = w.Write([]byte(`{"succeeded":true}`))
		case "/_search":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			encoded, _ := json.Marshal(body)
			assert.Contains(t, string(encoded), `"owner_user_id":"owner"`)
			assert.Contains(t, string(encoded), `"deleted":false`)
			assert.Contains(t, string(encoded), `"value":"*a\\*\\?\\\\b*"`)
			assert.Contains(t, string(encoded), `"case_insensitive":true`)
			assert.Contains(t, string(encoded), `"kind":["message"]`)
			assert.Contains(t, string(encoded), `"session_id":["session"]`)
			calls++
			if calls == 1 {
				assert.NotContains(t, body, "search_after")
				_, _ = w.Write(
					[]byte(
						`{"pit_id":"pit-two","hits":{"hits":[{"_source":{"kind":"message","source_id":"one","owner_user_id":"owner","body":"first"},"sort":[3,12,"message:one",1]},{"_source":{"kind":"message","source_id":"two","owner_user_id":"owner","body":"second"},"sort":[2,11,"message:two",2]}]}}`,
					),
				)
			} else {
				assert.Equal(t, []any{float64(3), float64(12), "message:one", float64(1)}, body["search_after"])
				assert.Equal(t, "pit-two", body["pit"].(map[string]any)["id"])
				_, _ = w.Write([]byte(`{"hits":{"hits":[{"_source":{"kind":"message","source_id":"two","owner_user_id":"owner"},"sort":[2,11,"message:two",2]}]}}`))
			}
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	client, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client(), Index: "test"})
	require.NoError(t, err)
	q := domain.SearchQuery{
		Query:       `a*?\b`,
		OwnerUserID: "owner",
		Kinds:       []string{"message"},
		SessionIDs:  []string{"session"},
		Limit:       1,
	}
	page, err := client.Search(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.Equal(t, "one", page.Hits[0].SourceID)
	require.NotEmpty(t, page.NextCursor)
	q.Cursor = page.NextCursor
	q.OwnerUserID = "another"
	_, err = client.Search(context.Background(), q)
	assert.Error(t, err)
	q.OwnerUserID = "owner"
	page, err = client.Search(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.Equal(t, "two", page.Hits[0].SourceID)
	assert.Empty(t, page.NextCursor)
}

func TestClientRejectsInvalidRequestsAndHidesBackendErrorDetails(t *testing.T) {
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"type":"private_error","reason":"super-secret"}}`))
	}))
	defer srv.Close()
	client, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client(), Index: "test"})
	require.NoError(t, err)
	for _, q := range []domain.SearchQuery{{Query: "x"}, {OwnerUserID: "o"}, {OwnerUserID: "o", Query: "x", Cursor: "bad"}, {OwnerUserID: "o", Query: strings.Repeat("x", 4097)}} {
		_, err = client.Search(context.Background(), q)
		assert.Error(t, err)
	}
	assert.Error(t, client.Index(context.Background(), domain.SearchDocument{}))
	err = client.EnsureIndex(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "super-secret")
	_, err = client.Search(context.Background(), domain.SearchQuery{Query: "x", OwnerUserID: "o"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "super-secret")
}

type testServer struct {
	URL     string
	handler http.Handler
}
type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func newTestServer(handler http.Handler) *testServer {
	return &testServer{URL: "http://search.test", handler: handler}
}
func (s *testServer) Close() {}
func (s *testServer) Client() *http.Client {
	return &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}
}

func TestSearchRejectsInvalidBackendSortAndClosesPIT(t *testing.T) {
	closed := false
	srv := newTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/_pit":
			_, _ = w.Write([]byte(`{"id":"pit"}`))
		case "/_search":
			_, _ = w.Write(
				[]byte(
					`{"hits":{"hits":[{"sort":[12,"message:one",1]},{"sort":[11,"message:two",2]}]}}`,
				),
			)
		case "/_pit":
			closed = true
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	client, err := NewClient(Config{URL: srv.URL, HTTPClient: srv.Client(), Index: "test"})
	require.NoError(t, err)
	page, err := client.Search(
		context.Background(),
		domain.SearchQuery{OwnerUserID: "owner", Query: "port", Limit: 1},
	)
	require.ErrorContains(t, err, "invalid search sort response")
	assert.Empty(t, page.Hits)
	assert.Empty(t, page.NextCursor)
	assert.True(t, closed, "invalid sort must not leave an unusable PIT cursor")
}
