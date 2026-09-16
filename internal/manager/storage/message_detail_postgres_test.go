package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// This test uses the real pgx extended query protocol. Scripted database tests
// cannot detect PostgreSQL selecting the text/regex overload of substring.
func TestPostgresMessageDetailParameterTypesAndPages(t *testing.T) {
	dsn := os.Getenv("PAX_MANAGER_MESSAGE_DETAIL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip(
			"set PAX_MANAGER_MESSAGE_DETAIL_TEST_DATABASE_URL to an isolated PostgreSQL database",
		)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.ExecContext(ctx, `
 CREATE TEMP TABLE agent_sessions (
  agent_id text, session_id text, native_id text, updated_at timestamptz
 );
 CREATE TEMP TABLE messages (
  agent_id text, session_id text, message_id text, updated_at timestamptz, raw_json jsonb
 );
 CREATE TEMP TABLE message_parts (
  message_id text, part_index integer, part_type text, text text, updated_at timestamptz
 );
 INSERT INTO agent_sessions VALUES ('agent', 'session', 'native', now());
 INSERT INTO messages VALUES ('agent', 'native', 'message', now(),
  '{"params":{"update":{"rawInput":{"command":"pwd"},"rawOutput":"hello"}}}');
 `)
	require.NoError(t, err)
	store := NewPostgresStore(db, time.Now)
	for _, section := range []string{"input", "output"} {
		t.Run(section, func(t *testing.T) {
			offset := 0
			var content string
			var revision string
			for {
				page, err := store.GetMessageDetailPage(
					ctx,
					"agent",
					"session",
					"message",
					section,
					offset,
					3,
				)
				require.NoError(t, err)
				require.Equal(t, "json", page.Format)
				require.LessOrEqual(t, len([]rune(page.Text)), 3)
				content += page.Text
				if revision != "" {
					require.Equal(t, revision, page.Revision)
				}
				revision = page.Revision
				if !page.HasMore {
					break
				}
				require.Greater(t, page.NextOffset, offset)
				offset = page.NextOffset
			}
			if section == "input" {
				require.JSONEq(t, `{"command":"pwd"}`, content)
			} else {
				require.JSONEq(t, `"hello"`, content)
			}
		})
	}
	terminal := "ab\u4f60\U0001f600cd"
	_, err = db.ExecContext(ctx, `INSERT INTO message_parts VALUES
 ('message', 0, 'text', $1, now()), ('message', 1, 'text', $2, now())`,
		string([]rune(terminal)[:3]), string([]rune(terminal)[3:]))
	require.NoError(t, err)
	page, err := store.GetMessageDetailPage(ctx, "agent", "session", "message", "output", 2, 3)
	require.NoError(t, err)
	require.Equal(t, "text", page.Format)
	require.Equal(t, string([]rune(terminal)[2:5]), page.Text)
	require.True(t, page.HasMore)
	require.Equal(t, 5, page.NextOffset)
	page, err = store.GetMessageDetailPage(ctx, "agent", "session", "message", "output", 100, 3)
	require.NoError(t, err)
	require.Empty(t, page.Text)
	require.False(t, page.HasMore)
	require.Equal(t, 100, page.NextOffset)
	_, err = store.GetMessageDetailPage(ctx, "other-agent", "session", "message", "output", 0, 3)
	require.ErrorIs(t, err, domain.ErrNotFound)
	_, err = store.GetMessageDetailPage(ctx, "agent", "other-session", "message", "output", 0, 3)
	require.ErrorIs(t, err, domain.ErrNotFound)
	// Keep a missing input as valid JSON null rather than a SQL NULL scan error.
	_, err = db.ExecContext(ctx, `UPDATE messages SET raw_json = '{}'::jsonb`)
	require.NoError(t, err)
	page, err = store.GetMessageDetailPage(ctx, "agent", "session", "message", "input", 0, 10)
	require.NoError(t, err)
	require.True(t, json.Valid([]byte(page.Text)))
	require.Equal(t, "null", page.Text)
}
