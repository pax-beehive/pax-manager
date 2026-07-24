package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scriptedPostgresDriverName = "pax_manager_scripted_postgres"

var (
	scriptedPostgresRegisterOnce sync.Once
	scriptedPostgresMu           sync.Mutex
	scriptedPostgresActive       *scriptedPostgresScript
)

type scriptedPostgresScript struct {
	queries                 []scriptedRows
	queryTexts              []string
	queryArgs               [][]driver.NamedValue
	queueStateInsertResults []int64
	execResults             []int64
	execTexts               []string
	execArgs                [][]driver.NamedValue
	execCount               int
	committed               bool
	rolled                  bool
}

type scriptedRows struct {
	columns []string
	values  [][]driver.Value
}

func TestPostgresStoreUpsertAgentSessions(t *testing.T) {
	t.Run(
		"Given a node-owned agent when upserting sessions then it commits the session upsert",
		func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
					{columns: []string{"session_id"}},
				},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()

			err := store.UpsertAgentSessions(
				context.Background(),
				Node{NodeID: "node_1"},
				"agent_1",
				[]SessionStatusInput{{SessionID: "codex:abc", Status: "available"}},
			)

			require.NoError(t, err)
			assert.Equal(t, 1, script.execCount)
			require.Len(t, script.execTexts, 1)
			assert.NotContains(t, script.execTexts[0], "custom_session_name")
			assert.True(t, script.committed)
			assert.False(t, script.rolled)
		},
	)

	t.Run(
		"Given an agent outside the node when upserting sessions then it rolls back not found",
		func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					{columns: []string{"exists"}, values: [][]driver.Value{{false}}},
				},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()

			err := store.UpsertAgentSessions(
				context.Background(),
				Node{NodeID: "node_1"},
				"agent_1",
				[]SessionStatusInput{{SessionID: "codex:abc"}},
			)

			require.ErrorIs(t, err, ErrNotFound)
			assert.Equal(t, 0, script.execCount)
			assert.False(t, script.committed)
			assert.True(t, script.rolled)
		},
	)

	t.Run(
		"Given an empty session ID when upserting sessions then it rolls back conflict",
		func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
				},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()

			err := store.UpsertAgentSessions(
				context.Background(),
				Node{NodeID: "node_1"},
				"agent_1",
				[]SessionStatusInput{{}},
			)

			require.ErrorIs(t, err, ErrConflict)
			assert.Equal(t, 0, script.execCount)
			assert.False(t, script.committed)
			assert.True(t, script.rolled)
		},
	)

	t.Run(
		"Given activity is reported then the upsert keeps the newest timestamp",
		func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
					{columns: []string{"session_id"}},
				},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			activity := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

			err := store.UpsertAgentSessions(
				context.Background(),
				Node{NodeID: "node_1"},
				"agent_1",
				[]SessionStatusInput{{SessionID: "codex:abc", LastMessageAt: &activity}},
			)

			require.NoError(t, err)
			require.Len(t, script.execTexts, 1)
			assert.Contains(t, script.execTexts[0], "GREATEST(agent_sessions.last_message_at, EXCLUDED.last_message_at)")
		},
	)

	t.Run(
		"Given user prompt activity is reported then the upsert keeps the newest timestamp",
		func(t *testing.T) {
			script := &scriptedPostgresScript{queries: []scriptedRows{
				{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
				{columns: []string{"session_id"}},
			}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			activity := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

			err := store.UpsertAgentSessions(context.Background(), Node{NodeID: "node_1"}, "agent_1", []SessionStatusInput{{
				SessionID: "codex:abc", LastUserMessageAt: &activity,
			}})

			require.NoError(t, err)
			require.Len(t, script.execTexts, 1)
			assert.Contains(t, script.execTexts[0], "GREATEST(agent_sessions.last_user_message_at, EXCLUDED.last_user_message_at)")
		},
	)
}

func TestPostgresStoreUpsertAgentStatus(t *testing.T) {
	t.Run(
		"Given a node-owned agent when reporting sessions then it upserts with the agent node ID",
		func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					{columns: []string{"node_id"}, values: [][]driver.Value{{"node_1"}}},
					{columns: []string{"session_id"}},
				},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()

			err := store.UpsertAgentStatus(context.Background(), AgentStatusReport{
				AgentID:  "agent_1",
				Sessions: []SessionStatusInput{{SessionID: "sess_1", Status: "available"}},
			})

			require.NoError(t, err)
			assert.Equal(t, 1, script.execCount)
			require.Len(t, script.execArgs, 1)
			require.NotEmpty(t, script.execArgs[0])
			assert.Equal(t, "node_1", script.execArgs[0][0].Value)
			assert.True(t, script.committed)
			assert.False(t, script.rolled)
		},
	)

	t.Run(
		"Given a missing agent when reporting status then it rolls back not found",
		func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					{columns: []string{"node_id"}},
				},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()

			err := store.UpsertAgentStatus(context.Background(), AgentStatusReport{
				AgentID: "agent_missing",
			})

			require.ErrorIs(t, err, ErrNotFound)
			assert.Equal(t, 0, script.execCount)
			assert.False(t, script.committed)
			assert.True(t, script.rolled)
		},
	)
}

