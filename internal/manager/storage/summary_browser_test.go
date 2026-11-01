package storage

import (
	"database/sql/driver"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryBrowserGivenCurrentVersionsWhenPagingThenUsesOwnerBoundStableCursor(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	store.mu.Lock()
	state := store.secretaryLocked()
	for _, id := range []string{"c", "a", "b"} {
		state.summaries[id] = []domain.SummaryVersion{
			{SummaryID: id, OwnerUserID: "owner", Revision: 1,
				Body: "old"},
			{SummaryID: id, OwnerUserID: "owner", Revision: 2,
				Title: "Current " + id, Body: strings.Repeat("x", 450), Coverage: []domain.MessageVersionRef{},
				Claims: []domain.SummaryClaim{{ID: "claim", Text: "fact"}}, CreatedAt: time.Now()},
		}
	}
	state.summaries["foreign"] = []domain.SummaryVersion{
		{SummaryID: "foreign", OwnerUserID: "other", Revision: 1},
	}
	store.mu.Unlock()
	first, err := store.ListCurrentSummaries(
		t.Context(),
		"owner",
		domain.SummaryListQuery{Limit: 2},
	)
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	assert.Equal(t, "a", first.Items[0].SummaryID)
	assert.EqualValues(t, 2, first.Items[0].Revision)
	assert.Len(t, first.Items[0].BodyPreview, 400)
	assert.Equal(t, 1, first.Items[0].ClaimCount)
	require.NotEmpty(t, first.NextCursor)
	second, err := store.ListCurrentSummaries(
		t.Context(),
		"owner",
		domain.SummaryListQuery{Limit: 2, Cursor: first.NextCursor},
	)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	assert.Equal(t, "c", second.Items[0].SummaryID)
	assert.Empty(t, second.NextCursor)
	_, err = store.ListCurrentSummaries(
		t.Context(),
		"other",
		domain.SummaryListQuery{Cursor: first.NextCursor},
	)
	assert.ErrorIs(t, err, domain.ErrInvalidCursor)
}

func TestSummaryBrowserGivenHiddenSourcesWhenListingThenOmitsHeadersAndKeepsPaging(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	store.mu.Lock()
	state := store.secretaryLocked()
	state.summaries["a"] = []domain.SummaryVersion{
		{SummaryID: "a", OwnerUserID: "owner", Revision: 1,
			Body: "must not leak", Coverage: []domain.MessageVersionRef{{MessageID: "deleted"}}},
	}
	state.summaries["b"] = []domain.SummaryVersion{
		{SummaryID: "b", OwnerUserID: "owner", Revision: 1, Body: "visible"},
	}
	store.mu.Unlock()
	page, err := store.ListCurrentSummaries(t.Context(), "owner", domain.SummaryListQuery{Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	require.NotEmpty(t, page.NextCursor)
	page, err = store.ListCurrentSummaries(
		t.Context(),
		"owner",
		domain.SummaryListQuery{Cursor: page.NextCursor, Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "b", page.Items[0].SummaryID)
	assert.Empty(t, page.NextCursor)
}

func TestSummaryBrowserGivenLongPersistedIDsThenGeneratedCursorsRemainUsable(t *testing.T) {
	for _, length := range []int{513, 5000} {
		store, _ := secretaryMemoryFixture(t)
		id := strings.Repeat("a", length)
		store.mu.Lock()
		for _, current := range []string{id, "z"} {
			store.secretaryLocked().summaries[current] = []domain.SummaryVersion{{
				SummaryID: current, Revision: 1, OwnerUserID: "owner", Body: "persisted summary",
			}}
		}
		store.mu.Unlock()
		first, err := store.ListCurrentSummaries(
			t.Context(),
			"owner",
			domain.SummaryListQuery{Limit: 1},
		)
		require.NoError(t, err)
		require.Len(t, first.Items, 1)
		assert.Equal(t, id, first.Items[0].SummaryID)
		require.NotEmpty(t, first.NextCursor)
		if length > 2048 {
			assert.Greater(t, len(first.NextCursor), 2048)
		}
		last, err := store.ListCurrentSummaries(
			t.Context(),
			"owner",
			domain.SummaryListQuery{Limit: 1, Cursor: first.NextCursor},
		)
		require.NoError(t, err)
		require.Len(t, last.Items, 1)
		assert.Equal(t, "z", last.Items[0].SummaryID)
		assert.Empty(t, last.NextCursor)
	}
}

func TestSummaryBrowserGivenInvalidQueryThenRejectsBeforeReading(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	for _, query := range []domain.SummaryListQuery{{Limit: -1}, {Limit: 101}, {Cursor: "bad"},
		{Cursor: strings.Repeat("a", 2049)}, {Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"o":"owner","a":"a","extra":true}`))}} {
		_, err := store.ListCurrentSummaries(t.Context(), "owner", query)
		assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	}
	_, err := store.ListCurrentSummaries(t.Context(), "", domain.SummaryListQuery{})
	assert.ErrorIs(t, err, ErrUnauthorized)
	page, err := store.ListCurrentSummaries(t.Context(), "owner", domain.SummaryListQuery{})
	require.NoError(t, err)
	assert.NotNil(t, page.Items)
	assert.Empty(t, page.Items)
}

func TestSummaryBrowserPostgresGivenCurrentCandidatesThenReusesAccessChecksAndExactHead(
	t *testing.T,
) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{
			columns: []string{"summary_id", "revision"},
			values:  [][]driver.Value{{"a", int64(2)}, {"b", int64(1)}},
		},
		summaryVersionRows("a", "owner_1", 2), summarySourceRows(),
		summaryAccessRows(
			"owner_1",
			"message_1",
		), summaryMessageRows("message_1", "current body", "owner_1"),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	page, err := store.ListCurrentSummaries(
		t.Context(),
		"owner_1",
		domain.SummaryListQuery{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "a", page.Items[0].SummaryID)
	assert.EqualValues(t, 2, page.Items[0].Revision)
	assert.Equal(t, 1, page.Items[0].CoverageCount)
	assert.NotEmpty(t, page.NextCursor)
	assert.Contains(t, script.queryTexts[0], "MAX(revision)")
	assert.Contains(t, script.queryTexts[0], "owner_user_id = $1")
	assert.Contains(t, script.queryTexts[0], "summary_id > $2")
	assert.Equal(t, "owner_1", script.queryArgs[0][0].Value)
	assert.EqualValues(t, 2, script.queryArgs[0][2].Value)
	assert.EqualValues(t, 2, script.queryArgs[1][2].Value)
	assert.True(t, script.committed)
}

func TestSummaryBrowserPostgresGivenInaccessibleCandidateThenNeverReturnsItsHeader(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		{
			columns: []string{"summary_id", "revision"},
			values:  [][]driver.Value{{"a", int64(1)}, {"b", int64(1)}},
		},
		summaryVersionRows(
			"a",
			"owner_1",
			1,
		), summarySourceRows(), summaryAccessRows("other", "message_1"),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	page, err := store.ListCurrentSummaries(
		t.Context(),
		"owner_1",
		domain.SummaryListQuery{Limit: 1},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.NotEmpty(t, page.NextCursor)
	assert.True(t, script.committed)
}

func TestSummaryBrowserPostgresGivenStorageFailureThenFailsClosed(t *testing.T) {
	for _, queries := range [][]scriptedRows{
		nil,
		{{columns: []string{"summary_id", "revision"}, values: [][]driver.Value{{"a", "invalid"}}}},
		{{columns: []string{"summary_id", "revision"}, values: [][]driver.Value{{"a", int64(1)}}}},
	} {
		script := &scriptedPostgresScript{queries: queries}
		store, cleanup := scriptedPostgresStore(t, script)
		_, err := store.ListCurrentSummaries(t.Context(), "owner_1", domain.SummaryListQuery{})
		assert.Error(t, err)
		assert.False(t, script.committed)
		assert.True(t, script.rolled)
		cleanup()
	}
}
