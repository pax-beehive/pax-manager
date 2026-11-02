package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryRecentPostgresGivenMultipleRevisionsThenPagesNewestCurrentOwnedSources(
	t *testing.T,
) {
	store := summaryPostgresFixture(t)
	when := time.Date(2026, 11, 2, 12, 0, 0, 123456000, time.UTC)
	for i, id := range []string{"a", "b", "c"} {
		store.now = func() time.Time { return when.Add(time.Duration(i/2) * time.Hour) }
		request := summaryTestRequest("old body")
		request.SummaryID, request.RequestID = id, "publish-"+id
		_, err := store.PublishSummary(t.Context(), "owner_1", request)
		require.NoError(t, err)
	}
	store.now = func() time.Time { return when.Add(2 * time.Hour) }
	request := summaryTestRequest("old body")
	request.SummaryID, request.RequestID, request.ExpectedRevision = "a", "revise-a", 1
	request.Body = "Latest result"
	_, err := store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	query := domain.SummaryListQuery{Limit: 1}
	for _, id := range []string{"a", "c", "b"} {
		page, readErr := store.ListRecentSummaries(t.Context(), "owner_1", query)
		require.NoError(t, readErr)
		require.Len(t, page.Items, 1)
		assert.Equal(t, id, page.Items[0].SummaryID)
		if id == "a" {
			assert.EqualValues(t, 2, page.Items[0].Revision)
			assert.Equal(t, "Latest result", page.Items[0].BodyPreview)
		}
		query.Cursor = page.NextCursor
	}
	assert.Empty(t, query.Cursor)
	other, err := store.ListRecentSummaries(t.Context(), "other", domain.SummaryListQuery{})
	require.NoError(t, err)
	assert.Empty(t, other.Items)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agent_sessions SET archived_at=now() WHERE session_id='session_1'`,
	)
	require.NoError(t, err)
	page, err := store.ListRecentSummaries(
		t.Context(),
		"owner_1",
		domain.SummaryListQuery{Limit: 1},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.NotEmpty(t, page.NextCursor)
}