func scriptedPostgresStore(
	t *testing.T,
	script *scriptedPostgresScript,
) (*PostgresStore, func()) {
	t.Helper()
	scriptedPostgresRegisterOnce.Do(func() {
		sql.Register(scriptedPostgresDriverName, scriptedPostgresDriver{})
	})
	scriptedPostgresMu.Lock()
	scriptedPostgresActive = script
	db, err := sql.Open(scriptedPostgresDriverName, "")
	require.NoError(t, err)
	return &PostgresStore{
			db:  db,
			now: func() time.Time { return time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC) },
		}, func() {
			_ = db.Close()
			scriptedPostgresActive = nil
			scriptedPostgresMu.Unlock()
		}
}

type scriptedPostgresDriver struct{}

func (scriptedPostgresDriver) Open(name string) (driver.Conn, error) {
	if scriptedPostgresActive == nil {
		return nil, errors.New("missing scripted postgres test script")
	}
	return &scriptedPostgresConn{script: scriptedPostgresActive}, nil
}

type scriptedPostgresConn struct {
	script *scriptedPostgresScript
}

func (c *scriptedPostgresConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not implemented")
}

func (c *scriptedPostgresConn) Close() error {
	return nil
}

func (c *scriptedPostgresConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *scriptedPostgresConn) BeginTx(
	ctx context.Context,
	opts driver.TxOptions,
) (driver.Tx, error) {
	return &scriptedPostgresTx{script: c.script}, nil
}

func (c *scriptedPostgresConn) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	c.script.execCount++
	c.script.execTexts = append(c.script.execTexts, query)
	c.script.execArgs = append(c.script.execArgs, append([]driver.NamedValue(nil), args...))
	if strings.Contains(query, "INSERT INTO transport_queue_state") {
		if len(c.script.queueStateInsertResults) == 0 {
			return driver.RowsAffected(0), nil
		}
		affected := c.script.queueStateInsertResults[0]
		c.script.queueStateInsertResults = c.script.queueStateInsertResults[1:]
		return driver.RowsAffected(affected), nil
	}
	if len(c.script.execResults) == 0 {
		return driver.RowsAffected(1), nil
	}
	affected := c.script.execResults[0]
	c.script.execResults = c.script.execResults[1:]
	return driver.RowsAffected(affected), nil
}

func (c *scriptedPostgresConn) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	c.script.queryTexts = append(c.script.queryTexts, query)
	c.script.queryArgs = append(c.script.queryArgs, append([]driver.NamedValue(nil), args...))
	if len(c.script.queries) == 0 {
		return nil, errors.New("unexpected query")
	}
	rows := c.script.queries[0]
	c.script.queries = c.script.queries[1:]
	return &scriptedPostgresRows{rows: rows}, nil
}

type scriptedPostgresTx struct {
	script *scriptedPostgresScript
}

func (tx *scriptedPostgresTx) Commit() error {
	tx.script.committed = true
	return nil
}

func (tx *scriptedPostgresTx) Rollback() error {
	tx.script.rolled = true
	return nil
}

type scriptedPostgresRows struct {
	rows scriptedRows
	idx  int
}

func (r *scriptedPostgresRows) Columns() []string {
	return r.rows.columns
}

func (r *scriptedPostgresRows) Close() error {
	return nil
}

func (r *scriptedPostgresRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows.values) {
		return io.EOF
	}
	copy(dest, r.rows.values[r.idx])
	r.idx++
	return nil
}
