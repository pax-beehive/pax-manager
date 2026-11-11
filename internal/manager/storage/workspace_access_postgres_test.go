package storage

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostgresWorkspaceTurnExpiryPreservesOtherOwnersTurnsAndDomains(t *testing.T) {
	dsn := os.Getenv("PAX_MANAGER_QUEUE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_QUEUE_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.ExecContext(t.Context(), `CREATE TEMP TABLE agent_approvals (
        approval_id text PRIMARY KEY, owner_user_id text, request_node_id text,
        request_agent_id text, request_session_id text, domain text,
        operation text, resource_type text, resource_ref text, expires_at timestamptz
    );
    INSERT INTO agent_approvals VALUES
        ('matching','owner','node','agent','session','workspace','search','turn','turn-one',NULL),
        ('other-owner','foreign','node','agent','session','workspace','search','turn','turn-one',NULL),
        ('other-turn','owner','node','agent','session','workspace','search','turn','turn-two',NULL),
        ('other-domain','owner','node','agent','session','agent_action','search','turn','turn-one',NULL);`)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { return now })
	require.NoError(t, store.ExpireWorkspaceTurnApprovals(t.Context(),
		"owner", "node", "agent", "session", "turn-one"))
	var expiredAt time.Time
	require.NoError(t, db.QueryRowContext(t.Context(),
		`SELECT expires_at FROM agent_approvals WHERE approval_id='matching'`).Scan(&expiredAt))
	require.True(t, expiredAt.Equal(now))
	var untouched int
	require.NoError(t, db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM agent_approvals WHERE expires_at IS NULL`).Scan(&untouched))
	require.Equal(t, 3, untouched)
}
