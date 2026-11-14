package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

const configTestSecret = "sk-config-test-0123456789abcd"

// newConfigTestServer uses the production registry, so every feature
// defaults to the admin stage exactly as after a fresh deployment.
func newConfigTestServer(t *testing.T, cfg Config) (*Server, *MemoryStore) {
	t.Helper()
	cfg.AllowLocalUserHeader = true
	cfg.AdminEmails = map[string]bool{"admin@example.com": true}
	store := NewMemoryStore(time.Now)
	srv := newServer(cfg, store)
	srv.runtimeConfig = runtimeconfig.New(runtimeconfig.Options{
		Repository: store.RuntimeConfig(),
		Env:        runtimeConfigEnv(cfg),
		Region:     cfg.Region,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, srv.CloseTransportStore(ctx))
	})
	return srv, store
}

func callAPI(
	t *testing.T,
	srv *Server,
	method, path, email string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	switch typed := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case string:
		reader = bytes.NewReader([]byte(typed))
	default:
		raw, err := json.Marshal(typed)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, reader)
	r.Header.Set("Content-Type", "application/json")
	if email != "" {
		r.Header.Set("X-User-Email", email)
	}
	w := httptest.NewRecorder()
	srv.routes().ServeHTTP(w, r)
	return w
}

type errorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Error       string            `json:"error"`
		Feature     string            `json:"feature"`
		FieldErrors map[string]string `json:"field_errors"`
		Version     *int64            `json:"version"`
	} `json:"data"`
}

func decodeError(t *testing.T, w *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var envelope errorEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), w.Body.String())
	return envelope
}

func TestAdminConfigGivenNonAdminThenEveryRouteReturns403(t *testing.T) {
	srv, _ := newConfigTestServer(t, Config{})
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/config"},
		{http.MethodPut, "/api/v1/admin/config/search.url"},
		{http.MethodDelete, "/api/v1/admin/config/search.url?expected_version=0&reason=x"},
		{http.MethodPost, "/api/v1/admin/config/test"},
		{http.MethodGet, "/api/v1/admin/config/audit"},
		{http.MethodPost, "/api/v1/admin/config/audit/1/revert"},
	}
	for _, route := range routes {
		w := callAPI(t, srv, route.method, route.path, "member@example.com", `{}`)
		assert.Equal(t, http.StatusForbidden, w.Code, route.path)
		assert.Equal(t, runtimeconfig.CodeNotAdmin, decodeError(t, w).Data.Error, route.path)
		w = callAPI(t, srv, route.method, route.path, "", `{}`)
		assert.Equal(t, http.StatusUnauthorized, w.Code, route.path)
	}
}

func TestAdminConfigGivenAdminThenListsContractFieldsWithoutCredentials(t *testing.T) {
	srv, _ := newConfigTestServer(t, Config{SearchAPIKey: configTestSecret})
	w := callAPI(t, srv, http.MethodGet, "/api/v1/admin/config", "admin@example.com", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), configTestSecret)
	assert.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	for _, field := range []string{"region", "revision", "health", "entries"} {
		assert.Contains(t, envelope.Data, field)
	}
	var entries []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope.Data["entries"], &entries))
	require.NotEmpty(t, entries)
	for _, field := range []string{
		"key", "kind", "group", "label", "description", "type", "options", "secret", "value",
		"display", "has_value", "source", "env_var", "default_display", "apply",
		"pending_restart", "testable", "version", "updated_at", "updated_by",
	} {
		assert.Contains(t, entries[0], field)
	}
}

