package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeSessionConcurrentPostgresRegistration(t *testing.T) {
	dsn := os.Getenv("PAX_MANAGER_CRON_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_CRON_TEST_DATABASE_URL to isolated PostgreSQL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*cfg)
	defer func() { _ = admin.Close() }()
	schema := fmt.Sprintf("cron_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	defer func() { _ = db.Close() }()
	ddl, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(ddl))
	require.NoError(t, err)
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO users(user_id,email) VALUES ('owner','cron@example.test'); INSERT INTO nodes(node_id,owner_user_id,hostname,api_key_hash) VALUES ('node','owner','host','node-key'); INSERT INTO agents(agent_id,owner_user_id,node_id,hostname,os,api_key_hash) VALUES ('agent','owner','node','host','linux','agent-key')`,
	)
	require.NoError(t, err)
	store := NewPostgresStore(db, time.Now)
	request := &domain.RegisterNodeSessionRequest{
		NativeID: "native",
		CWD:      "/workspace",
		Origin: domain.SessionOrigin{
			Kind:           "cronjob",
			RegistrationID: "binding",
			CronjobID:      "cron",
			RunID:          "run",
			Name:           "Daily review",
		},
	}
	node := domain.Node{NodeID: "node", OwnerUserID: "owner"}
	var wg sync.WaitGroup
	results := make(chan AgentSession, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			session, err := store.RegisterNodeSession(t.Context(), node, "agent", request)
			if err != nil {
				failures <- err
			} else {
				results <- session
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var id string
	for session := range results {
		if id == "" {
			id = session.SessionID
		}
		require.Equal(t, id, session.SessionID)
		require.Equal(t, "e2ee", session.Transport)
		require.Equal(t, "/workspace", session.PaxConfig.CWD)
	}
	require.NotEmpty(t, id)
	changed := *request
	changed.NativeID = "other"
	_, err = store.RegisterNodeSession(t.Context(), node, "agent", &changed)
	require.ErrorIs(t, err, ErrConflict)
	var count int
	require.NoError(
		t,
		db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM agent_sessions").Scan(&count),
	)
	require.Equal(t, 1, count)
}
