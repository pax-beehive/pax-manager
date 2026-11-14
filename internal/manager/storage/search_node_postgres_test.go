package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSearchPostgresNodeAttributionPreservesSessionHistory(t *testing.T) {
	s := searchIntegrationStore(t)
	_, err := s.db.ExecContext(t.Context(), `
 INSERT INTO nodes(node_id,owner_user_id,hostname,api_key_hash) VALUES ('new-node','owner','new-host','new-key');
 UPDATE agents SET node_id='new-node';`)
	require.NoError(t, err)
	doc, err := s.ReadSearchDocument(t.Context(), "owner", "message", "message")
	require.NoError(t, err)
	assert.Equal(t, "node", doc.NodeID)
	var sourceID string
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT id::text FROM agent_sessions WHERE session_id='session'`).
			Scan(&sourceID),
	)
	session, err := s.ReadSearchDocument(t.Context(), "owner", "session", sourceID)
	require.NoError(t, err)
	assert.Equal(t, "node", session.NodeID)
	_, err = s.db.ExecContext(
		t.Context(),
		`UPDATE messages SET node_id='new-node' WHERE message_id='message'`,
	)
	require.NoError(t, err)
	doc, err = s.ReadSearchDocument(t.Context(), "owner", "message", "message")
	require.NoError(t, err)
	assert.Empty(t, doc.NodeID)
	_, err = s.ReadSearchDocument(t.Context(), "other", "message", "message")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSearchPostgresSummaryNodeRequiresAllTransitiveSources(t *testing.T) {
	s := searchIntegrationStore(t)
	_, err := s.db.ExecContext(t.Context(), `
 INSERT INTO nodes(node_id,owner_user_id,hostname,api_key_hash) VALUES ('new-node','owner','new-host','new-key');
 UPDATE agents SET node_id='new-node';
 INSERT INTO agent_sessions(agent_id,session_id,session_name,node_id) VALUES ('agent','related','Related','node');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type)
 VALUES ('related-message','agent','related','test','inbound','user','user_message');
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('related-message',0,'text','port evidence');`)
	require.NoError(t, err)
	ref := func(id string) domain.MessageVersionRef {
		doc, readErr := s.ReadSearchDocument(t.Context(), "owner", "message", id)
		require.NoError(t, readErr)
		return domain.MessageVersionRef{MessageID: id, ContentHash: doc.ContentHash,
			CanonicalizationVersion: doc.CanonicalizationVersion}
	}
	_, err = s.PublishSummary(t.Context(), "owner", domain.PublishSummaryRequest{
		SummaryID: "child", SubjectType: "topic", SubjectID: "topic", RequestID: "node-child",
		Body: "port child", Coverage: []domain.MessageVersionRef{ref("related-message")},
	})
	require.NoError(t, err)
	var sourceID string
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT id::text FROM agent_sessions WHERE session_id='session'`).
			Scan(&sourceID),
	)
	_, err = s.PublishSummary(t.Context(), "owner", domain.PublishSummaryRequest{
		SummaryID: "root", SubjectType: "fragment", SubjectID: "secretary:session:" + sourceID,
		RequestID: "node-root", Body: "port root", Coverage: []domain.MessageVersionRef{ref("message")},
		SummarySources: []domain.SummaryRef{{SummaryID: "child", Revision: 1}},
	})
	require.NoError(t, err)
	doc, err := s.ReadSearchDocument(t.Context(), "owner", "summary", "root")
	require.NoError(t, err)
	assert.Equal(t, "node", doc.NodeID)
	_, err = s.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id,session_name,node_id)
 VALUES ('agent','primary','Private primary title','new-node')`,
	)
	require.NoError(t, err)
	var primaryID string
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT id::text FROM agent_sessions WHERE session_id='primary'`).
			Scan(&primaryID),
	)
	_, err = s.PublishSummary(t.Context(), "owner", domain.PublishSummaryRequest{
		SummaryID: "primary-other", SubjectType: "fragment", SubjectID: "secretary:session:" + primaryID,
		RequestID: "node-primary-other", Body: "port standalone", Coverage: []domain.MessageVersionRef{ref("message")},
	})
	require.NoError(t, err)
	_, err = s.PublishSummary(t.Context(), "owner", domain.PublishSummaryRequest{
		SummaryID: "parent-standalone", SubjectType: "topic", SubjectID: "parent-topic",
		RequestID: "node-parent", Body: "port parent",
		SummarySources: []domain.SummaryRef{{SummaryID: "primary-other", Revision: 1}},
	})
	require.NoError(t, err)
	parent, err := s.ReadSearchDocument(t.Context(), "owner", "summary", "parent-standalone")
	require.NoError(t, err)
	assert.Empty(
		t,
		parent.NodeID,
		"a standalone parent must include its pinned child's primary-session provenance",
	)
	_, err = s.GetSummary(t.Context(), "owner", "parent-standalone", 1)
	require.NoError(t, err, "search attribution must not change owner-authorized summary reads")
	for _, sql := range []string{
		`UPDATE agent_sessions SET node_id='new-node' WHERE session_id='primary'`,
		`UPDATE agent_sessions SET node_id=NULL WHERE session_id='primary'`,
	} {
		_, err = s.db.ExecContext(t.Context(), sql)
		require.NoError(t, err)
		primary, readErr := s.ReadSearchDocument(t.Context(), "owner", "summary", "primary-other")
		require.NoError(t, readErr)
		assert.Empty(t, primary.NodeID, "primary metadata must also belong to the evidence node")
	}
	_, err = s.db.ExecContext(
		t.Context(),
		`UPDATE agent_sessions SET archived_at=NOW() WHERE session_id='primary'`,
	)
	require.NoError(t, err)
	primary, err := s.ReadSearchDocument(t.Context(), "owner", "summary", "primary-other")
	require.NoError(t, err)
	assert.Equal(
		t,
		"node",
		primary.NodeID,
		"unreadable primary metadata is omitted from standalone summaries",
	)
	assert.Empty(t, primary.AgentID)
	for _, sql := range []string{
		`UPDATE agent_sessions SET node_id='new-node' WHERE session_id='related'`,
		`UPDATE agent_sessions SET node_id=NULL WHERE session_id='related'`,
		`UPDATE messages SET node_id='node' WHERE message_id='related-message'; UPDATE agent_sessions SET node_id='new-node' WHERE session_id='related'`,
	} {
		_, err = s.db.ExecContext(t.Context(), sql)
		require.NoError(t, err)
		doc, err = s.ReadSearchDocument(t.Context(), "owner", "summary", "root")
		require.NoError(t, err)
		assert.Empty(
			t,
			doc.NodeID,
			"primary source must not hide mixed, unknown, or conflicting evidence",
		)
	}
	_, err = s.ReadSearchDocument(t.Context(), "other", "summary", "root")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSearchPostgresNodeProjectionUpgradeRaisesRevisionAndPreservesLease(t *testing.T) {
	s := searchIntegrationStore(t)
	_, err := s.db.ExecContext(
		t.Context(),
		`ALTER TABLE search_sync_state DROP COLUMN projection_version;
 UPDATE search_sync_state SET desired_revision=5,indexed_revision=4,lease_token='old-lease',lease_until=NOW()+INTERVAL '1 minute'
 WHERE kind='message' AND source_id='message'`,
	)
	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "search.sql"))
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(), string(contents))
	require.NoError(t, err)
	require.NoError(t, s.BackfillSearch(t.Context()))
	var desired, indexed int64
	var projection int
	var token string
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT desired_revision,indexed_revision,projection_version,lease_token
 FROM search_sync_state WHERE kind='message' AND source_id='message'`).
			Scan(&desired, &indexed, &projection, &token),
	)
	assert.Equal(t, int64(6), desired)
	assert.Equal(t, int64(4), indexed)
	assert.Equal(t, 2, projection)
	assert.Equal(t, "old-lease", token)
	require.NoError(t, s.BackfillSearch(t.Context()))
	require.NoError(t, s.CompleteSearchWork(t.Context(), domain.SearchWork{
		Document: domain.SearchDocument{
			Kind:     "message",
			SourceID: "message",
			Revision: 5,
		}, LeaseToken: token,
	}, nil))
	work, err := s.ClaimSearchBatch(t.Context(), 100, time.Minute)
	require.NoError(t, err)
	var message domain.SearchDocument
	for _, item := range work {
		if item.Document.Kind == "message" && item.Document.SourceID == "message" {
			message = item.Document
		}
	}
	assert.Equal(t, int64(6), message.Revision)
	assert.Equal(t, "node", message.NodeID)
}
