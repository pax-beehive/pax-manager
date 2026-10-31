package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func TestSecretaryWorkerGivenDisabledSettingsThenStopsWithServiceContext(t *testing.T) {
	srv, _ := testServer(t, "secretary-lifecycle@example.test")
	ctx, cancel := context.WithCancel(t.Context())
	stop := srv.startSecretaryWorker(ctx)
	cancel()
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Secretary worker did not stop with Manager")
	}
	assert.False(t, srv.secretaryAgentAvailable(t.Context(), "missing-owner", "missing-agent"))
	srv.secretaryStore = nil
	srv.startSecretaryWorker(t.Context())()
}

func TestSecretaryHTTPGivenOwnerThenSettingsArePersistentAndDefaultOff(t *testing.T) {
	srv, _ := testServer(t, "secretary@example.test")
	fixture := testNodeAgent(t, srv, "secretary@example.test")
	handler := srv.routes()
	request := func(method, path, email string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("X-User-Email", email)
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	get := request(http.MethodGet, "/api/v1/user/self/secretary", fixture.userEmail, nil)
	require.Equal(t, http.StatusOK, get.Code, get.Body.String())
	initial := decodeData[userapi.SecretaryResponse](t, get.Body.Bytes())
	assert.False(t, initial.Enabled)
	assert.Equal(t, "disabled", initial.State)
	assert.Zero(t, initial.Revision)
	update := map[string]any{
		"agent_id":          fixture.agentID,
		"enabled":           true,
		"expected_revision": initial.Revision,
	}
	saved := request(http.MethodPatch, "/api/v1/user/self/secretary", fixture.userEmail, update)
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	settings := decodeData[userapi.SecretaryResponse](t, saved.Body.Bytes())
	assert.True(t, settings.Enabled)
	assert.Equal(t, fixture.agentID, settings.AgentID)
	assert.Equal(t, "waiting_for_agent", settings.State)
	assert.False(t, settings.AgentAvailable)
	assert.Equal(t, int64(1), settings.Revision)
	stale := request(http.MethodPatch, "/api/v1/user/self/secretary", fixture.userEmail, update)
	assert.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
	run := request(http.MethodPost, "/api/v1/user/self/secretary/run", fixture.userEmail, nil)
	assert.Equal(t, http.StatusAccepted, run.Code, run.Body.String())
	other := request(
		http.MethodGet,
		"/api/v1/user/self/secretary",
		"other-secretary@example.test",
		nil,
	)
	require.Equal(t, http.StatusOK, other.Code, other.Body.String())
	assert.Empty(t, decodeData[userapi.SecretaryResponse](t, other.Body.Bytes()).AgentID)
	foreign := request(
		http.MethodPatch,
		"/api/v1/user/self/secretary",
		"other-secretary@example.test",
		update,
	)
	assert.Equal(t, http.StatusNotFound, foreign.Code, foreign.Body.String())
	disabled := request(
		http.MethodPatch,
		"/api/v1/user/self/secretary",
		fixture.userEmail,
		map[string]any{"agent_id": "", "enabled": false, "expected_revision": settings.Revision},
	)
	require.Equal(t, http.StatusOK, disabled.Code, disabled.Body.String())
	assert.Equal(
		t,
		"disabled",
		decodeData[userapi.SecretaryResponse](t, disabled.Body.Bytes()).State,
	)
	assert.Equal(
		t,
		http.StatusConflict,
		request(http.MethodPost, "/api/v1/user/self/secretary/run", fixture.userEmail, nil).Code,
	)
}

func TestSecretaryHTTPGivenMalformedOrInjectedSettingsThenRejectsWithoutChanges(t *testing.T) {
	srv, _ := testServer(t, "secretary-invalid@example.test")
	handler := srv.routes()
	for _, body := range []string{
		`{`, `null`, `{}`, `{"enabled":false}`, `{"agent_id":"","enabled":false,"expected_revision":0,"owner_user_id":"victim"}`,
		`{"agent_id":"","enabled":false,"expected_revision":0} {}`,
		`{"agent_id":"","enabled":true,"expected_revision":0}`,
	} {
		t.Run(body, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPatch,
				"/api/v1/user/self/secretary",
				bytes.NewBufferString(body),
			)
			req.Header.Set("X-User-Email", "secretary-invalid@example.test")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
}
