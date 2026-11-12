package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestWorkspaceAccessHTTPRequiresNodeAndUserDecisionForCurrentTurn(t *testing.T) {
	srv, fixture, _ := workspaceAccessFixture(t, domain.SessionApprovalModeManual)
	handler := srv.routes()
	post := func(path, key, email string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded)))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("X-Pax-Key", key)
		}
		if email != "" {
			req.Header.Set("X-User-Email", email)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	path := "/api/v1/node/agents/" + fixture.agentID + "/workspace/tools"
	search := workspaceToolRequest{
		SessionID:     "caller-session",
		Tool:          "search",
		ArgumentsJSON: `{"query":"cicd","scope":{"agent_ids":["*"],"session_ids":["*"]}}`,
	}
	response := post(path, "", "", search)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	response = post(path, fixture.nodeAPIKey, "", search)
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	response = post(path, fixture.nodeAPIKey, "", workspaceToolRequest{
		SessionID: "caller-session", Tool: "request_access", ArgumentsJSON: `{"queries":["cicd"]}`,
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	access := decodeData[workspaceAccessStatus](t, response.Body.Bytes())
	require.Equal(t, "pending", access.Status)
	arguments, err := json.Marshal(map[string]string{"approval_id": access.ApprovalID})
	require.NoError(t, err)
	response = post(path, fixture.nodeAPIKey, "", workspaceToolRequest{
		SessionID: "caller-session", Tool: "access_status", ArgumentsJSON: string(arguments),
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "pending", decodeData[workspaceAccessStatus](t, response.Body.Bytes()).Status)
	decisionPath := "/api/v1/user/self/approvals/" + access.ApprovalID + "/decision"
	response = post(
		decisionPath,
		"",
		fixture.userEmail,
		domain.ApprovalDecisionRequest{DecisionOption: "allow_once"},
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	response = post(path, fixture.nodeAPIKey, "", search)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	response = post(path, fixture.nodeAPIKey, "", workspaceToolRequest{
		SessionID: "caller-session", Tool: "cancel_access", ArgumentsJSON: string(arguments),
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(
		t,
		"cancelled",
		decodeData[workspaceAccessStatus](t, response.Body.Bytes()).Status,
	)
	response = post(path, fixture.nodeAPIKey, "", search)
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	setWorkspaceAccessTurn(t, srv, fixture, "turn-two", domain.RuntimeLifecycleRunning)
	response = post(path, fixture.nodeAPIKey, "", search)
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
}
