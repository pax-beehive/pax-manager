package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPostgresE2EEPairingLatestRequestWins(t *testing.T) {
	open := pairingPostgresFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(open(), func() time.Time { return now })
	testE2EEPairingLifecycle(t, store, &now)
}

func TestPostgresE2EEPairingConcurrentCreationLeavesOneCurrentRequest(t *testing.T) {
	open := pairingPostgresFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	const count = 8
	stores := make([]*PostgresStore, count)
	requests := make([]E2EEPairingRequest, count)
	errors := make([]error, count)
	for i := range count {
		stores[i] = NewPostgresStore(open(), func() time.Time { return now })
		requests[i] = testE2EEPairingRequest(now)
		requests[i].PairingID = fmt.Sprintf("pair_concurrent_%d", i)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var group sync.WaitGroup
	start := make(chan struct{})
	for i := range count {
		group.Go(func() {
			<-start
			_, errors[i] = stores[i].CreateE2EEPairingRequest(ctx, requests[i])
		})
	}
	close(start)
	group.Wait()
	for _, err := range errors {
		require.NoError(t, err)
	}
	pending, err := stores[0].ListPendingE2EEPairingRequests(ctx, "user_1", "agent_1", count)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	for i, request := range requests {
		keyPackage := testE2EEKeyPackage(now)
		keyPackage.PairingID = request.PairingID
		_, err = stores[i].CompleteE2EEPairing(ctx, request, keyPackage)
		if request.PairingID == pending[0].PairingID {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)
		}
	}
}

func TestPostgresE2EEPairingMigrationPreservesPublishedPackage(t *testing.T) {
	open := pairingPostgresFixture(t)
	db := open()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { return now })
	a := testE2EEPairingRequest(now)
	a.CreatedAt = now.Add(-time.Minute)
	_, err := store.CreateE2EEPairingRequest(t.Context(), a)
	require.NoError(t, err)
	_, err = store.CompleteE2EEPairing(t.Context(), a, testE2EEKeyPackage(now))
	require.NoError(t, err)
	b := testE2EEPairingRequest(now)
	b.PairingID = "pair_b"
	_, err = store.CreateE2EEPairingRequest(t.Context(), b)
	require.NoError(t, err)
	// Simulate the previous schema, where both requests had no supersession marker.
	_, err = db.ExecContext(
		t.Context(),
		"ALTER TABLE e2ee_pairing_requests DROP COLUMN superseded_at",
	)
	require.NoError(t, err)
	for range 2 {
		_, err = db.ExecContext(t.Context(), pairingSchemaSQL(t))
		require.NoError(t, err)
	}
	_, err = store.GetE2EEPairingRequest(t.Context(), a.OwnerUserID, a.AgentID, a.PairingID)
	require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)
	_, err = store.GetE2EEPairingRequest(t.Context(), b.OwnerUserID, b.AgentID, b.PairingID)
	require.NoError(t, err)
	loaded, err := store.GetE2EEKeyPackage(
		t.Context(),
		a.OwnerUserID,
		a.AgentID,
		a.DeviceID,
		a.KeyEpoch,
	)
	require.NoError(t, err)
	require.Equal(t, a.PairingID, loaded.PairingID)
}

func TestPostgresE2EEPairingCreationSerializesWithCompletion(t *testing.T) {
	for _, creationFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("creation_first_%t", creationFirst), func(t *testing.T) {
			testE2EEPairingRace(t, creationFirst)
		})
	}
}

func testE2EEPairingRace(t *testing.T, creationFirst bool) {
	t.Helper()
	open := pairingPostgresFixture(t)
	gateDB, createDB, completeDB, observer := open(), open(), open(), open()
	now := time.Now().UTC().Truncate(time.Microsecond)
	creator := NewPostgresStore(createDB, func() time.Time { return now })
	completer := NewPostgresStore(completeDB, func() time.Time { return now })
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a := testE2EEPairingRequest(now)
	_, err := creator.CreateE2EEPairingRequest(ctx, a)
	require.NoError(t, err)
	gate, err := gateDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = gate.Rollback() }()
	require.NoError(t, lockE2EEPairing(ctx, gate, a))
	var gatePID, createPID, completePID int
	require.NoError(t, gate.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&gatePID))
	require.NoError(t, createDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&createPID))
	require.NoError(
		t,
		completeDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&completePID),
	)
	b := a
	b.PairingID = "pair_b"
	created, completed := make(chan error, 1), make(chan error, 1)
	create := func() {
		_, callErr := creator.CreateE2EEPairingRequest(ctx, b)
		created <- callErr
	}
	complete := func() {
		_, callErr := completer.CompleteE2EEPairing(ctx, a, testE2EEKeyPackage(now))
		completed <- callErr
	}
	if creationFirst {
		go create()
		waitForPostgresBlocker(t, ctx, observer, createPID, gatePID)
		go complete()
		waitForPostgresBlocker(t, ctx, observer, completePID, gatePID)
	} else {
		go complete()
		waitForPostgresBlocker(t, ctx, observer, completePID, gatePID)
		go create()
		waitForPostgresBlocker(t, ctx, observer, createPID, gatePID)
	}
	require.NoError(t, gate.Commit())
	require.NoError(t, <-created)
	if creationFirst {
		require.ErrorIs(t, <-completed, domain.ErrE2EEPairingSuperseded)
	} else {
		require.NoError(t, <-completed)
		oldPackage, loadErr := creator.GetE2EEKeyPackage(ctx, a.OwnerUserID, a.AgentID, a.DeviceID, a.KeyEpoch)
		require.NoError(t, loadErr)
		require.Equal(t, a.PairingID, oldPackage.PairingID)
	}
	_, err = completer.CompleteE2EEPairing(ctx, a, testE2EEKeyPackage(now))
	require.ErrorIs(t, err, domain.ErrE2EEPairingSuperseded)
	packageB := testE2EEKeyPackage(now)
	packageB.PairingID = b.PairingID
	_, err = completer.CompleteE2EEPairing(ctx, b, packageB)
	require.NoError(t, err)
}

// The test uses the production pairing DDL in a disposable schema, including
// its foreign keys and startup migration, without requiring unrelated extensions.
func pairingPostgresFixture(t *testing.T) func() *sql.DB {
	t.Helper()
	dsn := os.Getenv("PAX_MANAGER_PAIRING_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_PAIRING_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("pairing_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	config.RuntimeParams["search_path"] = schema
	open := func() *sql.DB {
		db := stdlib.OpenDB(*config)
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	_, err = db.ExecContext(t.Context(), `
		CREATE TABLE users(user_id TEXT PRIMARY KEY);
		CREATE TABLE nodes(node_id TEXT PRIMARY KEY);
		CREATE TABLE agents(agent_id TEXT PRIMARY KEY);
		INSERT INTO users VALUES ('user_1');
		INSERT INTO nodes VALUES ('node_1');
		INSERT INTO agents VALUES ('agent_1');`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), pairingSchemaSQL(t))
	require.NoError(t, err)
	return open
}

func pairingSchemaSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	ddl := string(raw)
	start := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS e2ee_pairing_requests")
	end := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS agent_sessions")
	require.Positive(t, start)
	require.Greater(t, end, start)
	return ddl[start:end]
}
