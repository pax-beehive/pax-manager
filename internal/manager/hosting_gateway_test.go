package manager

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func hostingTestServer(t *testing.T, gateway string) *Service {
	t.Helper()
	s := newServer(Config{
		AllowLocalUserHeader:   true,
		HostingHostKey:         strings.Repeat("h", 32),
		HostingTokenSecret:     strings.Repeat("s", 32),
		HostingPublicURL:       "https://api.example.com",
		HostingCloudURL:        "https://wsapi.example.com",
		HostingGatewayURL:      gateway,
		HostingGatewayKey:      strings.Repeat("g", 32),
		HostingModel:           "trial-model",
		HostingGrantMicrousd:   1000000,
		HostingGrantTokens:     200000,
		HostingGlobalMicrousd:  100000000,
		HostingInputPrice:      1000000,
		HostingOutputPrice:     5000000,
		HostingMaxOutputTokens: 2048,
	}, NewMemoryStore(time.Now))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, s.CloseTransportStore(ctx))
	})
	return s
}

func hostingRequest(
	t *testing.T,
	s *Service,
	method, path, body, owner, token string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if owner != "" {
		request.Header.Set("X-User-Email", owner)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, request)
	return recorder
}

func TestStarterHTTPScopesOwnerAndDisabledConfiguration(t *testing.T) {
	s := hostingTestServer(t, "https://upstream.example.com/v1")
	response := hostingRequest(t, s, "POST", "/api/v1/starter", "{}", "alice@example.com", "")
	require.Equal(t, 200, response.Code)
	first := decodeData[hosting.Status](t, response.Body.Bytes())
	require.NotNil(t, first.Starter)
	response = hostingRequest(t, s, "POST", "/api/v1/starter", "{}", "alice@example.com", "")
	second := decodeData[hosting.Status](t, response.Body.Bytes())
	require.Equal(t, first.Starter.EnvironmentID, second.Starter.EnvironmentID)
	require.Equal(t, first.Starter.Quota.GrantedMicrousd, second.Starter.Quota.GrantedMicrousd)
	response = hostingRequest(t, s, "GET", "/api/v1/starter", "", "bob@example.com", "")
	other := decodeData[hosting.Status](t, response.Body.Bytes())
	require.Nil(t, other.Starter)
	response = hostingRequest(
		t,
		s,
		"POST",
		"/api/v1/hosting/jobs/claim",
		`{"host_id":"a","capacity":1}`,
		"alice@example.com",
		"",
	)
	require.Equal(t, 401, response.Code)
	s.hosting.Config.GatewayKey = ""
	response = hostingRequest(t, s, "GET", "/api/v1/starter", "", "alice@example.com", "")
	disabled := decodeData[hosting.Status](t, response.Body.Bytes())
	require.False(t, disabled.Enabled)
}

func TestManagerModelEndpointIsRetired(t *testing.T) {
	s := hostingTestServer(t, "https://model.example.com/v1")
	response := hostingRequest(
		t,
		s,
		"POST",
		"/api/v1/hosting/model/chat/completions",
		`{"messages":[{"role":"user","content":"do not forward"}]}`,
		"",
		"",
	)
	require.Equal(t, 410, response.Code)
	require.Contains(t, response.Body.String(), "model_gateway_moved")
	require.Empty(t, response.Header().Get("Location"))
}
