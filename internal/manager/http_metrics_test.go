package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	hertzserver "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestHTTPMetricsObserve(t *testing.T) {
	metrics := newHTTPMetrics()
	h := hertzserver.New()
	h.Use(metrics.observe)
	h.GET("/sessions/:id", func(_ context.Context, ctx *app.RequestContext) {
		ctx.Status(http.StatusOK)
	})
	h.GET("/error", func(_ context.Context, ctx *app.RequestContext) {
		ctx.Status(http.StatusInternalServerError)
	})
	h.GET("/events", func(_ context.Context, ctx *app.RequestContext) {
		ctx.Response.Header.SetContentType("text/event-stream; charset=utf-8")
		ctx.Status(http.StatusOK)
	})
	h.GET("/tunnel", func(_ context.Context, ctx *app.RequestContext) {
		ctx.Status(http.StatusSwitchingProtocols)
	})
	appHandler := hertzHTTPHandler{h}
	for _, request := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/sessions/session-123", http.StatusOK},
		{http.MethodGet, "/sessions/session-456", http.StatusOK},
		{"CUSTOM-A", "/unknown-123", http.StatusNotFound},
		{"CUSTOM-B", "/unknown-456", http.StatusNotFound},
		{http.MethodGet, "/error", http.StatusInternalServerError},
		{http.MethodGet, "/events", http.StatusOK},
		{http.MethodGet, "/tunnel", http.StatusSwitchingProtocols},
	} {
		response := httptest.NewRecorder()
		appHandler.ServeHTTP(response, httptest.NewRequest(request.method, request.path, nil))
		require.Equal(t, request.status, response.Code, "%s %s", request.method, request.path)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	metrics.handler(netip.MustParseAddr("192.0.2.10"), "test-token", func() bool { return true }).
		ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code)
	body := response.Body.String()
	require.Contains(
		t,
		body,
		`pax_http_requests_total{method="GET",route="/sessions/:id",status="200"} 2`,
	)
	require.Contains(
		t,
		body,
		`pax_http_requests_total{method="OTHER",route="unmatched",status="404"} 2`,
	)
	require.Contains(t, body, `pax_http_requests_total{method="GET",route="/error",status="500"} 1`)
	require.Contains(
		t,
		body,
		`pax_http_request_duration_seconds_count{method="GET",route="/error",status="500"} 1`,
	)
	require.Contains(
		t,
		body,
		`pax_http_requests_total{method="GET",route="/events",status="200"} 1`,
	)
	require.Contains(
		t,
		body,
		`pax_http_requests_total{method="GET",route="/tunnel",status="101"} 1`,
	)
	require.NotContains(
		t,
		body,
		`pax_http_request_duration_seconds_count{method="GET",route="/events"`,
	)
	require.NotContains(
		t,
		body,
		`pax_http_request_duration_seconds_count{method="GET",route="/tunnel"`,
	)
	require.NotContains(t, body, "session-123")
	require.NotContains(t, body, "unknown-123")
	require.NotContains(t, body, "CUSTOM-A")
}

func TestHTTPMetricsScrapeAccess(t *testing.T) {
	handler := newHTTPMetrics().handler(netip.MustParseAddr("192.0.2.10"), "test-token", func() bool {
		return true
	})
	for _, tc := range []struct {
		name, method, path, peer, authorization string
		status                                  int
	}{
		{"wrong peer", http.MethodGet, "/metrics", "192.0.2.11:1234", "Bearer test-token", http.StatusForbidden},
		{"missing token", http.MethodGet, "/metrics", "192.0.2.10:1234", "", http.StatusUnauthorized},
		{"wrong token", http.MethodGet, "/metrics", "192.0.2.10:1234", "Bearer wrong", http.StatusUnauthorized},
		{"other path", http.MethodGet, "/health", "192.0.2.10:1234", "Bearer test-token", http.StatusNotFound},
		{"other method", http.MethodPost, "/metrics", "192.0.2.10:1234", "Bearer test-token", http.StatusMethodNotAllowed},
		{"valid", http.MethodGet, "/metrics", "192.0.2.10:1234", "Bearer test-token", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.RemoteAddr = tc.peer
			request.Header.Set("Authorization", tc.authorization)
			request.Header.Set("X-Forwarded-For", "192.0.2.10")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, tc.status, response.Code)
			if tc.status == http.StatusUnauthorized {
				require.Equal(t, "Bearer", response.Header().Get("WWW-Authenticate"))
			}
			if tc.status == http.StatusOK {
				require.Contains(t, response.Body.String(), "go_goroutines")
			} else {
				require.NotContains(t, response.Body.String(), "go_goroutines")
			}
		})
	}
}

func TestHTTPMetricsFeatureGateUpdatesLive(t *testing.T) {
	store := runtimeconfig.New(runtimeconfig.Options{
		Repository: storage.NewMemoryStore(time.Now).RuntimeConfig(),
	})
	srv := &Service{runtimeConfig: store}
	handler := newHTTPMetrics().handler(netip.MustParseAddr("192.0.2.10"), "test-token", srv.metricsEnabled)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("Authorization", "Bearer test-token")

	for version, stage := range []string{"off", "admin", "ga", "off"} {
		_, err := store.Set(t.Context(), runtimeconfig.FeatureKey(runtimeconfig.FeatureMetrics),
			json.RawMessage(`{"stage":"`+stage+`","beta_emails":[]}`), int64(version),
			"test metrics gate", "admin@example.com")
		require.NoError(t, err)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		expected := http.StatusNotFound
		if stage == runtimeconfig.StageGA {
			expected = http.StatusOK
		}
		require.Equal(t, expected, response.Code, stage)
	}
}

func TestHTTPMetricsTracksOpenWebSocketsByKind(t *testing.T) {
	srv := &Service{httpMetrics: newHTTPMetrics()}
	openControl := srv.trackWebSocket(webSocketControl)
	openAgent := srv.trackWebSocket(webSocketAgent)
	openReplacement := srv.trackWebSocket(webSocketAgent)
	handler := srv.httpMetrics.handler(
		netip.MustParseAddr("192.0.2.10"),
		"test-token",
		func() bool {
			return true
		},
	)
	scrape := func() string {
		request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		request.RemoteAddr = "192.0.2.10:1234"
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		return response.Body.String()
	}
	metrics := scrape()
	require.Contains(t, metrics, `pax_websocket_connections{kind="agent"} 2`)
	require.Contains(t, metrics, `pax_websocket_connections{kind="control"} 1`)
	require.Contains(t, metrics, `pax_websocket_connections{kind="client"} 0`)
	openAgent()
	require.Contains(t, scrape(), `pax_websocket_connections{kind="agent"} 1`)
	openReplacement()
	openControl()
	require.Contains(t, scrape(), `pax_websocket_connections{kind="control"} 0`)
}
