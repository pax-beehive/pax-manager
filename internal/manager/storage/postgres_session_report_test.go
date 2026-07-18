package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
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
	queries     []scriptedRows
	queryTexts  []string
	execResults []int64
	execCount   int
	committed   bool
	rolled      bool
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
