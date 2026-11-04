package storage

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryNodesPostgresGivenMultipleOutputsThenCommitsNodesSourcesAndSingleCheckpoint(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	publication := secretaryNodesPublication(job, "search", "deploy")
	results, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	require.Len(t, results, 2)
	var nodes, sourceRows, processed int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM knowledge_nodes WHERE type='summary' AND origin_source_id=$1`, job.SourceID).
			Scan(&nodes),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM knowledge_node_sources`).
			Scan(&sourceRows),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_message_state WHERE processed_hash<>''`).
			Scan(&processed),
	)
	assert.Equal(t, 2, nodes)
	assert.Equal(t, 2, sourceRows)
	assert.Equal(t, 1, processed)
	replayed, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	assert.Equal(t, results, replayed)
	page, err := store.ListSecretaryNodeSummaries(
		t.Context(),
		"owner",
		job.SourceID,
		domain.SummaryListQuery{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "summary", page.Items[0].NodeType)
	assert.Equal(t, "session", page.Items[0].OriginSessionID)
	require.NotEmpty(t, page.NextCursor)
	page, err = store.ListSecretaryNodeSummaries(
		t.Context(),
		"owner",
		job.SourceID,
		domain.SummaryListQuery{Limit: 1, Cursor: page.NextCursor},
	)
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	assert.Empty(t, page.NextCursor)
	_, err = store.ListSecretaryNodeSummaries(
		t.Context(),
		"other",
		job.SourceID,
		domain.SummaryListQuery{},
	)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSecretaryNodesPostgresGivenInvalidSecondOutputThenRollsBackFirstOutputAndProgress(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	publication := secretaryNodesPublication(job, "one", "two")
	publication.Outputs[1].Request.Claims = []domain.SummaryClaim{
		{ID: "bad", Text: "wrong", Evidence: []domain.SummaryEvidence{
			{Message: publication.Outputs[1].Request.Coverage[0], Quote: "absent quote"},
		}},
	}
	_, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	assert.Error(t, err)
	var nodes, versions, snapshots, processed int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM knowledge_nodes`).Scan(&nodes),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM summary_versions`).
			Scan(&versions),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM message_snapshots`).
			Scan(&snapshots),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_message_state WHERE processed_hash<>''`).
			Scan(&processed),
	)
	assert.Zero(t, nodes)
	assert.Zero(t, versions)
	assert.Zero(t, snapshots)
	assert.Zero(t, processed)
}

func TestSecretaryNodesPostgresGivenNoSummaryThenDoesNotReplayHistoryAfterBootstrap(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	publication := domain.SecretaryBatchPublication{
		RequestID: job.BatchID,
		Outcomes: []domain.SecretaryMessageOutcome{
			{MessageID: "m1", Action: "no_summary", Reason: "No new durable information"},
		},
	}
	results, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	assert.Empty(t, results)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Zero(t, status.PendingCount)
	assert.NotNil(t, status.LastCompletedAt)
}

func TestKnowledgeNodesPostgresGivenExistingVersionsThenMigrationPreservesIdentityAndProgress(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	legacy, err := store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
	require.NoError(t, err)
	var checkpointBefore int64
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT processed_revision FROM secretary_jobs WHERE source_id=$1`, job.SourceID).
			Scan(&checkpointBefore),
	)
	_, err = store.db.ExecContext(
		t.Context(),
		`DROP TRIGGER summary_versions_node ON summary_versions;
 DROP TRIGGER summary_sources_directory ON summary_sources;
 DROP TABLE knowledge_node_sources; DROP TABLE knowledge_node_invalidations; DROP TABLE knowledge_nodes; DROP TABLE knowledge_node_migrations;`,
	)
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "summaries.sql"))
	require.NoError(t, err)
	for range 2 {
		_, err = store.db.ExecContext(t.Context(), string(raw))
		require.NoError(t, err)
	}
	var id, owner, nodeType, origin string
	var revision, checkpointAfter int64
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT id,owner_user_id,type,current_revision,origin_source_id FROM knowledge_nodes`).
			Scan(&id, &owner, &nodeType, &revision, &origin),
	)
	assert.Equal(t, legacy.SummaryID, id)
	assert.Equal(t, legacy.Revision, revision)
	assert.Equal(t, "owner", owner)
	assert.Equal(t, "summary", nodeType)
	assert.Equal(t, job.SourceID, origin)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT processed_revision FROM secretary_jobs WHERE source_id=$1`, job.SourceID).
			Scan(&checkpointAfter),
	)
	assert.Equal(t, checkpointBefore, checkpointAfter)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO knowledge_nodes(id,owner_user_id,type,current_revision,created_at,updated_at)
 VALUES('topic','owner','topic',1,NOW(),NOW())`,
	)
	assert.Error(t, err)
}

func TestSecretaryNodesPostgresGivenExternalSourceEditThenOriginGetsSafeRevisionTask(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id) VALUES('secretary','related');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,status,session_seq)
 VALUES('external','secretary','related','test','inbound','user','chat','complete',1);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('external',0,'text','External fact');`,
	)
	require.NoError(t, err)
	ref := summaryTestRef("external", "External fact")
	job.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
	publication := secretaryNodesPublication(job, "one")
	publication.Outputs[0].Request.Coverage = append(publication.Outputs[0].Request.Coverage, ref)
	versions, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='A corrected external fact' WHERE message_id='external';
 SELECT pax_knowledge_invalidate_source('external');
 UPDATE secretary_jobs SET available_at='infinity' WHERE source_session_id='related';`,
	)
	require.NoError(t, err)
	stale, err := store.GetSummary(t.Context(), "owner", versions[0].SummaryID, 0)
	require.NoError(t, err)
	assert.Equal(t, "stale", stale.NodeStatus)
	revision := secretaryNodesReadyJob(t, store)
	assert.Empty(t, revision.Messages)
	assert.Empty(t, revision.CurrentSummaries)
	require.Equal(
		t,
		[]domain.SummaryRef{{SummaryID: versions[0].SummaryID, Revision: 1}},
		revision.InvalidatedSummaries,
	)
	request := domain.PublishSummaryRequest{SummaryID: versions[0].SummaryID, ExpectedRevision: 1,
		Title: "Unavailable summary", Body: domain.SecretaryEmptySummaryBody}
	results, err := store.PublishSecretaryBatch(
		t.Context(),
		revision,
		domain.SecretaryBatchPublication{RequestID: revision.BatchID,
			Outputs: []domain.SecretarySummaryOutput{{Request: request}}},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "empty", results[0].NodeStatus)
	historical, err := store.GetSummary(t.Context(), "owner", versions[0].SummaryID, 1)
	require.NoError(t, err)
	assert.Equal(t, "active", historical.NodeStatus)
	var consumed, pending int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_message_state WHERE message_id='external' AND processed_hash<>''`).
			Scan(&consumed),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM knowledge_node_invalidations`).
			Scan(&pending),
	)
	assert.Zero(t, consumed)
	assert.Zero(t, pending)
}

func TestSecretaryNodesPostgresGivenUnchangedSourceInvalidationThenClearsStaleWithoutCallingAgent(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	versions, err := store.PublishSecretaryBatch(
		t.Context(),
		job,
		secretaryNodesPublication(job, "one"),
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `SELECT pax_knowledge_invalidate_source('m1')`)
	require.NoError(t, err)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	current, err := store.GetSummary(t.Context(), "owner", versions[0].SummaryID, 0)
	require.NoError(t, err)
	assert.Equal(t, "active", current.NodeStatus)
	var attempts int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_attempts`).
			Scan(&attempts),
	)
	assert.Equal(t, 1, attempts)
}

