package storage

import (
	"database/sql/driver"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryRecentPostgresGivenLatestCandidatesThenRevalidatesAccessBeforeHeaders(
	t *testing.T,
) {
	when := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for _, visible := range []bool{true, false} {
		sourceOwner := "owner_1"
		if !visible {
			sourceOwner = "other"
		}
		script := &scriptedPostgresScript{queries: []scriptedRows{
			{
				columns: []string{"summary_id", "revision", "created_at"},
				values:  [][]driver.Value{{"b", int64(2), when}, {"a", int64(1), when}},
			},
			summaryVersionRows(
				"b",
				"owner_1",
				2,
			), summarySourceRows(), summaryAccessRows(sourceOwner, "message_1"),
		}}
		if visible {
			script.queries = append(
				script.queries,
				summaryMessageRows("message_1", "current body", "owner_1"),
			)
		}
		store, cleanup := scriptedPostgresStore(t, script)
		page, err := store.ListRecentSummaries(
			t.Context(),
			"owner_1",
			domain.SummaryListQuery{Limit: 1},
		)
		require.NoError(t, err)
		require.NotEmpty(t, page.NextCursor)
		if visible {
			require.Len(t, page.Items, 1)
			assert.Equal(t, "b", page.Items[0].SummaryID)
			assert.EqualValues(t, 2, page.Items[0].Revision)
		} else {
			assert.Empty(t, page.Items)
		}
		assert.Contains(t, script.queryTexts[0], "DISTINCT ON (summary_id)")
		assert.Contains(t, script.queryTexts[0], "owner_user_id = $1")
		assert.Contains(t, script.queryTexts[0], "ORDER BY created_at DESC, summary_id DESC")
		assert.Equal(t, "owner_1", script.queryArgs[0][0].Value)
		assert.EqualValues(t, 2, script.queryArgs[0][3].Value)
		assert.True(t, script.committed)
		cleanup()
	}
}

func TestSummaryRecentPostgresGivenInvalidQueryOrStorageFailureThenFailsClosed(t *testing.T) {
	for _, query := range []domain.SummaryListQuery{{Limit: 101}, {Cursor: "bad"}} {
		store := &PostgresStore{}
		_, err := store.ListRecentSummaries(t.Context(), "owner_1", query)
		assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	}
	for _, queries := range [][]scriptedRows{nil,
		{{columns: []string{"summary_id", "revision", "created_at"}, values: [][]driver.Value{{"a", "invalid", time.Now()}}}},
		{{columns: []string{"summary_id", "revision", "created_at"}, values: [][]driver.Value{{"a", int64(1), time.Now()}}}},
	} {
		script := &scriptedPostgresScript{queries: queries}
		store, cleanup := scriptedPostgresStore(t, script)
		_, err := store.ListRecentSummaries(t.Context(), "owner_1", domain.SummaryListQuery{})
		assert.Error(t, err)
		assert.False(t, script.committed)
		assert.True(t, script.rolled)
		cleanup()
	}
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	cleanup()
	_, err := store.ListRecentSummaries(t.Context(), "owner_1", domain.SummaryListQuery{})
	assert.Error(t, err)
}

func TestSummaryRecentGivenLatestVersionsThenPagesNewestFirstWithStableTies(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	when := time.Date(2026, 11, 2, 10, 0, 0, 123456000, time.UTC)
	store.mu.Lock()
	for i, id := range []string{"a", "b", "c"} {
		store.secretaryLocked().summaries[id] = []domain.SummaryVersion{
			{SummaryID: id, OwnerUserID: "owner", Revision: 1, CreatedAt: when.Add(time.Hour)},
			{
				SummaryID:   id,
				OwnerUserID: "owner",
				Revision:    2,
				Body:        "current",
				CreatedAt:   when.Add(time.Duration(i/2) * time.Hour),
			},
		}
	}
	store.secretaryLocked().summaries["foreign"] = []domain.SummaryVersion{
		{SummaryID: "foreign", OwnerUserID: "other", Revision: 1, CreatedAt: when.Add(time.Hour)},
	}
	store.mu.Unlock()
	query := domain.SummaryListQuery{Limit: 1}
	for _, id := range []string{"c", "b", "a"} {
		page, err := store.ListRecentSummaries(t.Context(), "owner", query)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, id, page.Items[0].SummaryID)
		assert.EqualValues(t, 2, page.Items[0].Revision)
		query.Cursor = page.NextCursor
		if id != "a" {
			require.NotEmpty(t, query.Cursor)
			_, err = store.ListRecentSummaries(t.Context(), "other", query)
			assert.ErrorIs(t, err, domain.ErrInvalidCursor)
			_, err = store.ListCurrentSummaries(t.Context(), "owner", query)
			assert.ErrorIs(t, err, domain.ErrInvalidCursor)
		}
	}
	assert.Empty(t, query.Cursor)
}

func TestSummaryRecentGivenInvalidCursorOrHiddenCoverageThenRejectsOrAdvances(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	for _, query := range []domain.SummaryListQuery{{Limit: -1}, {Limit: 101}, {Cursor: "bad"},
		{Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"o":"owner","a":"id","t":"invalid"}`))},
		{Cursor: summaryNextCursor("owner", "id")},
		{Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"o":"owner","a":"id","t":"2026-11-02T00:00:00Z","extra":true}`))}} {
		_, err := store.ListRecentSummaries(t.Context(), "owner", query)
		assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	}
	_, err := store.ListRecentSummaries(t.Context(), "", domain.SummaryListQuery{})
	assert.ErrorIs(t, err, ErrUnauthorized)
	store.mu.Lock()
	store.secretaryLocked().summaries["z"] = []domain.SummaryVersion{
		{SummaryID: "z", OwnerUserID: "owner", Revision: 1,
			Coverage: []domain.MessageVersionRef{{MessageID: "deleted"}}, CreatedAt: time.Now()},
	}
	store.secretaryLocked().summaries["a"] = []domain.SummaryVersion{
		{SummaryID: "a", OwnerUserID: "owner", Revision: 1},
	}
	store.mu.Unlock()
	page, err := store.ListRecentSummaries(t.Context(), "owner", domain.SummaryListQuery{Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	require.NotEmpty(t, page.NextCursor)
	page, err = store.ListRecentSummaries(
		t.Context(),
		"owner",
		domain.SummaryListQuery{Cursor: page.NextCursor},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "a", page.Items[0].SummaryID)
	assert.Empty(t, page.NextCursor)
}
