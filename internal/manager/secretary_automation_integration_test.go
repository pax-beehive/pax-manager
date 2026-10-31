//go:build integration

package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestSecretaryAutomationPostgresGivenOwnedAgentThenHTTPAndACPCommitFrozenVersions(
	t *testing.T,
) {
	dsn := os.Getenv("PAX_MANAGER_SECRETARY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("secretary_automation_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"init.sql", "summaries.sql", "search.sql", "secretary.sql"} {
		ddl, readErr := os.ReadFile(filepath.Join("..", "..", "db", name))
		require.NoError(t, readErr)
		_, err = db.ExecContext(t.Context(), string(ddl))
		require.NoError(t, err, name)
	}
	assertSecretaryAutomation(t, storage.NewPostgresStore(db, time.Now))
}