func TestSummaryRevisionsPostgresGivenSeveralVersionsThenPagesNewestFirstAndChecksSourceAccess(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	request := secretaryPublication(job)
	for revision := range 3 {
		request.ExpectedRevision = int64(revision)
		request.RequestID = "history-" + strconv.Itoa(revision)
		_, err := store.PublishSummary(t.Context(), "owner", request)
		require.NoError(t, err)
	}
	page, err := store.ListSummaryRevisions(
		t.Context(),
		"owner",
		request.SummaryID,
		domain.SummaryListQuery{Limit: 2},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.EqualValues(t, 3, page.Items[0].Revision)
	assert.EqualValues(t, 2, page.Items[1].Revision)
	require.NotEmpty(t, page.NextCursor)
	last, err := store.ListSummaryRevisions(
		t.Context(),
		"owner",
		request.SummaryID,
		domain.SummaryListQuery{Limit: 2, Cursor: page.NextCursor},
	)
	require.NoError(t, err)
	require.Len(t, last.Items, 1)
	assert.EqualValues(t, 1, last.Items[0].Revision)
	assert.Empty(t, last.NextCursor)
	_, err = store.ListSummaryRevisions(
		t.Context(),
		"owner",
		"another",
		domain.SummaryListQuery{Cursor: page.NextCursor},
	)
	assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	foreign, err := store.ListSummaryRevisions(
		t.Context(),
		"other",
		request.SummaryID,
		domain.SummaryListQuery{},
	)
	require.NoError(t, err)
	assert.Empty(t, foreign.Items)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
	require.NoError(t, err)
	hidden, err := store.ListSummaryRevisions(
		t.Context(),
		"owner",
		request.SummaryID,
		domain.SummaryListQuery{},
	)
	require.NoError(t, err)
	assert.Empty(t, hidden.Items)
}

func TestSecretaryNodesPostgresGivenUnfinishedRelatedSourceThenRejectsWithoutPublishing(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id) VALUES('secretary','related');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,status,session_seq)
 VALUES('external','secretary','related','test','inbound','assistant','agent_message_chunk','streaming',1);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('external',0,'text','Unfinished fact');`,
	)
	require.NoError(t, err)
	ref := summaryTestRef("external", "Unfinished fact")
	job.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
	publication := secretaryNodesPublication(job, "one")
	publication.Outputs[0].Request.Coverage = append(publication.Outputs[0].Request.Coverage, ref)
	_, err = store.PublishSecretaryBatch(t.Context(), job, publication)
	assert.ErrorIs(t, err, domain.ErrSecretarySourceIncomplete)
	var count int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM summary_versions`).Scan(&count),
	)
	assert.Zero(t, count)
}

