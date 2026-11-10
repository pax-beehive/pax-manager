package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPostgresMessageSummaryGivenCodexActivityThenMatchesMemoryProjection(t *testing.T) {
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
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.ExecContext(ctx, `CREATE TEMP TABLE messages (
	 id bigint, message_id text, conversation_id text, owner_user_id text, node_id text,
	 agent_id text, session_id text, source text, direction text, role text, status text,
	 message_type text, parent_message_id text, turn_id text, response_id text, logical_key text,
	 raw_json jsonb, session_seq bigint, conversation_seq bigint,
	 created_at timestamptz, updated_at timestamptz
	)`)
	require.NoError(t, err)
	frames := []string{
		`{"params":{"update":{"rawInput":{"activityKind":" \tstarted\n",
			"agentThreadId":" child \r","agentPath":" /root/child "}}}}`,
		`{"raw_input":{"activityKind":"completed","agentThreadId":"child"}}`,
		`{"rawInput":{"activityKind":"interacted","agentThreadId":"child"}}`,
		`{"rawInput":{"activityKind":"interrupted","agentThreadId":"child"}}`,
		`{"rawInput":{"activityKind":"\u00a0started\u3000","agentThreadId":"\u0085child\u2000",
			"agentPath":"\u2001/root/child\u202f"}}`,
		`{"rawInput":{"activityKind":"running","agentThreadId":"child"}}`,
		`{"rawInput":{"activityKind":"started","agentThreadId":42}}`,
		`{"rawInput":{"activityKind":"started","agentThreadId":" \t\n"}}`,
		`{"rawInput":{"activityKind":true,"agentThreadId":"child"}}`,
		`{"rawInput":{"activityKind":"completed","agentThreadId":"child","agentPath":42}}`,
		`{"rawInput":null,"raw_input":{"activityKind":"started","agentThreadId":"child"}}`,
		`{"rawInput":{"activityKind":"started","agentThreadId":"first"},
			"raw_input":{"activityKind":"completed","agentThreadId":"second"}}`,
		`{"rawInput":{}}`,
		`{}`,
	}
	for _, frame := range frames {
		t.Run(frame, func(t *testing.T) {
			_, err = db.ExecContext(ctx, `TRUNCATE messages;
			 INSERT INTO messages(id,message_id,agent_id,source,direction,message_type,created_at,updated_at)
			 VALUES (1,'message','agent','acp_tunnel','agent_to_user','tool_call_update',now(),now())`)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE messages SET raw_json=$1::jsonb`, frame)
			require.NoError(t, err)
			message, scanErr := scanMessage(db.QueryRowContext(ctx,
				`SELECT `+messageSummaryReturningSQL+` FROM messages`))
			require.NoError(t, scanErr)
			var summary domain.MessageToolSummary
			require.NoError(t, json.Unmarshal(message.RawJSON, &summary))
			assert.Equal(t, toolSummaryFromUpdate(toolUpdate(json.RawMessage(frame))), summary)
		})
	}
}
