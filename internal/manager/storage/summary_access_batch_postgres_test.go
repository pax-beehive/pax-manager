package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryAccessPostgresGivenCurrentVisibilityChangesThenMatchesCanonicalRead(t *testing.T) {
	for _, test := range []struct {
		name, change string
	}{
		{"archived session", `UPDATE agent_sessions SET archived_at=now()`},
		{"deleted agent", `UPDATE agents SET deleted_at=now()`},
		{"foreign owner", `UPDATE agents SET owner_user_id='other'`},
		{"deleted agent node", `INSERT INTO nodes VALUES ('n',now()); UPDATE agents SET node_id='n'`},
		{"deleted message node", `INSERT INTO nodes VALUES ('n',now()); UPDATE messages SET node_id='n'`},
		{"deleted session node", `INSERT INTO nodes VALUES ('n',now()); UPDATE agent_sessions SET node_id='n'`},
		{"internal session", `UPDATE agent_sessions SET source='secretary_internal'`},
		{"internal session prefix", `UPDATE agent_sessions SET session_id='sess_secretary_hidden'; UPDATE messages SET session_id='sess_secretary_hidden'`},
		{"missing session", `DELETE FROM agent_sessions`},
		{"hidden analysis", `UPDATE messages SET raw_json='{"params":{"update":{"channel":"analysis"}}}'`},
		{"hidden invalid envelope", `UPDATE messages SET raw_json='[]'`},
		{"unsupported role", `UPDATE messages SET role='tool'`},
		{"unsupported message type", `UPDATE messages SET message_type='reasoning'`},
		{"blank canonical text", `UPDATE message_parts SET text=U&'\3000\00a0'`},
		{"negative part index", `UPDATE message_parts SET part_index=-1`},
		{"duplicate part index", `INSERT INTO message_parts VALUES ('message_1',0,'text','duplicate')`},
		{"display replacement", `INSERT INTO messages(message_id,agent_id,session_id,role,message_type,parent_message_id,created_at)
VALUES ('display','agent_1','session_1','assistant','pax:invocation','message_1',now())`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := summaryPostgresFixture(t)
			request := summaryTestRequest("old body")
			_, err := store.PublishSummary(t.Context(), "owner_1", request)
			require.NoError(t, err)
			_, err = store.db.ExecContext(t.Context(), test.change)
			require.NoError(t, err)
			_, canonicalErr := store.ReadSearchDocument(
				t.Context(),
				"owner_1",
				"message",
				"message_1",
			)
			assert.ErrorIs(t, canonicalErr, domain.ErrNotFound)
			_, err = store.GetSummary(t.Context(), "owner_1", request.SummaryID, 1)
			assert.ErrorIs(t, err, domain.ErrNotFound)
			page, err := store.ListRecentSummaries(
				t.Context(),
				"owner_1",
				domain.SummaryListQuery{},
			)
			require.NoError(t, err)
			assert.Empty(t, page.Items)
		})
	}
}

func TestSummaryAccessPostgresGivenTwentySummariesAndLargeCoverageThenPageCompletesWithinDeadline(
	t *testing.T,
) {
	store := summaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO messages(message_id,agent_id,session_id,role,session_seq,created_at)
SELECT 'bulk_'||i,'agent_1','session_1','user',i+2,now() FROM generate_series(1,1280) i;
INSERT INTO message_parts(message_id,part_index,part_type,text)
SELECT 'bulk_'||i,0,'text',repeat('canonical source ',1024) FROM generate_series(1,1280) i;
CREATE INDEX summary_test_parts_message_idx ON message_parts(message_id);
INSERT INTO summary_versions(summary_id,revision,owner_user_id,subject_type,subject_id,title,body,request_id,request_hash,created_at)
SELECT 'bulk_summary_'||i,1,'owner_1','fragment','subject_'||i,'Result '||i,'Summary body','request_'||i,'hash',now()
FROM generate_series(1,20) i;
INSERT INTO message_snapshots(message_id,canonicalization_version,content_hash,owner_user_id,agent_id,session_id,body,created_at,message_created_at)
SELECT 'bulk_'||i,'visible-text-v1','sha256:'||encode(sha256(convert_to(repeat('canonical source ',1024),'UTF8')),'hex'),
'owner_1','agent_1','session_1',repeat('canonical source ',1024),now(),now() FROM generate_series(1,1280) i;
INSERT INTO summary_sources(summary_id,revision,source_order,source_kind,message_id,canonicalization_version,content_hash)
SELECT 'bulk_summary_'||((i-1)/64+1),1,(i-1)%64,'message','bulk_'||i,'visible-text-v1',
'sha256:'||encode(sha256(convert_to(repeat('canonical source ',1024),'UTF8')),'hex')
FROM generate_series(1,1280) i;`)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	started := time.Now()
	page, err := store.ListRecentSummaries(ctx, "owner_1", domain.SummaryListQuery{})
	elapsed := time.Since(started)
	require.NoError(t, err)
	require.Len(t, page.Items, 20)
	for _, item := range page.Items {
		assert.Equal(t, 64, item.CoverageCount)
	}
	assert.Less(t, elapsed, 8*time.Second)
	t.Logf("20 summaries, 1280 current source messages, about 21 MiB canonical text: %s", elapsed)
}
