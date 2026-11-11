package storage

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryProgressPostgresGivenManySourcesThenClassifiesEachDisplayOnce(t *testing.T) {
	store := secretaryPostgresFixture(t)
	const sourceCount, displayCount = 128, 24
	_, err := store.db.ExecContext(t.Context(), fmt.Sprintf(`
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type)
SELECT 'progress_'||i,'source','session','test','inbound','user','user_message' FROM generate_series(1,%d) i;
INSERT INTO message_parts(message_id,part_index,part_type,text)
SELECT 'progress_'||i,0,'text','visible body' FROM generate_series(1,%d) i;
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,raw_json)
SELECT 'progress_display_'||i,'source','session','test','inbound','tool','pax:artifact','m1',
jsonb_build_object('replaces_message_ids',(SELECT jsonb_agg('unrelated_'||j) FROM generate_series(1,32) j))
FROM generate_series(1,%d) i;
ANALYZE messages; ANALYZE message_parts`, sourceCount, sourceCount, displayCount))
	require.NoError(t, err)
	var raw []byte
	query := buildSecretaryProgressSQL()
	require.NoError(t, store.db.QueryRowContext(t.Context(),
		`EXPLAIN (ANALYZE,FORMAT JSON) `+query, "owner", secretaryProgressWhitespace).Scan(&raw))
	var plan []map[string]any
	require.NoError(t, json.Unmarshal(raw, &plan))
	loops := replacementArrayExpansionLoops(plan)
	assert.Positive(t, loops)
	assert.LessOrEqual(t, loops, float64(displayCount*2),
		"replacement validation and expansion must scale with display rows, not source rows")
	t.Logf("sources=%d displays=%d replacement_array_loops=%.0f", sourceCount, displayCount, loops)
	page := secretaryProgress(t, store)
	assert.Equal(t, sourceCount+1, page.Counts.TotalMessages)
	assert.Equal(t, sourceCount+1, page.Counts.PendingMessages)
}

func TestSecretaryProgressPostgresGivenReplacementShapesThenPreservesCanonicalCounts(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO messages
(message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,raw_json)
VALUES ('display','source','session','test','inbound','tool','pax:invocation','m1','{}')`)
	require.NoError(t, err)
	for _, kind := range []string{"pax:invocation", "pax:invocation_pending", "pax:artifact"} {
		for _, test := range []struct {
			name   string
			raw    any
			parent string
			hidden bool
		}{
			{"explicit target", `{"replaces_message_ids":["m1"]}`, "other", true},
			{"explicit other overrides parent", `{"replaces_message_ids":["other"]}`, "m1", false},
			{"target and null", `{"replaces_message_ids":[null,"m1"]}`, "other", true},
			{"only null overrides parent", `{"replaces_message_ids":[null]}`, "m1", false},
			{"empty string overrides parent", `{"replaces_message_ids":[""]}`, "m1", false},
			{"duplicates", `{"replaces_message_ids":["m1","m1"]}`, "other", true},
			{"empty array", `{"replaces_message_ids":[]}`, "m1", true},
			{"missing array", `{}`, "m1", true},
			{"null array", `{"replaces_message_ids":null}`, "m1", true},
			{"string array", `{"replaces_message_ids":"other"}`, "m1", true},
			{"object array", `{"replaces_message_ids":{}}`, "m1", true},
			{"number array", `{"replaces_message_ids":1}`, "m1", true},
			{"boolean array", `{"replaces_message_ids":false}`, "m1", true},
			{"invalid member falls back", `{"replaces_message_ids":["other",1]}`, "m1", true},
			{"invalid target list ignores target", `{"replaces_message_ids":["m1",{}]}`, "other", false},
			{"nested array falls back", `{"replaces_message_ids":[[]]}`, "m1", true},
			{"null envelope", `null`, "m1", true},
			{"SQL null envelope", nil, "m1", true},
			{"string envelope", `"other"`, "m1", true},
			{"number envelope", `1`, "m1", true},
			{"array envelope", `[]`, "m1", true},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				_, updateErr := store.db.ExecContext(t.Context(), `UPDATE messages
SET message_type=$1,raw_json=$2,parent_message_id=$3 WHERE message_id='display'`, kind, test.raw, test.parent)
				require.NoError(t, updateErr)
				page := secretaryProgress(t, store)
				if test.hidden {
					assert.Zero(t, page.Counts.TotalSessions)
					assert.Zero(t, page.Counts.TotalMessages)
				} else {
					assert.Equal(t, 1, page.Counts.TotalSessions)
					assert.Equal(t, 1, page.Counts.TotalMessages)
					assert.Equal(t, 1, page.Counts.CompletedMessages)
				}
				rows, queryErr := store.db.QueryContext(
					t.Context(),
					buildSecretaryProgressScopeSQL(
						"",
						"",
						searchMessageVisibilitySQL,
					),
					"owner",
					secretaryProgressWhitespace,
				)
				require.NoError(t, queryErr)
				legacy, scanErr := scanSecretaryProgress(rows, "owner")
				require.NoError(t, scanErr)
				expected := legacy.page("owner", "", 20, time.Time{})
				page.AsOf = time.Time{}
				assert.Equal(t, expected, page)
			})
		}
	}
}

func TestSecretaryProgressPostgresGivenForeignDisplaysThenOnlyMatchingSourceSessionIsHidden(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO agent_sessions(agent_id,session_id) VALUES ('source','other_session'),('other','session');
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,raw_json)
VALUES ('display','source','session','test','inbound','tool','pax:artifact','{"replaces_message_ids":["m1"]}')`)
	require.NoError(t, err)
	for _, test := range []struct {
		name, agent string
		session     any
		hidden      bool
	}{
		{"matching session", "source", "session", true},
		{"foreign session", "source", "other_session", false},
		{"foreign agent", "other", "session", false},
		{"null session", "source", nil, false},
		{"empty session", "source", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, updateErr := store.db.ExecContext(
				t.Context(),
				`UPDATE messages SET agent_id=$1,session_id=$2 WHERE message_id='display'`,
				test.agent,
				test.session,
			)
			require.NoError(t, updateErr)
			page, readErr := store.ReadSecretaryProgress(
				t.Context(),
				"owner",
				domain.SecretaryProgressQuery{},
			)
			require.NoError(t, readErr)
			assert.Equal(t, test.hidden, page.Counts.TotalMessages == 0)
		})
	}
}
