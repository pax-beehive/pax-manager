package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresArtifactPublicationGivenSourceTurnThenPersistsImmutableAssociation(t *testing.T) {
	db := pairingPostgresFixture(t)()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	ddl := string(raw)
	start := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS artifact_publications")
	end := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS artifact_uploads")
	require.Positive(t, start)
	require.Greater(t, end, start)
	for range 2 {
		_, err = db.ExecContext(t.Context(), ddl[start:end])
		require.NoError(t, err)
	}
	store := NewPostgresStore(db, time.Now)
	publication := ArtifactPublication{
		PublicationID: "apub_turn", OwnerUserID: "user_1", NodeID: "node_1",
		AgentID: "agent_1", SessionID: "session_1", TurnID: "turn_original", Filename: "report.pdf",
	}
	stored, err := store.PutArtifactPublication(t.Context(), publication)
	require.NoError(t, err)
	assert.Equal(t, "turn_original", stored.TurnID)
	loaded, err := store.GetArtifactPublication(
		t.Context(),
		UserPrincipal{User: User{UserID: "user_1"}},
		"apub_turn",
	)
	require.NoError(t, err)
	assert.Equal(t, stored, loaded)
	replayed, err := store.PutArtifactPublication(t.Context(), publication)
	require.NoError(t, err)
	assert.Equal(t, stored, replayed)
	publication.TurnID = "turn_next"
	_, err = store.PutArtifactPublication(t.Context(), publication)
	assert.ErrorIs(t, err, ErrConflict)
}
