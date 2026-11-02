package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSearchGroupsPostgresGivenPrimaryAndCrossSessionCoverageThenUsesCanonicalSession(
	t *testing.T,
) {
	store := searchIntegrationStore(t)
	var sourceID string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT id::text FROM agent_sessions WHERE session_id='session'`).
			Scan(&sourceID),
	)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE agent_sessions SET custom_session_name='My canonical name';
INSERT INTO agent_sessions(agent_id,session_id,session_name,node_id) VALUES ('agent','related','Related session','node');
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type) VALUES ('related-message','agent','related','test','inbound','user','user_message');
INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('related-message',0,'text','related evidence')`,
	)
	require.NoError(t, err)
	related, err := store.ReadSearchDocument(t.Context(), "owner", "message", "related-message")
	require.NoError(t, err)
	request := domain.PublishSummaryRequest{
		SummaryID:   domain.SecretarySummaryID("owner", sourceID),
		SubjectType: "fragment",
		SubjectID:   "secretary:session:" + sourceID,
		RequestID:   "summary-group",
		Title:       "Summary title",
		Body:        "port summary",
		Coverage: []domain.MessageVersionRef{
			{
				MessageID:               related.SourceID,
				ContentHash:             related.ContentHash,
				CanonicalizationVersion: related.CanonicalizationVersion,
			},
		},
	}
	_, err = store.PublishSummary(t.Context(), "owner", request)
	require.NoError(t, err)
	doc, err := store.ReadSearchDocument(t.Context(), "owner", "summary", request.SummaryID)
	require.NoError(t, err)
	assert.Equal(t, "session", doc.SessionID)
	group, err := store.ReadSearchGroup(t.Context(), "owner", doc)
	require.NoError(t, err)
	assert.Equal(t, "My canonical name", group.Title)
	assert.Equal(t, "session", group.SessionID)
	request.SummaryID, request.SubjectType, request.SubjectID, request.RequestID = "project-summary", "project", "project", "project-group"
	_, err = store.PublishSummary(t.Context(), "owner", request)
	require.NoError(t, err)
	standalone, err := store.ReadSearchDocument(t.Context(), "owner", "summary", request.SummaryID)
	require.NoError(t, err)
	separate, err := store.ReadSearchGroup(t.Context(), "owner", standalone)
	require.NoError(t, err)
	assert.Empty(t, separate.SessionID)
	assert.NotEqual(t, group.Key, separate.Key)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agent_sessions SET custom_session_name=NULL,session_name='' WHERE session_id='session'`,
	)
	require.NoError(t, err)
	group, err = store.ReadSearchGroup(t.Context(), "owner", doc)
	require.NoError(t, err)
	assert.Equal(t, "session", group.Title)
	_, err = store.ReadSearchGroup(t.Context(), "other", doc)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agent_sessions SET archived_at=now() WHERE session_id='session'`,
	)
	require.NoError(t, err)
	_, err = store.ReadSearchGroup(t.Context(), "owner", doc)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSearchGroupsPostgresGivenExplicitMigrationThenBoundsPagesAndPreservesLeasesAndPriority(
	t *testing.T,
) {
	store := searchIntegrationStore(t)
	_, err := store.db.ExecContext(t.Context(), `TRUNCATE search_sync_state;
INSERT INTO search_sync_state(kind,source_id,desired_revision,indexed_revision,available_at,lease_until,lease_token)
VALUES ('message','a',5,5,'2000-01-01',NULL,''),('message','b',7,2,'2001-01-01',now()+interval '1 hour','active-lease'),('message','c',1,1,'2000-01-01',NULL,'')`)
	require.NoError(t, err)
	var schema string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT current_schema()`).Scan(&schema),
	)
	config, err := pgx.ParseConfig(os.Getenv("PAX_MANAGER_SEARCH_TEST_DATABASE_URL"))
	require.NoError(t, err)
	run := func(after string) map[string]any {
		command := exec.Command(
			"psql",
			"-X",
			"-q",
			"-t",
			"-A",
			"-v",
			"ON_ERROR_STOP=1",
			"-v",
			"after_kind=message",
			"-v",
			"after_source_id="+after,
			"-v",
			"batch_limit=2",
			"-f",
			filepath.Join(
				"..",
				"..",
				"..",
				"db",
				"migrations",
				"20261102_search_group_projection.sql",
			),
		)
		command.Env = append(os.Environ(), "PGDATABASE="+config.Database,
			"PGHOST="+config.Host, "PGPORT="+strconv.Itoa(int(config.Port)),
			"PGUSER="+config.User, "PGPASSWORD="+config.Password,
			"PGOPTIONS=-c search_path="+schema)
		output, commandErr := command.CombinedOutput()
		require.NoError(t, commandErr, string(output))
		var page map[string]any
		require.NoError(t, json.Unmarshal(output, &page))
		return page
	}
	first := run("")
	assert.EqualValues(t, 2, first["queued"])
	assert.Equal(t, "b", first["after_source_id"])
	var desired, indexed int64
	var token string
	var available time.Time
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT desired_revision,indexed_revision,lease_token,available_at FROM search_sync_state WHERE source_id='b'`).
			Scan(&desired, &indexed, &token, &available),
	)
	assert.EqualValues(t, 8, desired)
	assert.EqualValues(t, 2, indexed)
	assert.Equal(t, "active-lease", token)
	assert.Equal(t, 2001, available.UTC().Year())
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT available_at FROM search_sync_state WHERE source_id='a'`).
			Scan(&available),
	)
	assert.WithinDuration(t, time.Now(), available, 5*time.Second)
	require.NoError(
		t,
		store.CompleteSearchWork(
			t.Context(),
			domain.SearchWork{
				Document:   domain.SearchDocument{Kind: "message", SourceID: "b", Revision: 7},
				LeaseToken: "active-lease",
			},
			nil,
		),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT desired_revision,indexed_revision FROM search_sync_state WHERE source_id='b'`).
			Scan(&desired, &indexed),
	)
	assert.Greater(t, desired, indexed)
	last := run(fmt.Sprint(first["after_source_id"]))
	assert.EqualValues(t, 1, last["queued"])
	assert.Equal(t, "c", last["after_source_id"])
	assert.EqualValues(t, 0, run("c")["queued"])
}
