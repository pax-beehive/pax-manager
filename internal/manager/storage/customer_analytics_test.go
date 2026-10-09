package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCustomerAnalyticsGivenMixedInventoryThenOnlyRealBindingsAndUserMessagesCount(
	t *testing.T,
) {
	dsn := os.Getenv("PAX_MANAGER_ANALYTICS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_ANALYTICS_TEST_DATABASE_URL to isolated PostgreSQL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*cfg)
	schema := fmt.Sprintf("analytics_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	cfg.RuntimeParams["search_path"] = schema
	sqlDB := stdlib.OpenDB(*cfg)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	for _, ddl := range []string{
		`CREATE TABLE users(user_id TEXT PRIMARY KEY, email TEXT, created_at DATETIME)`,
		`CREATE TABLE customer_visits(user_id TEXT PRIMARY KEY REFERENCES users(user_id), first_visit_at DATETIME, last_visit_at DATETIME)`,
		`CREATE TABLE nodes(node_id TEXT, owner_user_id TEXT, kind TEXT, deleted_at DATETIME)`,
		`CREATE TABLE agents(agent_id TEXT, owner_user_id TEXT, node_id TEXT, registered_at DATETIME, last_heartbeat DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE messages(owner_user_id TEXT, agent_id TEXT, role TEXT, direction TEXT, created_at DATETIME)`,
		`CREATE TABLE e2ee_messages(owner_user_id TEXT)`,
		`CREATE TABLE agent_sessions(agent_id TEXT)`,
	} {
		require.NoError(t, db.Exec(strings.ReplaceAll(ddl, "DATETIME", "TIMESTAMPTZ")).Error)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	require.NoError(
		t,
		db.Exec(
			`INSERT INTO users VALUES ('u1','one@example.invalid',?),('u2','two@example.invalid',?)`,
			now,
			now,
		).Error,
	)
	require.NoError(
		t,
		db.Exec(
			`INSERT INTO nodes VALUES ('n1','u1','paxd',NULL),('n2','u1','paxl',NULL),('n3','u1','paxd',?)`,
			now,
		).Error,
	)
	for _, a := range []struct {
		id, node           string
		heartbeat, deleted any
	}{
		{"a1", "n1", now, nil}, {"a2", "n1", now, now}, {"a3", "n2", now, nil}, {"a4", "n1", nil, nil}, {"a5", "n3", now, nil},
	} {
		require.NoError(
			t,
			db.Exec(
				`INSERT INTO agents VALUES (?,'u1',?,?,?,?)`,
				a.id,
				a.node,
				now,
				a.heartbeat,
				a.deleted,
			).Error,
		)
	}
	require.NoError(
		t,
		db.Exec(
			`INSERT INTO messages VALUES (NULL,'a1','user','user_to_agent',?),('u1','a1','assistant','agent_to_user',?),('u1','a1','user','user_to_agent',?)`,
			now,
			now,
			now.Add(time.Hour),
		).Error,
	)
	require.NoError(t, db.Exec(`INSERT INTO e2ee_messages VALUES ('u2'),('u2')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO agent_sessions VALUES ('a1'),('a1')`).Error)
	store := &PostgresStore{gormDB: db}
	require.NoError(t, store.RecordCustomerVisit(t.Context(), "u1", now))
	require.NoError(t, store.RecordCustomerVisit(t.Context(), "u1", now.Add(time.Hour)))
	require.NoError(t, store.RecordCustomerVisit(t.Context(), "u1", now.Add(-time.Hour)))
	require.NoError(t, store.RecordCustomerVisit(t.Context(), "missing", now))
	rows, err := store.CustomerAnalytics(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, int64(1), rows[0].Devices)
	assert.Equal(t, int64(1), rows[0].Agents)
	assert.Equal(t, int64(2), rows[0].UserMessages)
	assert.Equal(t, int64(2), rows[0].Sessions)
	assert.WithinDuration(t, now, *rows[0].FirstVisitAt, time.Millisecond)
	assert.WithinDuration(t, now.Add(time.Hour), *rows[0].LastVisitAt, time.Millisecond)
	assert.NotNil(t, rows[0].FirstBoundAt)
	assert.Nil(t, rows[1].LastVisitAt)
	assert.Equal(t, int64(0), rows[1].UserMessages, "encrypted records do not prove user sends")
	assert.Equal(t, int64(2), rows[1].EncryptedRecords)
	require.NoError(t, db.Exec(`DROP TABLE customer_visits`).Error)
	assert.Error(t, store.RecordCustomerVisit(t.Context(), "u1", now))
	_, err = store.CustomerAnalytics(t.Context())
	assert.Error(t, err)
}
