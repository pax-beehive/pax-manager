package storage

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestRegionalUserGivenConcurrentPostgresProvisioningThenOneIdentityWins(t *testing.T) {
	dsn := os.Getenv("PAX_MANAGER_REGION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_REGION_TEST_DATABASE_URL to isolated PostgreSQL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*cfg)
	schema := fmt.Sprintf("region_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(
		t.Context(),
		`CREATE TABLE users (user_id TEXT PRIMARY KEY, email TEXT UNIQUE NOT NULL, display_name TEXT NOT NULL DEFAULT '', role TEXT, created_at TIMESTAMPTZ, last_seen_at TIMESTAMPTZ)`,
	)
	require.NoError(t, err)
	store := NewPostgresStore(db, time.Now)
	var wg sync.WaitGroup
	results := make(chan domain.User, 16)
	failures := make(chan error, 16)
	for i := range 16 {
		wg.Go(func() {
			id := "usr_first"
			if i%2 == 0 {
				id = "usr_second"
			}
			user, err := store.EnsureRegionalUser(t.Context(), id, "owner@example.com", "user")
			if err != nil {
				failures <- err
			} else {
				results <- user
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	winner, err := store.GetUserByEmail(t.Context(), "owner@example.com")
	require.NoError(t, err)
	for user := range results {
		assert.Equal(t, winner.UserID, user.UserID)
	}
	for err := range failures {
		assert.ErrorIs(t, err, domain.ErrConflict)
	}
	for range 3 {
		user, err := store.EnsureRegionalUser(t.Context(), winner.UserID, winner.Email, "user")
		require.NoError(t, err)
		assert.Equal(t, winner, user)
	}
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM users").Scan(&count))
	assert.Equal(t, 1, count)
}
