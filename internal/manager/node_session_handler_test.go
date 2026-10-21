package manager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNodeCreatesEncryptedSessionWithImmutableCronOrigin(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	register := func(key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/node/agents/"+fixture.agentID+"/sessions/register",
			strings.NewReader(body),
		)
		setJSON(req)
		req.Header.Set("X-Pax-Key", key)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	body := `{"native_id":"native_cron","cwd":"/workspace","origin":{"kind":"cronjob","registration_id":"binding_1","cronjob_id":"cron_1","run_id":"run_1","name":"Daily review"}}`
	require.Equal(t, http.StatusUnauthorized, register("", body).Code)
	first := register(fixture.nodeAPIKey, body)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	session := decodeData[AgentSession](t, first.Body.Bytes())
	require.NotEmpty(t, session.SessionID)
	require.Equal(t, "e2ee", session.Transport)
	require.Equal(t, "/workspace", session.PaxConfig.CWD)
	require.Contains(t, string(session.Metadata), `"cronjob_id":"cron_1"`)
	again := register(fixture.nodeAPIKey, body)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	require.Equal(t, session.SessionID, decodeData[AgentSession](t, again.Body.Bytes()).SessionID)
	conflict := register(fixture.nodeAPIKey, strings.Replace(body, "native_cron", "other", 1))
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	other := testNodeAgent(t, srv, "other@example.com")
	require.Equal(t, http.StatusNotFound, register(other.nodeAPIKey, body).Code)
}
