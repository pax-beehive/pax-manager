package storage

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workspaceApprovalConcurrentPool(t *testing.T, db *sql.DB) {
	t.Helper()
	// The shared fixture selects its private schema on one connection. Initialize
	// every connection before allowing the blocked transaction test to run.
	var searchPath string
	require.NoError(t, db.QueryRowContext(t.Context(), "SHOW search_path").Scan(&searchPath))
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	var connections []*sql.Conn
	defer func() {
		for _, connection := range connections {
			require.NoError(t, connection.Close())
		}
	}()
	for range 4 {
		connection, err := db.Conn(t.Context())
		require.NoError(t, err)
		connections = append(connections, connection)
		_, err = connection.ExecContext(
			t.Context(),
			"SELECT set_config('search_path',$1,false)",
			searchPath,
		)
		require.NoError(t, err)
	}
}

func TestPostgresWorkspaceApprovalRechecksExpiryOrCancellationAfterTakingRowLock(t *testing.T) {
	for _, scenario := range []string{"turn_expiry", "request_timeout", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			generic, owner, clock := workspaceApprovalFixture(t, "postgres")
			store := generic.(*PostgresStore)
			workspaceApprovalConcurrentPool(t, store.db)
			deadline := time.Unix(0, clock.Load()).UTC().Add(600 * time.Second)
			approval := createWorkspaceApproval(t, store, owner, &deadline)
			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(
				t.Context(),
				`SELECT approval_id FROM agent_approvals WHERE approval_id=$1 FOR UPDATE`,
				approval.ApprovalID,
			)
			require.NoError(t, err)
			result := make(chan error, 1)
			go func() {
				_, decideErr := store.DecideApproval(
					t.Context(),
					UserPrincipal{User: User{UserID: owner}},
					approval.ApprovalID,
					ApprovalDecisionRequest{DecisionOption: "allow_once"},
				)
				result <- decideErr
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				queryErr := store.db.QueryRowContext(t.Context(), `
					SELECT EXISTS (
						SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid
						WHERE l.relation='agent_approvals'::regclass AND a.wait_event_type='Lock'
						AND a.query LIKE '%FOR UPDATE' AND a.pid<>pg_backend_pid()
					)`).Scan(&waiting)
				return queryErr == nil && waiting
			}, 5*time.Second, 5*time.Millisecond)
			now := time.Unix(0, clock.Load()).UTC().Add(time.Second)
			switch scenario {
			case "turn_expiry":
				clock.Store(now.UnixNano())
				_, err = tx.ExecContext(
					t.Context(),
					`UPDATE agent_approvals SET expires_at=$2 WHERE approval_id=$1`,
					approval.ApprovalID,
					now,
				)
			case "request_timeout":
				clock.Store(deadline.UnixNano())
			case "cancelled":
				_, err = tx.ExecContext(
					t.Context(),
					`UPDATE agent_approvals SET grant_revoked_at=$2 WHERE approval_id=$1`,
					approval.ApprovalID,
					now,
				)
			}
			require.NoError(t, err)
			require.NoError(t, tx.Commit())
			select {
			case err = <-result:
				assert.ErrorIs(t, err, ErrConflict)
			case <-time.After(5 * time.Second):
				t.Fatal("approval did not finish after the row lock was released")
			}
			current, err := store.GetApproval(
				t.Context(),
				UserPrincipal{User: User{UserID: owner}},
				approval.ApprovalID,
			)
			require.NoError(t, err)
			assert.Equal(t, "pending", current.Status)
		})
	}
}
