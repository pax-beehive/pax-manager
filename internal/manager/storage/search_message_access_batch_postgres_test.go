package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func validateMessageAccessBatch(t *testing.T, store *PostgresStore, ids ...string) error {
	t.Helper()
	tx, err := store.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	return validateSearchMessageAccess(t.Context(), tx, "owner_1", ids)
}

func capturedMessageAccessQuery(t *testing.T) string {
	t.Helper()
	script := &scriptedPostgresScript{queries: []scriptedRows{{
		columns: []string{"message", "owner", "parts", "revision"},
	}}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	assert.ErrorIs(t, validateMessageAccessBatch(t, store, "missing"), domain.ErrNotFound)
	require.Len(t, script.queryTexts, 1)
	return script.queryTexts[0]
}

func TestSearchMessageAccessPostgresGivenReplacementShapesThenMatchesSingleRead(t *testing.T) {
	store := summaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO messages
(message_id,agent_id,session_id,role,message_type,parent_message_id,raw_json)
VALUES ('display','agent_1','session_1','tool','pax:invocation','message_1','{}')`)
	require.NoError(t, err)
	for _, kind := range []string{"pax:invocation", "pax:invocation_pending", "pax:artifact"} {
		for _, test := range []struct {
			name   string
			raw    any
			parent string
			hidden bool
		}{
			{"explicit target", `{"replaces_message_ids":["message_1"]}`, "other", true},
			{"explicit other overrides parent", `{"replaces_message_ids":["other"]}`, "message_1", false},
			{"target and null", `{"replaces_message_ids":[null,"message_1"]}`, "other", true},
			{"only null overrides parent", `{"replaces_message_ids":[null]}`, "message_1", false},
			{"empty string overrides parent", `{"replaces_message_ids":[""]}`, "message_1", false},
			{"duplicates", `{"replaces_message_ids":["message_1","message_1"]}`, "other", true},
			{"empty array", `{"replaces_message_ids":[]}`, "message_1", true},
			{"missing array", `{}`, "message_1", true},
			{"null array", `{"replaces_message_ids":null}`, "message_1", true},
			{"string array", `{"replaces_message_ids":"other"}`, "message_1", true},
			{"object array", `{"replaces_message_ids":{}}`, "message_1", true},
			{"number array", `{"replaces_message_ids":1}`, "message_1", true},
			{"boolean array", `{"replaces_message_ids":false}`, "message_1", true},
			{"invalid member falls back", `{"replaces_message_ids":["other",1]}`, "message_1", true},
			{"invalid target list ignores target", `{"replaces_message_ids":["message_1",{}]}`, "other", false},
			{"nested array falls back", `{"replaces_message_ids":[[]]}`, "message_1", true},
			{"null envelope", `null`, "message_1", true},
			{"SQL null envelope", nil, "message_1", true},
			{"string envelope", `"other"`, "message_1", true},
			{"number envelope", `1`, "message_1", true},
			{"array envelope", `[]`, "message_1", true},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				_, updateErr := store.db.ExecContext(t.Context(), `UPDATE messages
SET message_type=$1,raw_json=$2,parent_message_id=$3 WHERE message_id='display'`, kind, test.raw, test.parent)
				require.NoError(t, updateErr)
				_, singleErr := store.ReadSearchDocument(
					t.Context(),
					"owner_1",
					"message",
					"message_1",
				)
				batchErr := validateMessageAccessBatch(t, store, "message_1", "message_1")
				if test.hidden {
					assert.ErrorIs(t, singleErr, domain.ErrNotFound)
					assert.ErrorIs(t, batchErr, domain.ErrNotFound)
				} else {
					assert.NoError(t, singleErr)
					assert.NoError(t, batchErr)
				}
			})
		}
	}
}

func TestSearchMessageAccessPostgresGivenNullableSessionsThenScopesReplacementsExactly(
	t *testing.T,
) {
	store := summaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO agents(agent_id,owner_user_id) VALUES ('other_agent','owner_1');
INSERT INTO messages(message_id,agent_id,session_id,role,message_type)
VALUES ('null_message','agent_1',NULL,'user','user_message'),
('empty_message','agent_1','','user','user_message');
INSERT INTO message_parts VALUES ('null_message',0,'text','null session body'),('empty_message',0,'text','empty session body');
INSERT INTO messages(message_id,agent_id,session_id,role,message_type,raw_json)
VALUES ('display','agent_1',NULL,'tool','pax:artifact','{"replaces_message_ids":["message_1","null_message","empty_message"]}')`)
	require.NoError(t, err)
	for _, test := range []struct {
		name, agent string
		session     any
		hidden      string
	}{
		{"null session", "agent_1", nil, "null_message"},
		{"empty session", "agent_1", "", "empty_message"},
		{"normal session", "agent_1", "session_1", "message_1"},
		{"foreign session", "agent_1", "other_session", ""},
		{"foreign agent", "other_agent", "session_1", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, updateErr := store.db.ExecContext(
				t.Context(),
				`UPDATE messages SET agent_id=$1,session_id=$2
WHERE message_id='display'`,
				test.agent,
				test.session,
			)
			require.NoError(t, updateErr)
			for _, id := range []string{"message_1", "null_message", "empty_message"} {
				_, singleErr := store.ReadSearchDocument(t.Context(), "owner_1", "message", id)
				batchErr := validateMessageAccessBatch(t, store, id)
				if id == test.hidden {
					assert.ErrorIs(t, singleErr, domain.ErrNotFound)
					assert.ErrorIs(t, batchErr, domain.ErrNotFound)
				} else {
					assert.NoError(t, singleErr)
					assert.NoError(t, batchErr)
				}
			}
			assert.Equal(t, test.hidden != "", validateMessageAccessBatch(
				t, store, "message_1", "null_message", "empty_message",
			) != nil)
		})
	}
}

