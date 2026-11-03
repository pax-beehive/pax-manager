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

func TestSecretaryValidationRetryPostgresGivenRejectedCompletedACPOutputThenPublishesFreshValidRun(
	t *testing.T,
) {
	dsn := os.Getenv("PAX_MANAGER_SECRETARY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	for _, code := range []string{"schema", "evidence"} {
		t.Run(code, func(t *testing.T) {
			config, err := pgx.ParseConfig(dsn)
			require.NoError(t, err)
			admin := stdlib.OpenDB(*config)
			schema := fmt.Sprintf("secretary_validation_acp_%d", time.Now().UnixNano())
			_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
			require.NoError(t, err)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
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
			assertSecretaryValidationRetry(t, storage.NewPostgresStore(db, time.Now), code, func() {
				_, updateErr := db.ExecContext(
					t.Context(),
					"UPDATE secretary_jobs SET available_at=NOW()-INTERVAL '1 second'",
				)
				require.NoError(t, updateErr)
			})
		})
	}
}
