package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryProgressPostgresGivenLateDisabledPartsThenReportsExactCurrentCoverage(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE messages SET turn_id='turn-1' WHERE message_id='m1';
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq,turn_id) VALUES ('m2','source','session','test','inbound','user','chat',2,'turn-1');
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m2',0,'text','second');`,
	)
	require.NoError(t, err)
	initial := secretaryProgress(t, store)
	assert.Equal(t, 2, initial.Counts.PendingMessages)
	assert.Equal(t, 1, initial.Counts.PendingTurns)
	cfg := secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	_, err = store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
	require.NoError(t, err)
	complete := secretaryProgress(t, store)
	assert.Equal(t, 1, complete.Counts.CompletedSessions)
	assert.Equal(t, 1, complete.Counts.CompletedTurns)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq,turn_id) VALUES ('m3','source','session','test','inbound','assistant','chat',3,'turn-2');
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m3',0,'text','new turn');`,
	)
	require.NoError(t, err)
	partial := secretaryProgress(t, store)
	assert.Equal(t, 1, partial.Counts.PartialSessions)
	assert.Equal(t, 1, partial.Counts.PendingTurns)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "secretary", ExpectedRevision: cfg.Revision},
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m1',1,'text',' late');`,
	)
	require.NoError(t, err)
	changed := secretaryProgress(t, store)
	assert.Equal(t, 1, changed.Counts.CompletedMessages)
	assert.Equal(t, 2, changed.Counts.PendingMessages)
	assert.Equal(t, 2, changed.Counts.PendingTurns)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text=CASE part_index WHEN 0 THEN 'orig' ELSE 'inal' END WHERE message_id='m1';`,
	)
	require.NoError(t, err)
	repartitioned := secretaryProgress(t, store)
	assert.Equal(t, 2, repartitioned.Counts.CompletedMessages)
	assert.Equal(t, 1, repartitioned.Counts.CompletedTurns)
	assert.Equal(t, 1, repartitioned.Counts.PendingTurns)
	_, err = store.db.ExecContext(
		t.Context(),
		`DELETE FROM messages WHERE message_id='m3'; UPDATE messages SET turn_id=NULL WHERE message_id='m2';`,
	)
	require.NoError(t, err)
	current := secretaryProgress(t, store)
	assert.Equal(t, 1, current.Counts.CompletedSessions)
	assert.Equal(t, 1, current.Counts.TotalTurns)
	assert.Equal(t, 1, current.Counts.UngroupedMessages)
	assert.NotNil(t, current.Sessions[0].LastCompletedAt)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agents SET owner_user_id='other' WHERE agent_id='source';`,
	)
	require.NoError(t, err)
	foreign, err := store.ReadSecretaryProgress(
		t.Context(),
		"other",
		domain.SecretaryProgressQuery{},
	)
	require.NoError(t, err)
	assert.Equal(t, 2, foreign.Counts.PendingMessages)
	assert.Equal(t, 1, foreign.Counts.UnstartedSessions)
	assert.Nil(t, foreign.Sessions[0].LastCompletedAt)
	assert.Zero(t, secretaryProgress(t, store).Counts.TotalSessions)
}

func TestSecretaryProgressPostgresGivenHiddenSourcesThenExcludesThemFromProgress(t *testing.T) {
	for _, change := range []string{
		`UPDATE agent_sessions SET archived_at=NOW()`,
		`UPDATE agent_sessions SET source='secretary_internal'`,
		`UPDATE agent_sessions SET session_id='sess_secretary_hidden'; UPDATE messages SET session_id='sess_secretary_hidden'`,
		`INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES ('source','session','owner')`,
		`UPDATE agents SET deleted_at=NOW() WHERE agent_id='source'`,
		`UPDATE messages SET role='tool'`,
		`UPDATE messages SET raw_json='{"channel":"analysis"}'`,
		`DELETE FROM message_parts`,
		`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,session_seq) VALUES ('replacement','source','session','test','inbound','assistant','pax:invocation','m1',2)`,
	} {
		t.Run(change, func(t *testing.T) {
			store := secretaryPostgresFixture(t)
			_, err := store.db.ExecContext(t.Context(), change)
			require.NoError(t, err)
			page := secretaryProgress(t, store)
			assert.Zero(t, page.Counts.TotalSessions)
			assert.Empty(t, page.Sessions)
		})
	}
}