func TestAdminConfigGivenCredentialKeyThenRejectsAndPlainPutSucceeds(t *testing.T) {
	srv, _ := newConfigTestServer(t, Config{})
	w := callAPI(t, srv, http.MethodPut, "/api/v1/admin/config/search.api_key", "admin@example.com",
		map[string]any{"value": configTestSecret, "expected_version": 0, "reason": "rotate"})
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, runtimeconfig.CodeUnknownKey, decodeError(t, w).Data.Error)
	assert.NotContains(t, w.Body.String(), configTestSecret)

	w = callAPI(t, srv, http.MethodPut, "/api/v1/admin/config/search.index", "admin@example.com",
		map[string]any{"value": "pax-hk", "expected_version": 0, "reason": "hk index"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	entry := decodeData[runtimeconfig.EntryView](t, w.Body.Bytes())
	assert.Equal(t, "db", entry.Source)
	assert.Equal(t, int64(1), entry.Version)
	assert.Equal(t, "admin@example.com", *entry.UpdatedBy)
}

func TestAdminConfigGivenBadRequestsThenUsesDocumentedStatusAndCodes(t *testing.T) {
	srv, _ := newConfigTestServer(t, Config{})
	put := func(key string, body any) *httptest.ResponseRecorder {
		return callAPI(
			t,
			srv,
			http.MethodPut,
			"/api/v1/admin/config/"+key,
			"admin@example.com",
			body,
		)
	}
	w := put("unknown.key", map[string]any{"value": "x", "expected_version": 0, "reason": "r"})
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, runtimeconfig.CodeUnknownKey, decodeError(t, w).Data.Error)

	w = put(
		"search.url",
		map[string]any{"value": "not a url", "expected_version": 0, "reason": "r"},
	)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	failure := decodeError(t, w)
	assert.Equal(t, runtimeconfig.CodeValidationFailed, failure.Data.Error)
	assert.Contains(t, failure.Data.FieldErrors, "value")

	w = put("search.url", map[string]any{"value": "http://es:9200", "reason": "r"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, decodeError(t, w).Data.FieldErrors, "expected_version")

	w = put(
		"search.url",
		map[string]any{"value": "http://es:9200", "expected_version": 0, "reason": "r"},
	)
	require.Equal(t, http.StatusOK, w.Code)
	w = put(
		"search.url",
		map[string]any{"value": "http://es2:9200", "expected_version": 0, "reason": "r"},
	)
	assert.Equal(t, http.StatusConflict, w.Code)
	conflict := decodeError(t, w)
	assert.Equal(t, runtimeconfig.CodeVersionConflict, conflict.Data.Error)
	assert.Equal(t, int64(1), *conflict.Data.Version)

	w = callAPI(t, srv, http.MethodDelete,
		"/api/v1/admin/config/search.url?expected_version=1&reason="+url.QueryEscape("back to env"),
		"admin@example.com", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "default", decodeData[runtimeconfig.EntryView](t, w.Body.Bytes()).Source)
}

func TestAdminConfigGivenEnvCredentialsThenNoResponseOrLogContainsThem(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	)
	t.Cleanup(func() { slog.SetDefault(previous) })
	srv, _ := newConfigTestServer(t, Config{
		SearchAPIKey: configTestSecret, OpenAIAPIKey: "sk-openai-env-only-9999",
		DeepSeekAPIKey: "sk-deepseek-env-only-8888",
	})
	var bodies []string
	record := func(w *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		bodies = append(bodies, w.Body.String())
		return w
	}
	w := record(
		callAPI(t, srv, http.MethodPut, "/api/v1/admin/config/search.url", "admin@example.com",
			map[string]any{"value": "http://127.0.0.1:1", "expected_version": 0, "reason": "set"}),
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	record(callAPI(t, srv, http.MethodPut, "/api/v1/admin/config/search.url", "admin@example.com",
		map[string]any{"value": "http://127.0.0.1:2", "expected_version": 1, "reason": "move"}))
	record(callAPI(t, srv, http.MethodGet, "/api/v1/admin/config", "admin@example.com", nil))
	audit := record(
		callAPI(t, srv, http.MethodGet, "/api/v1/admin/config/audit", "admin@example.com", nil),
	)
	page := decodeData[runtimeconfig.AuditPage](t, audit.Body.Bytes())
	require.Len(t, page.Items, 2)
	w = record(callAPI(t, srv, http.MethodPost,
		"/api/v1/admin/config/audit/"+jsonNumber(page.Items[0].ID)+"/revert", "admin@example.com",
		map[string]any{"expected_version": 2, "reason": "undo"}))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "http://127.0.0.1:1", srv.runtimeConfig.Search().URL)
	assert.Equal(t, configTestSecret, srv.runtimeConfig.Search().APIKey)
	record(callAPI(t, srv, http.MethodPost, "/api/v1/admin/config/test", "admin@example.com",
		map[string]any{"group": "search"}))
	w = record(callAPI(t, srv, http.MethodPost, "/api/v1/admin/config/test", "admin@example.com",
		map[string]any{"group": "search", "overrides": map[string]string{"search.api_key": "x"}}))
	assert.Equal(t, http.StatusNotFound, w.Code, "credentials cannot be overridden even for a test")
	for _, plaintext := range []string{
		configTestSecret, "sk-openai-env-only-9999", "sk-deepseek-env-only-8888",
	} {
		for _, body := range bodies {
			assert.NotContains(t, body, plaintext)
		}
		assert.NotContains(t, logs.String(), plaintext)
	}
}

