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
	assert.Contains(t, sql, "ALTER TABLE artifact_uploads ADD COLUMN IF NOT EXISTS artifact_id")
	assert.Contains(t, sql, "ALTER TABLE artifact_uploads ADD COLUMN IF NOT EXISTS node_id")
	assert.Contains(t, sql, "ALTER TABLE artifact_uploads ADD COLUMN IF NOT EXISTS agent_id")
	assert.Contains(t, sql, "idx_artifact_uploads_node_identity")
	assert.Contains(t, sql, "ON artifact_uploads(owner_user_id, session_id, filename, sha256)")
	assert.Contains(t, sql, "WHERE node_id <> '' AND sha256 <> ''")
	assert.Less(
		t,
		strings.Index(sql, "CREATE TABLE IF NOT EXISTS artifact_publications"),
		strings.Index(sql, "CREATE INDEX IF NOT EXISTS idx_artifact_publications_session_created"),
	)
}
