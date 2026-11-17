package manager

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/modelcatalog"
)

const catalogPath = "/api/v1/user/self/hosting-admin/catalog"

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func catalogIdentityProof(t *testing.T, userID, email, path, secret string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"user_id": userID, "identity_key": email, "home_region": "us", "region": "us",
		"subject": "user_catalog_fixture", "method": "GET", "path": path,
		"timestamp": time.Now().Unix(),
	})
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("pax-browser-identity-v1\n"))
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestHostingCatalogUsesPathBoundWorkerIdentityBeforeSharedCache(t *testing.T) {
	const admin, member = "catalog-operator@example.invalid", "catalog-member@example.invalid"
	secret := strings.Repeat("synthetic-catalog-secret-", 2)
	store := NewMemoryStore(time.Now)
	_, err := store.EnsureRegionalUser(t.Context(), "usr_catalog_operator", admin, "user")
	require.NoError(t, err)
	_, err = store.EnsureRegionalUser(t.Context(), "usr_catalog_member", member, "user")
	require.NoError(t, err)
	s := newServer(Config{
		Region: "us", RegionProvisioningSecret: secret, AdminEmails: map[string]bool{admin: true},
	}, store)
	t.Cleanup(func() { require.NoError(t, s.CloseTransportStore(t.Context())) })
	var calls atomic.Int32
	s.modelCatalog = modelcatalog.New(modelcatalog.Options{Transport: catalogTransport(
		func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			assert.Equal(t, "https://openrouter.ai/api/v1/models", request.URL.String())
			assert.Equal(t, http.MethodGet, request.Method)
			for _, header := range []string{
				"Authorization", "Cookie", "Cf-Access-Jwt-Assertion", "X-Pax-Identity-Assertion", "X-User-Email",
			} {
				assert.Empty(t, request.Header.Get(header), header)
			}
			return &http.Response{StatusCode: 200,
				Header: http.Header{"Content-Type": []string{"application/json"}},
				Body:   io.NopCloser(strings.NewReader(`{"data":[]}`)),
			}, nil
		},
	)})
	call := func(query, userID, email, proofPath string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", catalogPath+query, nil)
		if userID != "" {
			request.Header.Set("X-Pax-Identity-Assertion",
				catalogIdentityProof(t, userID, email, proofPath, secret))
		}
		response := httptest.NewRecorder()
		s.routes().ServeHTTP(response, request)
		return response
	}
	query := "?provider=openrouter"
	require.Equal(t, 401, call(query, "", "", "").Code)
	require.Equal(t, 403, call(query, "usr_catalog_member", member, catalogPath).Code)
	require.Equal(t, 401, call(query, "usr_catalog_operator", admin, hostingAdminPath).Code)
	require.Zero(t, calls.Load())
	for range 2 {
		response := call(query, "usr_catalog_operator", admin, catalogPath)
		require.Equal(t, 200, response.Code, response.Body.String())
		require.Contains(t, response.Header().Get("Cache-Control"), "no-store")
		catalog := decodeData[modelcatalog.Catalog](t, response.Body.Bytes())
		require.Equal(t, "openrouter", catalog.Source)
		require.NotEmpty(t, catalog.FetchedAt)
		require.Empty(t, catalog.Models)
	}
	require.EqualValues(t, 1, calls.Load())
	for _, invalid := range []string{
		"", "?provider=anthropic", "?provider=openrouter&provider=openrouter",
		"?provider=openrouter&url=https://example.invalid", "?provider=%ZZ",
	} {
		require.Equal(t, 400, call(invalid, "usr_catalog_operator", admin, catalogPath).Code)
	}
	require.Equal(t, 403, call(query, "usr_catalog_member", member, catalogPath).Code)
	require.Equal(t, 401, call(query, "usr_catalog_operator", admin, hostingAdminPath).Code)
	s.cfg.AdminEmails[admin] = false
	require.Equal(t, 403, call(query, "usr_catalog_operator", admin, catalogPath).Code)
	require.EqualValues(t, 1, calls.Load())
}

func TestHostingCatalogSanitizesUpstreamFailures(t *testing.T) {
	s, _ := newConfigTestServer(t, Config{})
	s.modelCatalog = modelcatalog.New(modelcatalog.Options{Transport: catalogTransport(
		func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503,
				Header: http.Header{"Content-Type": []string{"application/json"}},
				Body:   io.NopCloser(strings.NewReader(`{"error":"private-upstream-detail"}`)),
			}, nil
		},
	)})
	response := callAPI(t, s, "GET", catalogPath+"?provider=openrouter", "admin@example.com", nil)
	require.Equal(t, 503, response.Code)
	require.Contains(t, response.Body.String(), "model_catalog_unavailable")
	require.NotContains(t, response.Body.String(), "private-upstream-detail")
}
