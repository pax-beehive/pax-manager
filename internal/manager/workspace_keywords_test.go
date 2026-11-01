package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func TestWorkspaceHTTPKeywordsRevalidateAnyTermAgainstCurrentStoredSummary(t *testing.T) {
	srv, _ := testServer(t, "keywords@example.test")
	fixture := testNodeAgent(t, srv, "keywords@example.test")
	createConversationTestSession(t, srv, fixture, "keywords-session", "native-keywords")
	message := domain.Message{
		MessageID:   "keyword-source",
		AgentID:     fixture.agentID,
		SessionID:   "keywords-session",
		Role:        "user",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
		MessageType: "user_message",
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(
		t,
		srv.store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{
				MessageID: message.MessageID,
				PartType:  domain.MessagePartText,
				Text:      "source evidence",
			},
		),
	)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	base := srv.store.(canonicalSessionStore).Store
	sources := base.(domain.SearchSourceStore)
	summaries := base.(domain.SummaryStore)
	source, err := sources.ReadSearchDocument(
		t.Context(),
		principal.User.UserID,
		"message",
		message.MessageID,
	)
	require.NoError(t, err)
	publish := domain.PublishSummaryRequest{
		SummaryID:   "keyword-summary",
		SubjectType: "topic",
		SubjectID:   "topic",
		RequestID:   "keyword-v1",
		Title:       "API settings",
		Body:        "port configuration",
		Coverage: []domain.MessageVersionRef{
			{
				MessageID:               message.MessageID,
				ContentHash:             source.ContentHash,
				CanonicalizationVersion: source.CanonicalizationVersion,
			},
		},
	}
	_, err = summaries.PublishSummary(t.Context(), principal.User.UserID, publish)
	require.NoError(t, err)
	backend := &workspaceTestBackend{
		docs: []domain.SearchDocument{{Kind: "summary", SourceID: publish.SummaryID}},
	}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(
		srv.auth,
		sources,
		summaries,
		backend,
		[]byte(strings.Repeat("k", 32)),
	)
	handler := srv.routes()
	search := func(email, query string) userapi.WorkspaceSearchPage {
		t.Helper()
		raw, encodeErr := json.Marshal(map[string]string{"query": query})
		require.NoError(t, encodeErr)
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/user/self/search",
			strings.NewReader(string(raw)),
		)
		req.Header.Set("X-User-Email", email)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData[userapi.WorkspaceSearchPage](t, rec.Body.Bytes())
	}
	page := search(fixture.userEmail, "PORT api port")
	require.Len(t, page.Hits, 1)
	assert.Equal(t, "port configuration", page.Hits[0].Snippet)
	assert.Equal(t, "API settings", page.Hits[0].Title)
	assert.Empty(t, search("other@example.test", "port api").Hits)
	publish.ExpectedRevision = 1
	publish.RequestID = "keyword-v2"
	publish.Title = "different settings"
	_, err = summaries.PublishSummary(t.Context(), principal.User.UserID, publish)
	require.NoError(t, err)
	assert.Len(
		t,
		search(fixture.userEmail, "api port").Hits,
		1,
		"a current partial match remains eligible",
	)
	publish.ExpectedRevision = 2
	publish.RequestID = "keyword-v3"
	publish.Body = "unrelated content"
	_, err = summaries.PublishSummary(t.Context(), principal.User.UserID, publish)
	require.NoError(t, err)
	assert.Empty(
		t,
		search(fixture.userEmail, "api port").Hits,
		"stale index cannot retain a hit missing every current term",
	)
}
