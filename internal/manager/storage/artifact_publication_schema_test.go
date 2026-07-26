package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSchemaGivenArtifactPublicationsThenDefinesOwnerSessionLookup(t *testing.T) {
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)

	sql := string(initSQL)
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS artifact_publications")
	assert.Contains(t, sql, "publication_id TEXT PRIMARY KEY")
	assert.Contains(t, sql, "owner_user_id TEXT NOT NULL REFERENCES users(user_id)")
	assert.Contains(t, sql, "idx_artifact_publications_session_created")
	assert.Contains(t, sql, "ON artifact_publications(owner_user_id, session_id, created_at)")
	assert.Less(
		t,
		strings.Index(sql, "CREATE TABLE IF NOT EXISTS artifact_publications"),
		strings.Index(sql, "CREATE INDEX IF NOT EXISTS idx_artifact_publications_session_created"),
	)
}
