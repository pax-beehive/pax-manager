package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSchemaGivenSessionsThenDefinesOnlyThePrimaryLogicalProjectField(
	t *testing.T,
) {
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)

	sql := string(initSQL)
	assert.Contains(t, sql, "primary_project_id TEXT")
	assert.Contains(
		t,
		sql,
		"ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS primary_project_id TEXT",
	)
	assert.Contains(t, sql, "ALTER TABLE agent_sessions DROP COLUMN IF EXISTS project_id")
}
