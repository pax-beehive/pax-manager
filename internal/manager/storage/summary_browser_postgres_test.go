package storage

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryBrowserPostgresGivenLongPublishedIdentityThenGeneratedCursorAndDetailWork(
	t *testing.T,
) {
	store := summaryPostgresFixture(t)
	id := strings.Repeat("a", 5000)
	request := summaryTestRequest("old body")
	request.SummaryID = id
	_, err := store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	request.SummaryID, request.RequestID = "z", "next-summary"
	_, err = store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	first, err := store.ListCurrentSummaries(
		t.Context(),
		"owner_1",
		domain.SummaryListQuery{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	assert.Equal(t, id, first.Items[0].SummaryID)
	require.Greater(t, len(first.NextCursor), 2048)
	last, err := store.ListCurrentSummaries(
		t.Context(),
		"owner_1",
		domain.SummaryListQuery{Limit: 1, Cursor: first.NextCursor},
	)
	require.NoError(t, err)
	require.Len(t, last.Items, 1)
	assert.Equal(t, "z", last.Items[0].SummaryID)
	assert.Empty(t, last.NextCursor)
	exact, err := store.GetSummary(t.Context(), "owner_1", id, 1)
	require.NoError(t, err)
	assert.Equal(t, id, exact.SummaryID)
}

func TestSummaryBrowserPostgresGivenPublishedVersionsThenListsOnlyCurrentOwnedVisibleSummaries(
	t *testing.T,
) {
	store := summaryPostgresFixture(t)
	request := summaryTestRequest("old body")
	_, err := store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	request.ExpectedRevision = 1
	request.RequestID = "second-version"
	request.Body = "Updated body"
	_, err = store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	request.SummaryID = "summary_2"
	request.ExpectedRevision = 0
	request.RequestID = "other-summary"
	_, err = store.PublishSummary(t.Context(), "owner_1", request)
	require.NoError(t, err)
	page, err := store.ListCurrentSummaries(
		t.Context(),
		"owner_1",
		domain.SummaryListQuery{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "summary_1", page.Items[0].SummaryID)
	assert.EqualValues(t, 2, page.Items[0].Revision)
	assert.Equal(t, "Updated body", page.Items[0].BodyPreview)
	require.NotEmpty(t, page.NextCursor)
	last, err := store.ListCurrentSummaries(
		t.Context(),
		"owner_1",
		domain.SummaryListQuery{Cursor: page.NextCursor, Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, last.Items, 1)
	assert.Equal(t, "summary_2", last.Items[0].SummaryID)
	assert.Empty(t, last.NextCursor)
	other, err := store.ListCurrentSummaries(t.Context(), "other", domain.SummaryListQuery{})
	require.NoError(t, err)
	assert.Empty(t, other.Items)
	_, err = store.ListCurrentSummaries(
		t.Context(),
		"other",
		domain.SummaryListQuery{Cursor: page.NextCursor},
	)
	assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agent_sessions SET archived_at=now() WHERE session_id='session_1'`,
	)
	require.NoError(t, err)
	hidden, err := store.ListCurrentSummaries(t.Context(), "owner_1", domain.SummaryListQuery{})
	require.NoError(t, err)
	assert.Empty(t, hidden.Items)
	_, err = store.GetSummary(t.Context(), "owner_1", "summary_1", 1)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
