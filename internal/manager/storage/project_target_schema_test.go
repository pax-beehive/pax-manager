package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSchemaGivenProjectTargetsThenDefinesReusablePathsWithoutForeignKeys(
	t *testing.T,
) {
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)

	sql := string(initSQL)
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS project_targets")
	require.NotEqual(t, -1, start)
	end := strings.Index(sql[start:], ");")
	require.NotEqual(t, -1, end)
	targetTable := sql[start : start+end]

	assert.Contains(t, targetTable, "target_id TEXT PRIMARY KEY")
	assert.Contains(t, targetTable, "project_id TEXT NOT NULL")
	assert.Contains(t, targetTable, "agent_id TEXT NOT NULL")
	assert.Contains(t, targetTable, "cwd TEXT NOT NULL")
	assert.Contains(t, targetTable, "is_default BOOLEAN NOT NULL DEFAULT FALSE")
	assert.Contains(t, targetTable, "enabled BOOLEAN NOT NULL DEFAULT TRUE")
	assert.NotContains(t, targetTable, "REFERENCES")
	assert.Contains(t, sql, "idx_project_targets_one_default")
}
