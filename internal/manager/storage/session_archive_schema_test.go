package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionArchiveSchemaGivenDatabaseInitializationThenArchivedAtExists(t *testing.T) {
	t.Parallel()

	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	assert.Contains(
		t,
		string(sql),
		"ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ",
	)
}
