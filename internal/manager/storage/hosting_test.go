package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestHostingStateRollbackAndCrossProcessSerialization(t *testing.T) {
	t.Run(
		"memory",
		func(t *testing.T) { exerciseHostingTransactions(t, []hosting.Store{NewMemoryStore(time.Now)}) },
	)
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("PAX_MANAGER_HOSTING_TEST_DATABASE_URL")
		if dsn == "" {
			t.Skip("requires isolated PostgreSQL")
		}
		db, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		db.SetMaxOpenConns(4)
		// A private schema keeps these tests independent from application tables.
		schema := fmt.Sprintf("hosting_%d", time.Now().UnixNano())
		_, err = db.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
		require.NoError(t, err)
		t.Cleanup(
			func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); _ = db.Close() },
		)
		// Separate pools share the same transaction row, as multiple managers do.
		open := func() *PostgresStore {
			config, err := pgx.ParseConfig(dsn)
			require.NoError(t, err)
			config.RuntimeParams["search_path"] = schema
			connection := stdlib.OpenDB(*config)
			t.Cleanup(func() { _ = connection.Close() })
			return NewPostgresStore(connection, time.Now)
		}
		first, second := open(), open()
		_, err = first.db.ExecContext(
			t.Context(),
			`CREATE TABLE hosting_state(id SMALLINT PRIMARY KEY,document JSONB NOT NULL);CREATE TABLE agent_registration_tokens(token_hash TEXT PRIMARY KEY,owner_user_id TEXT,expires_at TIMESTAMPTZ,created_at TIMESTAMPTZ)`,
		)
		require.NoError(t, err)
		exerciseHostingTransactions(t, []hosting.Store{first, second})
	})
}

func exerciseHostingTransactions(t *testing.T, stores []hosting.Store) {
	t.Helper()
	ctx := t.Context()
	sentinel := errors.New("rollback")
	err := stores[0].WithHostingState(
		ctx,
		func(state *hosting.State) error { state.SpentMicrousd = 99; return sentinel },
	)
	require.ErrorIs(t, err, sentinel)
	var workers sync.WaitGroup
	failures := make(chan error, 20)
	for i := range 20 {
		workers.Go(func() {
			failures <- stores[i%len(stores)].WithHostingState(ctx, func(state *hosting.State) error { state.SpentMicrousd++; return nil })
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.NoError(
		t,
		stores[0].WithHostingState(
			ctx,
			func(state *hosting.State) error { require.Equal(t, int64(20), state.SpentMicrousd); return nil },
		),
	)
}
