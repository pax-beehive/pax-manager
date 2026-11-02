package manager

import (
	"context"
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

type workspaceGroupedHTTPBackend struct {
	workspaceTestBackend
	groups []domain.SearchGroup
	query  domain.SearchQuery
}

func (b *workspaceGroupedHTTPBackend) SearchGroups(
	_ context.Context,
	q domain.SearchQuery,
) (domain.SearchGroupPage, error) {
	b.query = q
	return domain.SearchGroupPage{Groups: b.groups}, nil
}

func TestWorkspaceGroupedHTTPGivenOwnedMessageThenSerializesGroupsAndEmptyResults(t *testing.T) {
	srv, _ := testServer(t, "grouped@example.test")
	fixture := testNodeAgent(t, srv, "grouped@example.test")
	createConversationTestSession(t, srv, fixture, "grouped-session", "native-grouped")
	message := domain.Message{
		MessageID:   "grouped-message",
		AgentID:     fixture.agentID,
		SessionID:   "grouped-session",
		Role:        "user",
		MessageType: "user_message",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(
		t,
		srv.store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{
				MessageID: message.MessageID,
				PartType:  "text",
				Text:      "port evidence",
			},
		),
	)
	base := srv.store.(canonicalSessionStore).Store
	source := base.(domain.SearchSourceStore)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	doc, err := source.ReadSearchDocument(
		t.Context(),
		principal.User.UserID,
		"message",
		message.MessageID,
	)
	require.NoError(t, err)
	backend := &workspaceGroupedHTTPBackend{
		workspaceTestBackend: workspaceTestBackend{docs: []domain.SearchDocument{doc}},
		groups: []domain.SearchGroup{
			{Key: domain.SearchDocumentGroupKey(doc), Matches: []domain.SearchDocument{doc}},
		},
	}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(
		srv.auth,
		source,
		workspaceSummaryFake{source},
		backend,
		nil,
	)
	handler := srv.routes()
	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/user/self/search",
			strings.NewReader(body),
		)
		request.Header.Set("X-User-Email", fixture.userEmail)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := post(`{"query":"port","group_by":"session","limit":20}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	page := decodeData[userapi.WorkspaceSearchPage](t, response.Body.Bytes())
	require.Len(t, page.Groups, 1)
	assert.Len(t, page.Groups[0].Matches, 1)
	assert.Equal(t, "session", backend.query.GroupBy)
	backend.groups = nil
	response = post(`{"query":"port","group_by":"session"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	data := decodeData[map[string]json.RawMessage](t, response.Body.Bytes())
	assert.JSONEq(t, `[]`, string(data["groups"]))
	assert.JSONEq(t, `[]`, string(data["hits"]))
	response = post(`{"query":"port"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	page = decodeData[userapi.WorkspaceSearchPage](t, response.Body.Bytes())
	assert.Len(t, page.Hits, 1)
	response = post(`{"query":"port","group_by":"session","group_key":"key"}`)
	assert.Equal(t, http.StatusBadRequest, response.Code)
}
