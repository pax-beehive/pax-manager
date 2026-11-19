package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestSandboxNodeHTTPCreatesAnOwnedNodeWithoutAgentOrModelCredentials(t *testing.T) {
	const ownerEmail = "pilot@example.com"
	const host = "aws-pilot"
	key := strings.Repeat("n", 32)
	store := NewMemoryStore(time.Now)
	owner, err := store.EnsureUser(t.Context(), ownerEmail, "Pilot", "user")
	require.NoError(t, err)
	server := newServer(Config{
		AllowLocalUserHeader: true, HostingSandboxHostKey: key, HostingSandboxHostID: host,
		HostingSandboxCloudURL: "https://node.example.com", HostingSandboxOwners: owner.UserID,
	}, store)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, server.CloseTransportStore(ctx))
	})

	get := func(user string) *httptest.ResponseRecorder {
		return hostingRequest(t, server, "GET", "/api/v1/sandbox", "", user, "")
	}
	start := func(body string) *httptest.ResponseRecorder {
		return hostingRequest(t, server, "POST", "/api/v1/sandbox", body, ownerEmail, "")
	}
	require.Equal(t, http.StatusUnauthorized, get("").Code)
	require.Equal(t, http.StatusNotFound, get("other@example.com").Code)
	initial := get(ownerEmail)
	require.Equal(t, http.StatusOK, initial.Code)
	require.Nil(t, decodeData[hosting.SandboxStatus](t, initial.Body.Bytes()).Sandbox)
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
	require.Equal(
		t,
		http.StatusUnauthorized,
		claim(`{"host_id":"aws-pilot","capacity":1}`, key).Code,
	)
	body := `{"kind":"node_only","host_id":"aws-pilot","capacity":1}`
	require.Equal(t, http.StatusUnauthorized, claim(body, "").Code)
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
	require.Equal(t, &hosting.SandboxView{Status: "ready", NodeID: node.NodeID},
		decodeData[hosting.SandboxStatus](t, ready.Body.Bytes()).Sandbox)
	for _, secret := range []string{key, job.LeaseToken, job.RegistrationToken, node.APIKey} {
		require.False(t, bytes.Contains(ready.Body.Bytes(), []byte(secret)))
	}
}
