package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
)

func TestSandboxNodeHTTPCreatesAnOwnedNodeWithoutAgentOrModelCredentials(t *testing.T) {
	const ownerEmail = "pilot@example.com"
	const host = "aws-pilot"
	key := strings.Repeat("n", 32)
	server, store := newConfigTestServer(t, Config{
		HostingSandboxHostKey: key, HostingSandboxHostID: host,
		HostingSandboxCloudURL: "https://node.example.com",
	})
	owner, err := store.EnsureUser(t.Context(), ownerEmail, "Pilot", "user")
	require.NoError(t, err)

	get := func(user string) *httptest.ResponseRecorder {
		return hostingRequest(t, server, "GET", "/api/v1/sandbox", "", user, "")
	}
	start := func(body string) *httptest.ResponseRecorder {
		return hostingRequest(t, server, "POST", "/api/v1/sandbox", body, ownerEmail, "")
	}
	reopen := func(action, body string) *httptest.ResponseRecorder {
		return hostingRequest(t, server, "POST", "/api/v1/sandbox/"+action, body, ownerEmail, "")
	}
	require.Equal(t, http.StatusUnauthorized, get("").Code)
	require.Equal(t, http.StatusNotFound, get(ownerEmail).Code)
	require.Equal(t, http.StatusNotFound, start("{}").Code)
	featureKey := runtimeconfig.FeatureKey(runtimeconfig.FeatureHostedSandbox)
	gates := callAPI(t, server, "GET", "/api/v1/admin/config", "admin@example.com", nil)
	require.Equal(t, http.StatusOK, gates.Code)
	require.Contains(t, gates.Body.String(), featureKey)
	enabled := callAPI(
		t,
		server,
		"PUT",
		"/api/v1/admin/config/"+featureKey,
		"admin@example.com",
		`{"value":{"stage":"beta","beta_emails":["PILOT@example.com"]},"expected_version":0,"reason":"pilot"}`,
	)
	require.Equal(t, http.StatusOK, enabled.Code)
	require.Equal(t, http.StatusNotFound, get("other@example.com").Code)
	require.Equal(t, http.StatusNotFound,
		hostingRequest(t, server, "POST", "/api/v1/sandbox", "{}", "other@example.com", "").Code)
	initial := get(ownerEmail)
	require.Equal(t, http.StatusOK, initial.Code)
	require.Nil(t, decodeData[hosting.SandboxStatus](t, initial.Body.Bytes()).Sandbox)
	require.Equal(t, http.StatusNotFound, reopen("resume", `{"node_id":"unknown"}`).Code)
	require.Equal(t, http.StatusBadRequest, start(`{"owner_user_id":"foreign"}`).Code)
	queued := start("{}")
	require.Equal(t, http.StatusOK, queued.Code)
	require.Equal(t, "no-store", queued.Header().Get("Cache-Control"))
	require.Equal(
		t,
		"queued",
		decodeData[hosting.SandboxStatus](t, queued.Body.Bytes()).Sandbox.Status,
	)

	claim := func(body, token string) *httptest.ResponseRecorder {
		return hostingRequest(t, server, "POST", "/api/v1/hosting/jobs/claim", body, "", token)
	}
	claimBody := func(host string, capacity string) string {
		return `{"kind":"node_only","host_id":"` + host + `"` + capacity + `}`
	}
	zero := claimBody(host, `,"capacity":0`)
	require.Equal(
		t,
		http.StatusUnauthorized,
		claim(`{"host_id":"aws-pilot","capacity":1}`, key).Code,
	)
	require.Equal(t, http.StatusUnauthorized, claim(zero, "").Code)
	wrongHost := claimBody("other-host", `,"capacity":0`)
	require.Equal(t, http.StatusUnauthorized, claim(wrongHost, key).Code)
	require.Equal(t, http.StatusConflict, claim(claimBody(host, `,"capacity":-1`), key).Code)
	require.Equal(t, http.StatusOK, claim(claimBody(host, ""), key).Code)
	require.Equal(t, http.StatusOK, claim(claimBody(host, `,"capacity":null`), key).Code)
	require.NotContains(t, get(ownerEmail).Body.String(), "reason_code")

	require.Equal(t, http.StatusOK, claim(zero, key).Code)
	waiting := get(ownerEmail)
	require.Equal(t, &hosting.SandboxView{Status: "queued", ReasonCode: "waiting_for_capacity"},
		decodeData[hosting.SandboxStatus](t, waiting.Body.Bytes()).Sandbox)
	require.NotContains(t, waiting.Body.String(), host)
	require.NotContains(t, waiting.Body.String(), "available_slots")

	body := `{"kind":"node_only","host_id":"aws-pilot","capacity":1}`
	claimed := claim(body, key)
	require.Equal(t, http.StatusOK, claimed.Code)
	require.NotContains(t, claimed.Body.String(), "\"model_")
	job := decodeData[struct{ Job *hosting.Job }](t, claimed.Body.Bytes()).Job
	require.NotNil(t, job)
	require.Equal(t, owner.UserID, job.OwnerUserID)
	require.NotEmpty(t, job.RegistrationToken)

	register := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/node/register", strings.NewReader(
			`{"name":"Pax Sandbox","hostname":"sandbox","machine_type":"linux_box","os":"linux","arch":"amd64"}`,
		))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Registration-Token", job.RegistrationToken)
		recorder := httptest.NewRecorder()
		server.routes().ServeHTTP(recorder, request)
		return recorder
	}
	registered := register()
	require.Equal(t, http.StatusOK, registered.Code)
	node := decodeData[domain.RegisterNodeResponse](t, registered.Body.Bytes())
	require.NotEmpty(t, node.NodeID)
	require.Equal(t, http.StatusUnauthorized, register().Code)
	update := hosting.Update{
		Kind: hosting.SandboxKind, HostID: host, JobID: job.JobID, Generation: job.Generation,
		LeaseToken: job.LeaseToken, State: "ready", NodeID: node.NodeID,
	}
	raw, err := json.Marshal(update)
	require.NoError(t, err)
	done := hostingRequest(t, server, "POST", "/api/v1/hosting/jobs/complete", string(raw), "", key)
	require.Equal(t, http.StatusOK, done.Code)
	ready := start("{}")
	require.Equal(t, http.StatusOK, ready.Code)
	require.Equal(t, &hosting.SandboxView{Status: "ready", NodeID: node.NodeID, Generation: 1},
		decodeData[hosting.SandboxStatus](t, ready.Body.Bytes()).Sandbox)
	for _, secret := range []string{key, job.LeaseToken, job.RegistrationToken, node.APIKey} {
		require.False(t, bytes.Contains(ready.Body.Bytes(), []byte(secret)))
	}

	resumeBody := `{"node_id":"` + node.NodeID + `"}`
	restartBody := `{"node_id":"` + node.NodeID + `","generation":1}`
	readyWithoutRestart := reopen("resume", resumeBody)
	require.Equal(t, http.StatusOK, readyWithoutRestart.Code)
	require.Equal(
		t,
		"ready",
		decodeData[hosting.SandboxStatus](t, readyWithoutRestart.Body.Bytes()).Sandbox.Status,
	)
	require.Equal(t, http.StatusBadRequest, reopen("restart", resumeBody).Code)
	require.Equal(
		t,
		http.StatusNotFound,
		reopen("restart", `{"node_id":"other-node","generation":1}`).Code,
	)
	for range 2 {
		restarted := reopen("restart", restartBody)
		require.Equal(t, http.StatusOK, restarted.Code, restarted.Body.String())
		require.Equal(t, &hosting.SandboxView{Status: "queued", NodeID: node.NodeID, Generation: 2},
			decodeData[hosting.SandboxStatus](t, restarted.Body.Bytes()).Sandbox)
	}
	require.NotEqual(
		t,
		http.StatusOK,
		hostingRequest(
			t,
			server,
			"POST",
			"/api/v1/hosting/jobs/complete",
			string(raw),
			"",
			key,
		).Code,
	)
	claimed = claim(body, key)
	require.Equal(t, http.StatusOK, claimed.Code)
	reused := decodeData[struct{ Job *hosting.Job }](t, claimed.Body.Bytes()).Job
	require.NotNil(t, reused)
	require.Equal(t, job.EnvironmentID, reused.EnvironmentID)
	require.Equal(t, node.NodeID, reused.NodeID)
	require.Empty(t, reused.RegistrationToken)
	update.JobID, update.LeaseToken, update.Generation = reused.JobID, reused.LeaseToken, reused.Generation
	raw, err = json.Marshal(update)
	require.NoError(t, err)
	require.Equal(
		t,
		http.StatusOK,
		hostingRequest(
			t,
			server,
			"POST",
			"/api/v1/hosting/jobs/complete",
			string(raw),
			"",
			key,
		).Code,
	)
	stale := reopen("restart", restartBody)
	require.Equal(t, http.StatusOK, stale.Code)
	require.Equal(t, &hosting.SandboxView{Status: "ready", NodeID: node.NodeID, Generation: 2},
		decodeData[hosting.SandboxStatus](t, stale.Body.Bytes()).Sandbox)

	update.State, update.NodeID = "sleeping", ""
	raw, err = json.Marshal(update)
	require.NoError(t, err)
	require.Equal(
		t,
		http.StatusOK,
		hostingRequest(
			t,
			server,
			"POST",
			"/api/v1/hosting/jobs/complete",
			string(raw),
			"",
			key,
		).Code,
	)
	resumed := reopen("resume", resumeBody)
	require.Equal(t, http.StatusOK, resumed.Code)
	require.Equal(t, &hosting.SandboxView{Status: "queued", NodeID: node.NodeID, Generation: 3},
		decodeData[hosting.SandboxStatus](t, resumed.Body.Bytes()).Sandbox)
}
