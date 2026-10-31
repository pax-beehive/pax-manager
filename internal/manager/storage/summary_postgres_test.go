package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryPostgresGivenPublishedVersionsThenSourcesRemainExactAndAccessIsCurrent(
	t *testing.T,
) {
	store := summaryPostgresFixture(t)
	ctx := t.Context()
	request := summaryTestRequest("old body")
	request.Coverage = append(request.Coverage, summaryTestRef("message_2", "another message"))
	first, err := store.PublishSummary(ctx, "owner_1", request)
	require.NoError(t, err)
	assert.EqualValues(t, 1, first.Revision)
	_, err = store.db.ExecContext(
		ctx,
		`UPDATE message_parts SET text='new body' WHERE message_id='message_1'`,
	)
	require.NoError(t, err)
	historical, err := store.ReadMessageVersion(ctx, "owner_1", request.Coverage[0])
	require.NoError(t, err)
	assert.Equal(t, "old body", historical.Body)

	secondRequest := summaryTestRequest("new body")
	secondRequest.ExpectedRevision, secondRequest.RequestID = 1, "request_2"
	secondRequest.Body = "Updated summary"
	second, err := store.PublishSummary(ctx, "owner_1", secondRequest)
	require.NoError(t, err)
	assert.EqualValues(t, 2, second.Revision)
	current, err := store.GetSummary(ctx, "owner_1", "summary_1", 0)
	require.NoError(t, err)
	assert.Equal(t, "Updated summary", current.Body)
	old, err := store.GetSummary(ctx, "owner_1", "summary_1", 1)
	require.NoError(t, err)
	assert.Equal(t, "Summary body", old.Body)
	assert.Len(t, old.Coverage, 2)
	retried, err := store.PublishSummary(ctx, "owner_1", request)
	require.NoError(t, err)
	assert.EqualValues(t, 1, retried.Revision)

	var snapshots int
	require.NoError(
		t,
		store.db.QueryRowContext(ctx, `SELECT count(*) FROM message_snapshots`).Scan(&snapshots),
	)
	assert.Equal(t, 3, snapshots)
	_, err = store.db.ExecContext(
		ctx,
		`UPDATE summary_versions SET body='mutated' WHERE summary_id='summary_1'`,
	)
	assert.ErrorContains(t, err, "immutable")
	_, err = store.db.ExecContext(
		ctx,
		`UPDATE message_snapshots SET body='mutated' WHERE message_id='message_1'`,
	)
	assert.ErrorContains(t, err, "immutable")
	_, err = store.db.ExecContext(
		ctx,
		`UPDATE summary_sources SET content_hash='mutated' WHERE message_id='message_1'`,
	)
	assert.ErrorContains(t, err, "immutable")

	_, err = store.db.ExecContext(
		ctx,
		`UPDATE agent_sessions SET archived_at=now() WHERE session_id='session_1'`,
	)
	require.NoError(t, err)
	_, err = store.ReadMessageVersion(ctx, "owner_1", request.Coverage[0])
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetSummary(ctx, "owner_1", "summary_1", 1)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSummaryPostgresGivenChangedUnreadVersionThenPublicationRollsBack(t *testing.T) {
	store := summaryPostgresFixture(t)
	request := summaryTestRequest("old body")
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='changed' WHERE message_id='message_1'`,
	)
	require.NoError(t, err)
	_, err = store.PublishSummary(t.Context(), "owner_1", request)
	assert.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
	var versions, snapshots int
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
	assert.Zero(t, versions)
	assert.Zero(t, snapshots)
}

func TestSummaryPostgresGivenChildRevisionThenParentsRemainPinnedAndRecheckLeafAccess(
	t *testing.T,
) {
	store := summaryPostgresFixture(t)
	ctx := t.Context()
	childRequest := summaryTestRequest("old body")
	_, err := store.PublishSummary(ctx, "owner_1", childRequest)
	require.NoError(t, err)
	parentRequest := domain.PublishSummaryRequest{
		SummaryID:      "parent_1",
		SubjectType:    "project",
		SubjectID:      "project_1",
		RequestID:      "parent_request",
		Body:           "Project progress",
		SummarySources: []domain.SummaryRef{{SummaryID: "summary_1", Revision: 1}},
	}
	_, err = store.PublishSummary(ctx, "owner_1", parentRequest)
	require.NoError(t, err)
	childRequest.RequestID, childRequest.ExpectedRevision, childRequest.Body = "child_v2", 1, "New child summary"
	_, err = store.PublishSummary(ctx, "owner_1", childRequest)
	require.NoError(t, err)
	parent, err := store.GetSummary(ctx, "owner_1", "parent_1", 0)
	require.NoError(t, err)
	require.Len(t, parent.SummarySources, 1)
	assert.EqualValues(t, 1, parent.SummarySources[0].Revision)
	_, err = store.GetSummary(ctx, "other_owner", "parent_1", 0)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.PublishSummary(ctx, "other_owner", parentRequest)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.db.ExecContext(
		ctx,
		`UPDATE agents SET deleted_at=now() WHERE agent_id='agent_1'`,
	)
	require.NoError(t, err)
	_, err = store.GetSummary(ctx, "owner_1", "parent_1", 0)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSummaryPostgresGivenConcurrentRetriesThenExactlyOneVersionIsPublished(t *testing.T) {
	store := summaryPostgresFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	const callers = 8
	results := make(chan error, callers)
	var callersDone sync.WaitGroup
	for range callers {
		callersDone.Add(1)
		go func() {
			defer callersDone.Done()
			version, err := store.PublishSummary(ctx, "owner_1", summaryTestRequest("old body"))
			if err == nil && version.Revision != 1 {
				err = fmt.Errorf("unexpected revision %d", version.Revision)
			}
			results <- err
		}()
	}
	callersDone.Wait()
	close(results)
	for err := range results {
		assert.NoError(t, err)
	}
	var count int
	require.NoError(
		t,
		store.db.QueryRowContext(ctx, `SELECT count(*) FROM summary_versions`).Scan(&count),
	)
	assert.Equal(t, 1, count)
}

func summaryPostgresFixture(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("PAX_MANAGER_SEARCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_SEARCH_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("summary_mvp_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	_, err = db.ExecContext(t.Context(), summarySourceFixtureSQL)
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "summaries.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(raw))
	require.NoError(t, err)
	return &PostgresStore{db: db, now: time.Now}
}

const summarySourceFixtureSQL = `
CREATE TABLE nodes (node_id TEXT PRIMARY KEY, deleted_at TIMESTAMPTZ);
CREATE TABLE agents (agent_id TEXT PRIMARY KEY, owner_user_id TEXT NOT NULL, node_id TEXT, deleted_at TIMESTAMPTZ);
CREATE TABLE agent_sessions (id BIGSERIAL PRIMARY KEY, agent_id TEXT, session_id TEXT, node_id TEXT, source TEXT, archived_at TIMESTAMPTZ);
CREATE TABLE messages (message_id TEXT PRIMARY KEY, agent_id TEXT, node_id TEXT, session_id TEXT,
    conversation_id TEXT, role TEXT, message_type TEXT, parent_message_id TEXT, raw_json JSONB,
    session_seq BIGINT, created_at TIMESTAMPTZ);
CREATE TABLE message_parts (message_id TEXT, part_index INTEGER, part_type TEXT, text TEXT);
CREATE TABLE search_sync_state (kind TEXT, source_id TEXT, desired_revision BIGINT);
INSERT INTO agents (agent_id, owner_user_id) VALUES ('agent_1','owner_1');
INSERT INTO agent_sessions (agent_id,session_id) VALUES ('agent_1','session_1');
INSERT INTO messages (message_id,agent_id,session_id,role,session_seq,created_at)
    VALUES ('message_1','agent_1','session_1','assistant',1,now()),('message_2','agent_1','session_1','user',2,now());
INSERT INTO message_parts VALUES ('message_1',0,'text','old body'),('message_2',0,'text','another message');
`
