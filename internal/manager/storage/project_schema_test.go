package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSchemaGivenLogicalProjectsThenDefinesHierarchyWithoutForeignKeys(t *testing.T) {
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)

	sql := string(initSQL)
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS projects")
	require.NotEqual(t, -1, start)
	end := strings.Index(sql[start:], ");")
	require.NotEqual(t, -1, end)
	projectTable := sql[start : start+end]

	assert.Contains(t, projectTable, "project_id TEXT PRIMARY KEY")
	assert.Contains(t, projectTable, "owner_user_id TEXT NOT NULL")
	assert.Contains(t, projectTable, "parent_project_id TEXT")
	assert.Contains(t, projectTable, "archived_at TIMESTAMPTZ")
	assert.NotContains(t, projectTable, "REFERENCES")
	assert.Contains(t, sql, "idx_projects_owner")
	assert.Contains(t, sql, "idx_projects_parent")
}
