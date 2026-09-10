package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/nodepolicy"
)

func TestNodePublicManifestMatchesRegisteredRoutes(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	actual := map[string]bool{}
	for _, route := range srv.engine(":0").Routes() {
		if nodepolicy.Managed(route.Path) {
			actual[route.Method+" "+route.Path] = true
		}
	}
	expected := map[string]bool{}
	for _, route := range nodepolicy.Routes() {
		key := route.Method + " " + route.Path
		require.False(t, expected[key], "duplicate manifest entry: %s", key)
		require.NotEmpty(t, route.Auth, key)
		require.NotEmpty(t, route.OwnerBoundary, key)
		require.NotEmpty(t, route.RateClass, key)
		require.NotEmpty(t, route.Caller, key)
		expected[key] = true
	}
	require.Equal(t, expected, actual, "review new machine routes before exposing them")
}

func TestNodePublicRoutesRejectMissingAndInvalidCredentials(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	handler := srv.routes()
	for _, route := range nodepolicy.Routes() {
		if route.Auth == "anonymous" || route.Auth == "poll-token" {
			continue
		}
		for _, key := range []string{"", "pax_invalid"} {
			t.Run(route.Method+" "+route.Path+" key="+key, func(t *testing.T) {
				req := httptest.NewRequest(
					route.Method,
					concreteNodePath(route.Path),
					strings.NewReader(`{"hostname":"probe","node":{"hostname":"probe"}}`),
				)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Pax-Key", key)
				req.Header.Set("X-User-Email", "admin@example.com")
				rec := httptest.NewRecorder()
				// The Hertz net/http adaptor requires a real connection for writes.
				switch route.Path {
				case routeNodeControlTunnel:
					srv.handleNodeControlTunnel(rec, req)
				case routeAgentACPTunnel:
					srv.handleAgentACPTunnel(rec, req)
				default:
					handler.ServeHTTP(rec, req)
				}
				require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
			})
		}
	}
}

func TestNodePublicSurfaceDeniesUnreviewedRoutes(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	h := srv.engine(":0")
	// A newly registered handler must remain unreachable until explicitly reviewed.
	h.GET("/api/v1/node/debug", func(_ context.Context, ctx *app.RequestContext) {
		ctx.String(http.StatusOK, "must not be exposed")
	})
	handler := hertzHTTPHandler{h}
	for _, path := range []string{
		"/api/v1/node/debug", "/api/v1/node/unknown", "/api/v1/agent/debug",
		"/api/v1/node", "/api/v1/node/status", "/api/v1/node/secrets/resolve",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		})
	}
}

func concreteNodePath(template string) string {
	parts := strings.Split(template, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[i] = "probe_" + strings.TrimPrefix(part, ":")
		}
	}
	return strings.Join(parts, "/")
}
