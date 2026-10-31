//go:build integration

package userapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/search"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestWorkspaceMVPIntegrationGivenSourceChangesThenSearchAndSummaryHistoryStayConsistent(
	t *testing.T,
) {
	dsn, endpoint := os.Getenv("PAX_MANAGER_SEARCH_TEST_DATABASE_URL"), os.Getenv("SEARCH_TEST_URL")
	if dsn == "" || endpoint == "" {
		t.Skip("requires isolated PostgreSQL and Elasticsearch")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("workspace_mvp_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"init.sql", "summaries.sql", "search.sql"} {
		ddl, readErr := os.ReadFile(filepath.Join("..", "..", "..", "db", name))
		require.NoError(t, readErr)
		_, err = db.ExecContext(t.Context(), string(ddl))
		require.NoError(t, err)
	}
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO users(user_id,email) VALUES ('owner','mvp@example.test'),('other','other@example.test');
INSERT INTO agents(agent_id,owner_user_id,hostname,os,api_key_hash) VALUES ('agent','owner','host','linux','test-key');
INSERT INTO agent_sessions(agent_id,session_id,session_name) VALUES ('agent','session','Search MVP');
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
VALUES ('m1','agent','session','test','inbound','assistant','agent_message_chunk',1);
INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m1',0,'text','\u6570\u636e'),('m1',1,'text','\u5e93');`,
	)
	require.NoError(t, err)
	// SQL standard strings preserve backslashes; set the real Unicode text via parameters.
	_, err = db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text=$1 WHERE message_id='m1' AND part_index=0`,
		"\u6570\u636e",
	)
	require.NoError(t, err)
	_, err = db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text=$1 WHERE message_id='m1' AND part_index=1`,
		"\u5e93",
	)
	require.NoError(t, err)
	store := storage.NewPostgresStore(db, time.Now)
	index := fmt.Sprintf("workspace-mvp-%d", time.Now().UnixNano())
	client, err := search.NewClient(search.Config{URL: endpoint, Index: index})
	require.NoError(t, err)
	require.NoError(t, client.EnsureIndex(t.Context()))
	t.Cleanup(func() {
		request, _ := http.NewRequest(http.MethodDelete, endpoint+"/"+index, nil)
		response, deleteErr := http.DefaultClient.Do(request)
		if deleteErr == nil {
			_ = response.Body.Close()
		}
	})
	require.NoError(t, store.BackfillSearch(t.Context()))
	worker := search.NewWorker(store, client, search.WorkerConfig{})
	syncIndex := func() {
		t.Helper()
		require.NoError(t, worker.ProcessOnce(t.Context()))
		request, _ := http.NewRequest(http.MethodPost, endpoint+"/"+index+"/_refresh", nil)
		response, refreshErr := http.DefaultClient.Do(request)
		require.NoError(t, refreshErr)
		_ = response.Body.Close()
		require.Equal(t, 200, response.StatusCode)
	}
	syncIndex()
	service := NewWorkspaceSearchService(
		workspacePrincipal{owner: "owner"},
		store,
		store,
		client,
		[]byte(strings.Repeat("k", 32)),
	)
	page, err := service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "\u6570\u636e"},
	)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	value, err := service.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: page.Hits[0].Ref, View: "full"},
	)
	require.NoError(t, err)
	oldRead := value.(WorkspaceMessageRead)
	assert.Equal(t, "\u6570\u636e\u5e93", oldRead.Text)
	coverage := func(read WorkspaceMessageRead) domain.MessageVersionRef {
		return domain.MessageVersionRef{
			MessageID:               read.MessageID,
			ContentHash:             read.Ref.ContentHash,
			CanonicalizationVersion: read.Ref.CanonicalizationVersion,
			ReadReceipt:             read.ReadReceipt,
		}
	}
	request := domain.PublishSummaryRequest{
		SummaryID:   "summary",
		SubjectType: "topic",
		SubjectID:   "topic",
		RequestID:   "request-1",
		Body:        "\u6570\u636e\u5e93",
		Coverage:    []domain.MessageVersionRef{coverage(oldRead)},
	}
	v1, err := service.Publish(t.Context(), auth.RequestMetadata{}, request)
	require.NoError(t, err)
	assert.Equal(t, int64(1), v1.Revision)
	repeat, err := service.Publish(t.Context(), auth.RequestMetadata{}, request)
	require.NoError(t, err)
	assert.Equal(t, v1.Revision, repeat.Revision)
	syncIndex()
	page, err = service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "\u6570\u636e", Kinds: []string{"summary"}},
	)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.Equal(t, int64(1), page.Hits[0].Ref.Revision)
	_, err = db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text=$1 WHERE message_id='m1' AND part_index=1`,
		"\u5e93\u66f4\u65b0",
	)
	require.NoError(t, err)
	value, err = service.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: oldRead.Ref, View: "full"},
	)
	require.NoError(t, err)
	assert.Equal(t, oldRead.Text, value.(WorkspaceMessageRead).Text)
	value, err = service.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "message", ID: "m1"}, View: "full"},
	)
	require.NoError(t, err)
	newRead := value.(WorkspaceMessageRead)
	assert.NotEqual(t, oldRead.Ref.ContentHash, newRead.Ref.ContentHash)
	request.ExpectedRevision = 1
	request.RequestID = "request-2"
	request.Body = newRead.Text
	request.Coverage = []domain.MessageVersionRef{coverage(newRead)}
	v2, err := service.Publish(t.Context(), auth.RequestMetadata{}, request)
	require.NoError(t, err)
	assert.Equal(t, int64(2), v2.Revision)
	syncIndex()
	page, err = service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "\u66f4\u65b0", Kinds: []string{"summary"}},
	)
	require.NoError(t, err)
	require.Len(t, page.Hits, 1)
	assert.Equal(t, int64(2), page.Hits[0].Ref.Revision)
	value, err = service.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: SearchRef{Kind: "summary", ID: "summary", Revision: 1}},
	)
	require.NoError(t, err)
	assert.Equal(t, v1.Body, value.(domain.SummaryVersion).Body)
	other := NewWorkspaceSearchService(
		workspacePrincipal{owner: "other"},
		store,
		store,
		client,
		[]byte(strings.Repeat("k", 32)),
	)
	page, err = other.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "\u6570\u636e"},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Hits)
	_, err = db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
	require.NoError(t, err)
	// Revalidation protects snippets even before the worker processes deletion.
	page, err = service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "\u6570\u636e"},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Hits)
	_, err = service.Read(
		t.Context(),
		auth.RequestMetadata{},
		WorkspaceReadRequest{Ref: oldRead.Ref},
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	syncIndex()
}
