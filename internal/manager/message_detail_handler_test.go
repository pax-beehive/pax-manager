package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSummaryHistoryKeepsConversationTextAcrossToolPageBoundary(t *testing.T) {
	srv, _ := testServer(t, "history@example.com")
	fixture := testNodeAgent(t, srv, "history@example.com")
	createConversationTestSession(t, srv, fixture, "long-history", "native-long-history")
	put := func(id, role, kind, text string, raw json.RawMessage) {
		t.Helper()
		require.NoError(t, srv.store.UpsertMessage(t.Context(), &domain.Message{
			MessageID: id, AgentID: fixture.agentID, SessionID: "long-history", TurnID: "turn-long",
			Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
			Role: role, MessageType: kind, RawJSON: raw,
		}))
		if text != "" {
			require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
				MessageID: id, PartType: domain.MessagePartText, Text: text,
			}))
		}
	}
	put("user-prompt", "user", "user", "Read the deployment status", json.RawMessage(
		`{"method":"session/prompt","params":{"prompt":[{"type":"text","text":"Read the deployment status"}]}}`,
	))
	put("assistant-answer", "assistant", "agent_message_chunk", "Deployment completed", nil)
	for i := 0; i < 150; i++ {
		put(fmt.Sprintf("tool-%03d", i), "assistant", "tool_call", "", json.RawMessage(
			`{"params":{"update":{"toolCallId":"call","title":"Check deployment","status":"completed","rawOutput":"not-inline"}}}`,
		))
	}
	put("turn-done", "assistant", "turn_done", "", nil)
	read := func(cursor int64) domain.MessageHistoryPagination {
		t.Helper()
		path := "/api/v1/user/self/sessions/long-history/history?view=summary&limit=100"
		if cursor != 0 {
			path += "&before_seq=" + strconv.FormatInt(cursor, 10)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-User-Email", fixture.userEmail)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := decodeData[struct {
			Messages   []domain.MessageWithParts       `json:"messages"`
			Pagination domain.MessageHistoryPagination `json:"pagination"`
		}](t, rec.Body.Bytes())
		byID := map[string]domain.MessageWithParts{}
		tools := 0
		for _, message := range body.Messages {
			require.NotContains(t, byID, message.MessageID)
			byID[message.MessageID] = message
			if message.Tool != nil {
				tools++
				require.Empty(t, message.RawJSON)
			}
		}
		require.LessOrEqual(t, tools, 100)
		require.Equal(t, "Read the deployment status", byID["user-prompt"].Parts[0].Text)
		require.Equal(t, "Deployment completed", byID["assistant-answer"].Parts[0].Text)
		require.NotContains(t, rec.Body.String(), "not-inline")
		return body.Pagination
	}
	page := read(0)
	require.True(t, page.HasOlder)
	require.Greater(t, page.NextBeforeSeq, int64(2))
	older := read(page.NextBeforeSeq)
	require.False(t, older.HasOlder)
}

