package storage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryPendingPostgresGivenProcessedHistoryThenHashesOnlyDirtyCandidates(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 SELECT 'history-'||i,'source','session','test','inbound','assistant','chat',i+1
 FROM generate_series(1,2048) i;
 INSERT INTO message_parts(message_id,part_index,part_type,text)
 SELECT 'history-'||i,0,'text','Processed historical body' FROM generate_series(1,2048) i;`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `UPDATE secretary_message_state
 SET processed_hash=$1,processed_canonicalization_version=$2,
 processed_owner_user_id='owner' WHERE message_id LIKE 'history-%'`,
		domain.SearchContentHash("Processed historical body"), domain.SearchCanonicalizationVersion)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_jobs SET processed_revision=desired_revision;
 UPDATE messages SET updated_at=NOW() WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	var raw []byte
	require.NoError(t, store.db.QueryRowContext(t.Context(),
		`EXPLAIN (ANALYZE,FORMAT JSON) `+secretaryPendingSQL,
		"owner", secretaryProgressWhitespace, 1).Scan(&raw))
	var plans []struct{ Plan secretaryPendingPlan }
	require.NoError(t, json.Unmarshal(raw, &plans))
	require.Len(t, plans, 1)
	rows, found := plans[0].Plan.rowsFor("CTE current_messages")
	require.True(t, found)
	assert.Equal(t, 1, rows, "unchanged history is outside the canonical hashing projection")
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Zero(t, status.PendingCount)
}

type secretaryPendingPlan struct {
	Subplan string                 `json:"Subplan Name"`
	Rows    int                    `json:"Actual Rows"`
	Plans   []secretaryPendingPlan `json:"Plans"`
}

func (plan secretaryPendingPlan) rowsFor(subplan string) (int, bool) {
	if plan.Subplan == subplan {
		return plan.Rows, true
	}
	for _, child := range plan.Plans {
		if rows, found := child.rowsFor(subplan); found {
			return rows, true
		}
	}
	return 0, false
}