func TestSecretaryNodesPostgresGivenChangedInputThenRejectsStaleNoSummaryCommit(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='changed while processing' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	_, err = store.PublishSecretaryBatch(
		t.Context(),
		job,
		domain.SecretaryBatchPublication{
			RequestID: job.BatchID,
			Outcomes: []domain.SecretaryMessageOutcome{
				{MessageID: "m1", Action: "no_summary", Reason: "Nothing new"},
			},
		},
	)
	assert.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
}

func TestSecretaryNodesPostgresGivenLargeLegacySummaryThenDefersBodyWithoutResettingProgress(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,status,session_seq)
 SELECT 'legacy_'||n,'source','session','test','inbound','assistant','agent_message_chunk','complete',n+1 FROM generate_series(1,4096) n;
 INSERT INTO message_parts(message_id,part_index,part_type,text)
 SELECT 'legacy_'||n,0,'text','Fact '||n FROM generate_series(1,4096) n;`,
	)
	require.NoError(t, err)
	secretaryEnable(t, store)
	_, err = store.db.ExecContext(t.Context(), `UPDATE secretary_message_state state SET
 processed_hash='sha256:'||encode(sha256(convert_to(part.text,'UTF8')),'hex'),
 processed_canonicalization_version='visible-text-v1',processed_owner_user_id='owner'
 FROM message_parts part WHERE state.message_id=part.message_id;
 UPDATE secretary_jobs SET processed_revision=desired_revision;
 INSERT INTO summary_versions(summary_id,revision,owner_user_id,subject_type,subject_id,title,body,request_id,request_hash,created_at)
 VALUES('legacy-large',1,'owner','fragment','secretary:session:'||(SELECT id::text FROM agent_sessions WHERE agent_id='source' AND session_id='session'),
 'Legacy conclusion','Preserved historical content','legacy-large','legacy-large',NOW());
 INSERT INTO message_snapshots(message_id,canonicalization_version,content_hash,owner_user_id,agent_id,session_id,role,session_seq,message_created_at,body,created_at)
 SELECT m.message_id,'visible-text-v1','sha256:'||encode(sha256(convert_to(p.text,'UTF8')),'hex'),'owner',m.agent_id,m.session_id,m.role,m.session_seq,m.created_at,p.text,NOW()
 FROM messages m JOIN message_parts p USING(message_id) WHERE starts_with(m.message_id,'legacy_');
 INSERT INTO summary_sources(summary_id,revision,source_order,source_kind,message_id,canonicalization_version,content_hash)
 SELECT 'legacy-large',1,(row_number() OVER(ORDER BY message_id))-1,'message',message_id,canonicalization_version,content_hash
 FROM message_snapshots WHERE starts_with(message_id,'legacy_');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,status,session_seq)
 VALUES('increment','source','session','test','inbound','user','chat','complete',4098);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('increment',0,'text','A new completed turn');`)
	require.NoError(t, err)
	work := secretaryClaim(t, store)
	require.Len(t, work.Messages, 1)
	assert.Equal(t, "increment", work.Messages[0].SourceID)
	assert.Empty(t, work.CurrentSummaries)
	assert.True(t, work.SummariesHasMore)
	assert.Empty(t, work.SummariesCursor)
	assert.False(t, work.ResetRequired)
	prepared, err := store.PrepareSecretaryBatch(t.Context(), work)
	require.NoError(t, err)
	assert.True(t, prepared.SummariesHasMore)
	var processed, attempts int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_message_state WHERE processed_hash<>''`).
			Scan(&processed),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_attempts`).
			Scan(&attempts),
	)
	assert.Equal(t, 4097, processed)
	assert.Zero(t, attempts)
}

func TestSecretaryNodesPostgresGivenNullableLegacyMessageWithCompletedTurnThenAllowsEvidence(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryNodesReadyJob(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id) VALUES('secretary','related');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,status,turn_id,session_seq)
 VALUES('external','secretary','related','test','inbound','assistant','agent_message_chunk',NULL,NULL,1),
 ('terminal','secretary','related','test','inbound','assistant','turn_done','complete','turn',2);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('external',0,'text','Completed external fact');`,
	)
	require.NoError(t, err)
	ref := summaryTestRef("external", "Completed external fact")
	job.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
	publication := secretaryNodesPublication(job, "one")
	publication.Outputs[0].Request.Coverage = append(publication.Outputs[0].Request.Coverage, ref)
	versions, err := store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Len(t, versions[0].Coverage, 2)
}