func TestSessionMessageSummaryAndDetails(t *testing.T) {
	srv, _ := testServer(t, "history@example.com")
	fixture := testNodeAgent(t, srv, "history@example.com")
	createConversationTestSession(t, srv, fixture, "summary-session", "native-summary")
	createConversationTestSession(t, srv, fixture, "other-session", "native-other")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	output := strings.Repeat("\u4f60\U0001f600", 20000)
	raw, err := json.Marshal(map[string]any{"method": "session/update", "params": map[string]any{
		"sessionId": "native-summary", "update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call-1",
			"title": "Run tests", "status": "completed", "rawInput": map[string]any{"command": "go test"}, "rawOutput": output}}})
	require.NoError(t, err)
	msg := domain.Message{
		MessageID:   "tool-summary",
		AgentID:     fixture.agentID,
		SessionID:   "native-summary",
		OwnerUserID: principal.User.UserID,
		NodeID:      fixture.nodeID,
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		MessageType: "tool_call",
		RawJSON:     raw,
		TurnID:      "turn-summary",
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &msg))
	require.NoError(
		t,
		srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{MessageID: msg.MessageID,
			PartType: domain.MessagePartRawJSON, PayloadJSON: raw}),
	)
	// The summary read must not run the old session-wide artifact repair scan.
	srv.userapi.SetHistoryReconciler(
		func(_ctx context.Context, _agentID, _sessionID string) error { return errors.New("unexpected repair") },
	)
	get := func(path, email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-User-Email", email)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	root := "/api/v1/user/self/sessions/summary-session"
	rec := get(root+"/history?view=summary", fixture.userEmail)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Less(t, rec.Body.Len(), 2000)
	history := decodeData[struct {
		Messages []domain.MessageWithParts `json:"messages"`
	}](t, rec.Body.Bytes())
	require.Len(t, history.Messages, 1)
	require.Equal(t, "Run tests", history.Messages[0].Tool.Title)
	require.True(t, history.Messages[0].HasDetail)
	require.Equal(t, "summary-session", history.Messages[0].SessionID)
	require.Empty(t, history.Messages[0].RawJSON)
	require.Empty(t, history.Messages[0].Parts)
	var combined strings.Builder
	offset, revision := 0, ""
	for {
		path := root + "/messages/tool-summary?section=output&limit=4096&offset=" + strconv.Itoa(
			offset,
		)
		if revision != "" {
			path += "&revision=" + url.QueryEscape(revision)
		}
		rec = get(path, fixture.userEmail)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		page := decodeData[domain.MessageDetailPage](t, rec.Body.Bytes())
		require.True(t, utf8.ValidString(page.Text))
		require.LessOrEqual(t, utf8.RuneCountInString(page.Text), 4096)
		combined.WriteString(page.Text)
		if !page.HasMore {
			break
		}
		require.Greater(t, page.NextOffset, offset)
		offset, revision = page.NextOffset, page.Revision
	}
	var restored string
	require.NoError(t, json.Unmarshal([]byte(combined.String()), &restored))
	require.Equal(t, output, restored)
	for _, tc := range []struct {
		path, email string
		code        int
	}{
		{root + "/messages/tool-summary?offset=1", fixture.userEmail, 400},
		{root + "/messages/tool-summary?offset=-1", fixture.userEmail, 400},
		{root + "/messages/tool-summary?limit=bad", fixture.userEmail, 400},
		{root + "/messages/tool-summary?section=invalid", fixture.userEmail, 400},
		{root + "/messages/tool-summary?offset=1&revision=stale", fixture.userEmail, 409},
		{root + "/messages/missing", fixture.userEmail, 404},
		{"/api/v1/user/self/sessions/other-session/messages/tool-summary", fixture.userEmail, 404},
		{root + "/messages/tool-summary", "stranger@example.com", 404},
		{root + "/history?view=summary", "stranger@example.com", 404},
	} {
		rec = get(tc.path, tc.email)
		require.Equal(t, tc.code, rec.Code, tc.path+": "+rec.Body.String())
	}
	promptRaw := json.RawMessage(
		`{"jsonrpc":"2.0","method":"session/prompt","params":{"sessionId":"native-summary","prompt":[{"type":"text","text":"unique-user-body"},{"type":"image","data":"test-image","mimeType":"image/png"}]}}`,
	)
	prompt := msg
	prompt.MessageID = "prompt-summary"
	prompt.ID = 0
	prompt.MessageType = "user_message"
	prompt.Role = "user"
	prompt.Direction = domain.MessageDirectionUserToAgent
	prompt.RawJSON = promptRaw
	prompt.SessionSeq = 0
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &prompt))
	require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: prompt.MessageID, PartType: domain.MessagePartText,
		Text: "unique-user-body", PayloadJSON: promptRaw,
	}))
	rec = get(root+"/history?view=summary", fixture.userEmail)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	withPrompt := decodeData[struct {
		Messages []domain.MessageWithParts `json:"messages"`
	}](t, rec.Body.Bytes())
	var promptParts []domain.MessagePart
	for _, message := range withPrompt.Messages {
		if message.MessageID == prompt.MessageID {
			promptParts = message.Parts
		}
	}
	require.Len(t, promptParts, 1)
	require.Equal(t, "unique-user-body", promptParts[0].Text)
	require.Empty(t, promptParts[0].PayloadJSON)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "test-image"))

}