func jsonNumber(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestAdminConfigTestGivenSearchOverridesThenUsesUnsavedURLWithoutWriting(t *testing.T) {
	var auth atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if r.URL.Path == "/pax-search-v1/_mapping" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer backend.Close()
	srv, _ := newConfigTestServer(t, Config{SearchAPIKey: "env-key"})
	w := callAPI(t, srv, http.MethodPost, "/api/v1/admin/config/test", "admin@example.com",
		map[string]any{"group": "search", "overrides": map[string]string{
			"search.url": backend.URL,
		}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	result := decodeData[runtimeconfig.TestResult](t, w.Body.Bytes())
	assert.True(t, result.OK, result.Message)
	assert.Equal(t, "ApiKey env-key", auth.Load(), "tests use the environment credential")
	assert.Equal(t, "", srv.runtimeConfig.Search().URL, "a test never writes")
	assert.Equal(t, int64(0), srv.runtimeConfig.Snapshot().Revision)

	w = callAPI(t, srv, http.MethodPost, "/api/v1/admin/config/test", "admin@example.com",
		map[string]any{"group": "voice"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSearchGivenURLChangedAtRuntimeThenNextRequestUsesNewBackend(t *testing.T) {
	var oldHits, newHits atomic.Int32
	oldBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oldHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer oldBackend.Close()
	newBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		newHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer newBackend.Close()
	srv, _ := newConfigTestServer(
		t,
		Config{SearchURL: oldBackend.URL, SearchIndex: "pax-search-v1"},
	)
	search := func() {
		callAPI(t, srv, http.MethodPost, "/api/v1/user/self/search", "member@example.com",
			map[string]any{"query": "deploy"})
	}
	search()
	require.Positive(t, oldHits.Load(), "environment URL is used without overrides")
	before := oldHits.Load()

	_, err := srv.runtimeConfig.Set(
		t.Context(),
		"search.url",
		json.RawMessage(`"`+newBackend.URL+`"`),
		0,
		"move search",
		"admin@example.com",
	)
	require.NoError(t, err)
	search()
	assert.Positive(t, newHits.Load(), "the next search uses the new URL without a restart")
	assert.Equal(t, before, oldHits.Load())
}

func TestSearchGivenNoURLThenReportsUnavailableAsBefore(t *testing.T) {
	srv, _ := newConfigTestServer(t, Config{})
	w := callAPI(t, srv, http.MethodPost, "/api/v1/user/self/search", "member@example.com",
		map[string]any{"query": "deploy"})
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "search is unavailable", decodeError(t, w).Message)
}

func TestUserFeaturesGivenAdminStageThenMembersSeeFalseAndPreviewMatches(t *testing.T) {
	srv, _ := newConfigTestServer(t, Config{})
	features := func(email, query string) map[string]bool {
		w := callAPI(t, srv, http.MethodGet, "/api/v1/user/self/features"+query, email, nil)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		data := decodeData[struct {
			Revision int64           `json:"revision"`
			Features map[string]bool `json:"features"`
		}](t, w.Body.Bytes())
		return data.Features
	}
	member := features("member@example.com", "")
	assert.False(t, member["secretary"])
	assert.False(t, member["notifications"])
	assert.Len(t, member, 7)
	assert.True(t, features("admin@example.com", "")["secretary"])
	assert.Equal(t, member, features("admin@example.com", "?as=user"))
	assert.Equal(t, member, features("member@example.com", "?as=admin"))

	_, err := srv.runtimeConfig.Set(
		t.Context(),
		"feature.secretary",
		json.RawMessage(
			`{"stage":"beta","beta_emails":["Member@Example.com"]}`,
		),
		0,
		"beta",
		"admin@example.com",
	)
	require.NoError(t, err)
	assert.True(t, features("member@example.com", "")["secretary"])
	assert.False(t, features("other@example.com", "")["secretary"])
	w := callAPI(t, srv, http.MethodGet, "/api/v1/user/self/features", "member@example.com", nil)
	assert.NotContains(t, w.Body.String(), "beta_emails")
	assert.NotContains(t, w.Body.String(), "stage")
}

type downRepository struct{ domain.RuntimeConfigRepository }

func (downRepository) RuntimeConfigRevision(context.Context) (int64, error) {
	return 0, context.DeadlineExceeded
}

func TestHealthAndFeaturesGivenConfigDatabaseDownThenDegradedButServing(t *testing.T) {
	srv, store := newConfigTestServer(t, Config{})
	srv.runtimeConfig = runtimeconfig.New(runtimeconfig.Options{
		Repository: downRepository{store.RuntimeConfig()},
		Env:        runtimeConfigEnv(srv.cfg),
	})
	stop := srv.runtimeConfig.Start(t.Context())
	defer stop()
	for _, path := range []string{"/health", "/api/v1/health"} {
		w := callAPI(t, srv, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "degraded", decodeData[map[string]any](t, w.Body.Bytes())["config"], path)
	}
	w := callAPI(t, srv, http.MethodGet, "/api/v1/user/self/features", "admin@example.com", nil)
	assert.Equal(t, http.StatusOK, w.Code)

	healthy, _ := newConfigTestServer(t, Config{})
	require.NoError(t, healthy.runtimeConfig.Refresh(t.Context()))
	w = callAPI(t, healthy, http.MethodGet, "/api/v1/health", "", nil)
	assert.Equal(t, "ok", decodeData[map[string]any](t, w.Body.Bytes())["config"])
}

func TestFeatureGateGivenAdminStageThenMembersGet404AndAdminsPass(t *testing.T) {
	srv, _ := newConfigTestServer(t, Config{})
	routes := []struct{ method, path, feature string }{
		{http.MethodGet, "/api/v1/user/self/secretary", "secretary"},
		{http.MethodPatch, "/api/v1/user/self/secretary", "secretary"},
		{http.MethodPost, "/api/v1/user/self/secretary/run", "secretary"},
		{http.MethodGet, "/api/v1/user/self/secretary/tasks", "secretary"},
		{http.MethodGet, "/api/v1/user/self/secretary/summaries", "secretary"},
		{http.MethodGet, "/api/v1/user/self/secretary/progress", "secretary"},
		{http.MethodPut, "/api/v1/user/self/secretary/guidance", "secretary"},
		{http.MethodPut, "/api/v1/user/self/notifications/receivers/web-push", "notifications"},
		{http.MethodPost, "/api/v1/user/self/notifications/receivers/rcv_1/test", "notifications"},
		{http.MethodGet, "/api/v1/user/self/teams", "collaboration"},
		{http.MethodGet, "/api/v1/user/self/friends", "collaboration"},
		{http.MethodGet, "/api/v1/user/self/envelopes", "collaboration"},
		{http.MethodGet, "/api/v1/user/self/team-invites", "collaboration"},
		{http.MethodGet, "/api/v1/user/self/knowledge-capsules", "knowledge_capsules"},
	}
	for _, route := range routes {
		w := callAPI(t, srv, route.method, route.path, "member@example.com", `{}`)
		assert.Equal(t, http.StatusNotFound, w.Code, route.path)
		failure := decodeError(t, w)
		assert.Equal(t, runtimeconfig.CodeFeatureUnavailable, failure.Data.Error, route.path)
		assert.Equal(t, route.feature, failure.Data.Feature, route.path)

		w = callAPI(t, srv, route.method, route.path, "admin@example.com", `{}`)
		if w.Code == http.StatusNotFound {
			assert.NotEqual(
				t,
				runtimeconfig.CodeFeatureUnavailable,
				decodeError(t, w).Data.Error,
				route.path,
			)
		}
		w = callAPI(t, srv, route.method, route.path, "", `{}`)
		assert.NotEqual(
			t,
			http.StatusNotFound,
			w.Code,
			"unauthenticated calls keep their auth error",
		)
	}
	// Listing and revoking push receivers stay available.
	w := callAPI(
		t,
		srv,
		http.MethodGet,
		"/api/v1/user/self/notifications/receivers",
		"member@example.com",
		nil,
	)
	assert.NotEqual(t, http.StatusNotFound, w.Code)

	_, err := srv.runtimeConfig.Set(t.Context(), "feature.secretary",
		json.RawMessage(`{"stage":"ga","beta_emails":[]}`), 0, "launch", "admin@example.com")
	require.NoError(t, err)
	w = callAPI(t, srv, http.MethodGet, "/api/v1/user/self/secretary", "member@example.com", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())

	_, err = srv.runtimeConfig.Set(t.Context(), "feature.secretary",
		json.RawMessage(`{"stage":"off","beta_emails":[]}`), 1, "incident", "admin@example.com")
	require.NoError(t, err)
	w = callAPI(t, srv, http.MethodGet, "/api/v1/user/self/secretary", "admin@example.com", nil)
	assert.Equal(t, http.StatusNotFound, w.Code, "off blocks administrators too")
}

func TestFeatureGateGivenRegisteredRoutesThenEveryGatedSurfaceIsCovered(t *testing.T) {
	srv, _ := newConfigTestServer(t, Config{})
	engine := srv.engine(":0")
	markers := map[string]string{
		"/secretary":            runtimeconfig.FeatureSecretary,
		"/teams":                runtimeconfig.FeatureCollaboration,
		"/team-invites":         runtimeconfig.FeatureCollaboration,
		"/friends":              runtimeconfig.FeatureCollaboration,
		"/envelopes":            runtimeconfig.FeatureCollaboration,
		"/knowledge-capsules":   runtimeconfig.FeatureKnowledgeCapsules,
		"/knowledge-injections": runtimeconfig.FeatureKnowledgeCapsules,
	}
	var gated int
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, "/api/v1/user/") {
			continue
		}
		for marker, feature := range markers {
			if !strings.Contains(route.Path, marker) {
				continue
			}
			got, ok := gatedFeature(route.Method, route.Path)
			assert.True(t, ok, "%s %s is not gated", route.Method, route.Path)
			assert.Equal(t, feature, got, route.Path)
			gated++
		}
	}
	assert.Greater(t, gated, 40)
	_, ok := gatedFeature(http.MethodGet, "/api/v1/user/:user_id/developer/summaries")
	assert.False(t, ok, "admin developer tools are not feature gates")
}

func TestFeatureForOwnerGivenStagesThenResolvesOwnerLazily(t *testing.T) {
	srv, store := newConfigTestServer(t, Config{})
	admin, err := store.EnsureUser(t.Context(), "admin@example.com", "Admin", "admin")
	require.NoError(t, err)
	member, err := store.EnsureUser(t.Context(), "member@example.com", "Member", "user")
	require.NoError(t, err)
	assert.True(t, srv.featureForOwner(t.Context(), admin.UserID, runtimeconfig.FeatureSecretary))
	assert.False(t, srv.featureForOwner(t.Context(), member.UserID, runtimeconfig.FeatureSecretary))
	assert.False(t, srv.featureForOwner(t.Context(), "usr_missing", runtimeconfig.FeatureSecretary))
	_, err = srv.runtimeConfig.Set(t.Context(), "feature.notifications",
		json.RawMessage(`{"stage":"ga","beta_emails":[]}`), 0, "launch", "admin@example.com")
	require.NoError(t, err)
	assert.True(
		t,
		srv.featureForOwner(t.Context(), member.UserID, runtimeconfig.FeatureNotifications),
	)
}

func TestRuntimeConfigEnvGivenLoadedConfigThenMatchesPreviousBehavior(t *testing.T) {
	cfg := Config{
		SearchURL: "http://es:9200", SearchIndex: "custom", SearchAPIKey: "key-from-env",
		VoiceProvider: "qwen", DashScopeAPIKey: "dash", VoiceQwenModel: "m", VoiceOpenAIDelay: "high",
		VoiceAdminOnly: true,
	}
	snapshot := newRuntimeConfigStore(cfg, storage.NewMemoryStore(time.Now)).Snapshot()
	assert.Equal(
		t,
		runtimeconfig.SearchConfig{URL: "http://es:9200", Index: "custom", APIKey: "key-from-env"},
		snapshot.Search(),
	)
	voiceCfg := snapshot.Voice()
	assert.Equal(t, "qwen", voiceCfg.Provider)
	assert.Equal(t, "dash", voiceCfg.Qwen.APIKey)
	assert.Equal(t, "high", voiceCfg.OpenAI.Delay)
	assert.False(t, snapshot.Bool("transport.journal_gc_disabled"))
	settings := voiceSettings(cfg, voiceCfg)
	assert.Equal(t, "qwen", settings.Provider)
	assert.Equal(t, "m", settings.Qwen.Model)
}