func TestSearchMessageAccessPostgresGivenManySourcesThenClassifiesEachDisplayOnce(t *testing.T) {
	query := capturedMessageAccessQuery(t)
	store := summaryPostgresFixture(t)
	const sourceCount, displayCount = 128, 24
	_, err := store.db.ExecContext(t.Context(), fmt.Sprintf(`
CREATE INDEX batch_access_parts_message_idx ON message_parts(message_id,part_index);
CREATE INDEX batch_access_display_session_idx ON messages(agent_id,session_id)
WHERE message_type IN ('pax:invocation','pax:invocation_pending','pax:artifact');
INSERT INTO messages(message_id,agent_id,session_id,role,message_type)
SELECT 'batch_'||i,'agent_1','session_1','user','user_message' FROM generate_series(1,%d) i;
INSERT INTO message_parts(message_id,part_index,part_type,text)
SELECT 'batch_'||i,0,'text','visible body' FROM generate_series(1,%d) i`, sourceCount, sourceCount))
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), fmt.Sprintf(`INSERT INTO messages
(message_id,agent_id,session_id,role,message_type,parent_message_id,raw_json)
SELECT 'batch_display_'||i,'agent_1','session_1','tool','pax:artifact','batch_1',
jsonb_build_object('replaces_message_ids',(SELECT jsonb_agg('unrelated_'||j) FROM generate_series(1,32) j))
FROM generate_series(1,%d) i;
ANALYZE messages; ANALYZE message_parts`, displayCount))
	require.NoError(t, err)
	ids := make([]string, sourceCount)
	for i := range ids {
		ids[i] = fmt.Sprintf("batch_%d", i+1)
	}
	encoded, err := json.Marshal(ids)
	require.NoError(t, err)
	var raw []byte
	require.NoError(t, store.db.QueryRowContext(t.Context(),
		`EXPLAIN (ANALYZE,FORMAT JSON) `+query, string(encoded)).Scan(&raw))
	var plan []map[string]any
	require.NoError(t, json.Unmarshal(raw, &plan))
	loops := replacementArrayExpansionLoops(plan)
	assert.Positive(t, loops)
	assert.LessOrEqual(t, loops, float64(displayCount*2),
		"replacement validation and expansion must scale with display rows, not source rows")
	t.Logf("sources=%d displays=%d replacement_array_loops=%.0f", sourceCount, displayCount, loops)
	assert.NoError(t, validateMessageAccessBatch(t, store, ids...))
}

func replacementArrayExpansionLoops(value any) float64 {
	var loops float64
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			loops += replacementArrayExpansionLoops(child)
		}
	case []map[string]any:
		for _, child := range node {
			loops += replacementArrayExpansionLoops(child)
		}
	case map[string]any:
		if node["Function Name"] == "jsonb_array_elements" {
			loops, _ = node["Actual Loops"].(float64)
		}
		for _, child := range node {
			loops += replacementArrayExpansionLoops(child)
		}
	}
	return loops
}
