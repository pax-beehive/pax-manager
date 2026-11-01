//go:build integration

package search

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestElasticsearchKeywordsRankAllTermsBeforePartialMatchesAcrossFields(t *testing.T) {
	ctx, client := relevanceTestClient(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		id, title, body, owner string
		minutes                int
	}{
		{"two-old", "port", "tcp", "owner", 1},
		{"two-new", "tcp tcp", "PORT port", "owner", 2},
		{"one-old", "transport", "tcp", "owner", 20},
		{"one-new", "mytcp", "port", "owner", 21},
		{"zero", "transport", "mytcp", "owner", 30},
		{"missing", "port", "udp", "owner", 40},
		{"foreign", "port", "tcp", "other", 50},
	} {
		require.NoError(
			t,
			client.Index(
				ctx,
				domain.SearchDocument{
					Kind:        "message",
					SourceID:    tc.id,
					Title:       tc.title,
					Body:        tc.body,
					OwnerUserID: tc.owner,
					SessionID:   "keywords",
					Revision:    1,
					CreatedAt:   now.Add(time.Duration(tc.minutes) * time.Minute),
				},
			),
		)
	}
	_, err := client.request(ctx, http.MethodPost, "/"+client.index+"/_refresh", nil)
	require.NoError(t, err)
	q := domain.SearchQuery{
		OwnerUserID: "owner",
		Query:       " PORT\u0085tcp PORT ",
		SessionIDs:  []string{"keywords"},
		Limit:       1,
	}
	var got []string
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, searchErr := client.Search(ctx, q)
		require.NoError(t, searchErr)
		for _, doc := range page.Hits {
			got = append(got, doc.SourceID)
		}
		if pageNumber == 0 {
			require.NoError(
				t,
				client.Index(
					ctx,
					domain.SearchDocument{
						Kind:        "message",
						SourceID:    "after-pit",
						OwnerUserID: "owner",
						SessionID:   "keywords",
						Body:        "port tcp",
						Revision:    1,
						CreatedAt:   now.Add(2 * time.Hour),
					},
				),
			)
			_, err = client.request(ctx, http.MethodPost, "/"+client.index+"/_refresh", nil)
			require.NoError(t, err)
		}
		if page.NextCursor == "" {
			break
		}
		q.Query = "tcp port"
		q.Cursor = page.NextCursor
	}
	assert.Equal(t, []string{"two-new", "two-old", "one-new", "one-old", "zero", "missing"}, got)
	for _, tc := range []struct{ id, title, body string }{{"literal", "C++", "a.b"}, {"regex-lookalike", "C++", "axb"}} {
		require.NoError(
			t,
			client.Index(
				ctx,
				domain.SearchDocument{
					Kind:        "message",
					SourceID:    tc.id,
					Title:       tc.title,
					Body:        tc.body,
					OwnerUserID: "owner",
					SessionID:   "punctuation",
					Revision:    1,
					CreatedAt:   now,
				},
			),
		)
	}
	_, err = client.request(ctx, http.MethodPost, "/"+client.index+"/_refresh", nil)
	require.NoError(t, err)
	page, err := client.Search(
		ctx,
		domain.SearchQuery{
			OwnerUserID: "owner",
			Query:       "a.b C++",
			SessionIDs:  []string{"punctuation"},
		},
	)
	require.NoError(t, err)
	require.Len(t, page.Hits, 2)
	assert.Equal(t, "literal", page.Hits[0].SourceID)
	assert.Equal(t, "regex-lookalike", page.Hits[1].SourceID)
	page, err = client.Search(
		ctx,
		domain.SearchQuery{OwnerUserID: "owner", Query: "a.b", SessionIDs: []string{"punctuation"}},
	)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1, "dot remains literal rather than a regexp operator")
}

func TestElasticsearchKeywordsFillAfterAllTermMatchesAndNeverDisplaceThem(t *testing.T) {
	ctx, client := relevanceTestClient(t)
	now := time.Now().UTC()
	for i := 0; i < 22; i++ {
		require.NoError(
			t,
			client.Index(
				ctx,
				domain.SearchDocument{
					Kind:        "message",
					SourceID:    fmt.Sprintf("all-%02d", i),
					OwnerUserID: "owner",
					SessionID:   "many",
					Body:        "port tcp",
					Revision:    1,
					CreatedAt:   now.Add(time.Duration(i) * time.Minute),
				},
			),
		)
	}
	for _, session := range []string{"many", "fallback"} {
		for i, body := range []string{"port only", "tcp only", "unrelated"} {
			require.NoError(
				t,
				client.Index(
					ctx,
					domain.SearchDocument{
						Kind:        "message",
						SourceID:    fmt.Sprintf("%s-partial-%d", session, i),
						OwnerUserID: "owner",
						SessionID:   session,
						Body:        body,
						Revision:    1,
						CreatedAt:   now.Add(time.Duration(100+i) * time.Minute),
					},
				),
			)
		}
	}
	_, err := client.request(ctx, http.MethodPost, "/"+client.index+"/_refresh", nil)
	require.NoError(t, err)
	q := domain.SearchQuery{
		OwnerUserID: "owner",
		Query:       "port tcp",
		SessionIDs:  []string{"many"},
		Limit:       20,
	}
	page, err := client.Search(ctx, q)
	require.NoError(t, err)
	require.Len(t, page.Hits, 20)
	require.NotEmpty(t, page.NextCursor)
	for _, hit := range page.Hits {
		assert.True(t, strings.HasPrefix(hit.SourceID, "all-"), hit.SourceID)
	}
	q.Cursor = page.NextCursor
	page, err = client.Search(ctx, q)
	require.NoError(t, err)
	require.Len(t, page.Hits, 4)
	assert.Equal(
		t,
		[]string{"all-01", "all-00", "many-partial-1", "many-partial-0"},
		[]string{
			page.Hits[0].SourceID,
			page.Hits[1].SourceID,
			page.Hits[2].SourceID,
			page.Hits[3].SourceID,
		},
	)
	assert.Empty(t, page.NextCursor)
	page, err = client.Search(
		ctx,
		domain.SearchQuery{
			OwnerUserID: "owner",
			Query:       "port tcp",
			SessionIDs:  []string{"fallback"},
			Limit:       20,
		},
	)
	require.NoError(t, err)
	require.Len(t, page.Hits, 2)
	assert.Equal(t, "fallback-partial-1", page.Hits[0].SourceID)
}
