package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryArtifactPublicationGivenNoOriginWhenPutThenDefaultsToAgent(t *testing.T) {
	store := NewMemoryStore(func() time.Time { return time.Unix(100, 0) })

	publication, err := store.PutArtifactPublication(context.Background(), ArtifactPublication{
		PublicationID: "apub_agent",
		OwnerUserID:   "user_1",
		NodeID:        "node_1",
		AgentID:       "agent_1",
		SessionID:     "sess_1",
		Filename:      "report.pdf",
	})

	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactPublicationOriginAgent, publication.Origin)
	assert.Empty(t, publication.RequestedByUserID)
	assert.Empty(t, publication.RequestedPath)
}

func TestMemoryArtifactPublicationGivenAgentRowWhenMarkedUserFetchThenRecordsAudit(
	t *testing.T,
) {
	store := NewMemoryStore(func() time.Time { return time.Unix(100, 0) })
	ctx := context.Background()
	base := ArtifactPublication{
		PublicationID: "apub_fetch",
		OwnerUserID:   "user_1",
		NodeID:        "node_1",
		AgentID:       "agent_1",
		SessionID:     "sess_1",
		Filename:      "report.pdf",
	}
	_, err := store.PutArtifactPublication(ctx, base)
	require.NoError(t, err)

	marked, err := store.MarkArtifactPublicationUserFetch(
		ctx, "apub_fetch", "user_1", "/home/u/report.pdf",
	)
	require.NoError(t, err)
	again, err := store.PutArtifactPublication(ctx, base)
	require.NoError(t, err)

	assert.Equal(t, domain.ArtifactPublicationOriginUserFetch, marked.Origin)
	assert.Equal(t, "user_1", marked.RequestedByUserID)
	assert.Equal(t, "/home/u/report.pdf", marked.RequestedPath)
	assert.Equal(t, marked, again)
}

func TestMemoryArtifactPublicationGivenOtherOwnerWhenMarkedThenReturnsNotFound(t *testing.T) {
	store := NewMemoryStore(func() time.Time { return time.Unix(100, 0) })
	ctx := context.Background()
	_, err := store.PutArtifactPublication(ctx, ArtifactPublication{
		PublicationID: "apub_owned",
		OwnerUserID:   "user_1",
		NodeID:        "node_1",
		AgentID:       "agent_1",
		SessionID:     "sess_1",
		Filename:      "report.pdf",
	})
	require.NoError(t, err)

	_, err = store.MarkArtifactPublicationUserFetch(ctx, "apub_owned", "user_2", "/x")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.MarkArtifactPublicationUserFetch(ctx, "apub_missing", "user_1", "/x")
	require.ErrorIs(t, err, ErrNotFound)
}
