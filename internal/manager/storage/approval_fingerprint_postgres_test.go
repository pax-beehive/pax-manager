package storage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresApprovalGivenLongFingerprintThenGrantLookupPreservesExactMatch(t *testing.T) {
	db := pairingPostgresFixture(t)()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	ddl := string(raw)
	start := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS agent_approvals")
	end := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS agent_profiles")
	require.Positive(t, start)
	require.Greater(t, end, start)
	ddl = ddl[start:end]
	_, err = db.ExecContext(t.Context(), ddl)
	require.NoError(t, err)
	// Simulate an installed legacy index before applying the upgrade twice.
	_, err = db.ExecContext(
		t.Context(),
		`CREATE INDEX IF NOT EXISTS idx_agent_approvals_grant_lookup ON agent_approvals(owner_user_id, domain, operation, action_fingerprint, decision, grant_node_id, grant_agent_id, grant_session_id, decided_at DESC)`,
	)
	require.NoError(t, err)
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO agent_approvals(approval_id,owner_user_id,domain,operation,resource_type,action_fingerprint,status,decision,decision_scope,grant_node_id,grant_agent_id,grant_session_id,decided_at) VALUES ('legacy','user_1','agent_action','execute','tool','legacy-command','decided','allow','session','node_1','agent_1','session_1',now())`,
	)
	require.NoError(t, err)
	for range 2 {
		_, err = db.ExecContext(t.Context(), ddl)
		require.NoError(t, err)
	}
	var fingerprint strings.Builder
	fingerprint.WriteString("acp:tool_call:execute:")
	for i := range 256 {
		fmt.Fprintf(&fingerprint, "%x", sha256.Sum256([]byte(fmt.Sprintf("command-%d", i))))
	}
	long := fingerprint.String()
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO agent_approvals(approval_id,owner_user_id,domain,operation,resource_type,action_fingerprint,status,decision,decision_scope,grant_node_id,grant_agent_id,grant_session_id,decided_at) VALUES ('long','user_1','agent_action','execute','tool',$1,'decided','allow','session','node_1','agent_1','session_1',now())`,
		long,
	)
	require.NoError(t, err)
	store := NewPostgresStore(db, time.Now)
	lookup := ApprovalGrantLookup{
		OwnerUserID:      "user_1",
		Domain:           "agent_action",
		Operation:        "execute",
		RequestNodeID:    "node_1",
		RequestAgentID:   "agent_1",
		RequestSessionID: "session_1",
	}
	for _, value := range []string{"legacy-command", long} {
		lookup.ActionFingerprint = value
		grant, lookupErr := store.FindReusableApprovalGrant(t.Context(), lookup)
		require.NoError(t, lookupErr)
		assert.Equal(t, value, grant.ActionFingerprint)
	}
	lookup.ActionFingerprint = long + "different"
	_, err = store.FindReusableApprovalGrant(t.Context(), lookup)
	assert.ErrorIs(t, err, ErrNotFound)
	lookup.ActionFingerprint = long
	lookup.RequestSessionID = "other-session"
	_, err = store.FindReusableApprovalGrant(t.Context(), lookup)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = db.ExecContext(t.Context(), `SET enable_seqscan = off`)
	require.NoError(t, err)
	rows, err := db.QueryContext(
		t.Context(),
		`EXPLAIN SELECT approval_id FROM agent_approvals WHERE owner_user_id = 'user_1' AND domain = 'agent_action' AND operation = 'execute' AND md5(action_fingerprint) = md5($1::text) AND action_fingerprint = $1`,
		long,
	)
	require.NoError(t, err)
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	assert.Contains(t, plan.String(), "idx_agent_approvals_grant_lookup_digest")

	var oldIndexExists bool
	err = db.QueryRowContext(t.Context(), `SELECT to_regclass('idx_agent_approvals_grant_lookup') IS NOT NULL`).
		Scan(&oldIndexExists)
	require.NoError(t, err)
	assert.False(t, oldIndexExists)
}
