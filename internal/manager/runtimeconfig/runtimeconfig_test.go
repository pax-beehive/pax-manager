package runtimeconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

const testSecret = "sk-env-only-0123456789abcdef"

func principal(email string, admin bool) domain.UserPrincipal {
	return domain.UserPrincipal{
		User:    domain.User{UserID: "usr_" + email, Email: email},
		IsAdmin: admin,
	}
}

func TestFeatureAllowsGivenStageAndPrincipalThenFollowsStageTable(t *testing.T) {
	admin := principal("root@example.com", true)
	beta := principal("Beta@Example.com ", false)
	member := principal("member@example.com", false)
	anonymous := domain.UserPrincipal{}
	betaList := []string{"beta@example.com"}
	cases := []struct {
		name    string
		feature Feature
		who     domain.UserPrincipal
		want    bool
	}{
		{"off blocks admins", Feature{Stage: StageOff}, admin, false},
		{"off blocks members", Feature{Stage: StageOff}, member, false},
		{"admin allows admins", Feature{Stage: StageAdmin}, admin, true},
		{"admin blocks members", Feature{Stage: StageAdmin}, member, false},
		{
			"admin blocks beta listed members",
			Feature{Stage: StageAdmin, BetaEmails: betaList},
			beta,
			false,
		},
		{"beta allows admins", Feature{Stage: StageBeta}, admin, true},
		{
			"beta allows normalized listed email",
			Feature{Stage: StageBeta, BetaEmails: betaList},
			beta,
			true,
		},
		{
			"beta blocks unlisted members",
			Feature{Stage: StageBeta, BetaEmails: betaList},
			member,
			false,
		},
		{"ga allows members", Feature{Stage: StageGA}, member, true},
		{"ga blocks anonymous", Feature{Stage: StageGA}, anonymous, false},
		{"unknown stage blocks", Feature{Stage: "later"}, admin, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.feature.Allows(tc.who))
		})
	}
}

func TestResolvedEnabledGivenUnknownOrShortKeysThenIsPure(t *testing.T) {
	resolved, _ := resolve(DefaultRegistry(), nil, nil, 0)
	admin := principal("root@example.com", true)
	assert.True(t, resolved.Enabled(admin, "secretary"))
	assert.True(t, resolved.Enabled(admin, "feature.secretary"))
	assert.False(t, resolved.Enabled(admin, "does_not_exist"))
	assert.False(t, resolved.Enabled(principal("m@example.com", false), "secretary"))
}

func TestDefaultRegistryGivenNoOverridesThenEveryFeatureIsAdminOnly(t *testing.T) {
	resolved, _ := resolve(DefaultRegistry(), func(string) string { return "" }, nil, 0)
	member := principal("member@example.com", false)
	admin := principal("root@example.com", true)
	for name, enabled := range resolved.Features(member) {
		assert.False(t, enabled, name)
	}
	for name, enabled := range resolved.Features(admin) {
		assert.True(t, enabled, name)
	}
	assert.Len(t, resolved.Features(member), 7)
}

func envOf(values map[string]string) EnvLookup {
	return func(name string) string { return values[name] }
}

func rowJSON(key, raw string, version int64) domain.RuntimeConfigRow {
	return domain.RuntimeConfigRow{
		Key: key, ValueJSON: json.RawMessage(raw), Version: version,
		UpdatedBy: "admin@example.com", UpdatedAt: time.Unix(1700000000, 0).UTC(),
	}
}

func TestResolveGivenLayersThenPrefersDatabaseThenEnvironmentThenDefault(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		rows       []domain.RuntimeConfigRow
		wantURL    string
		wantSource string
	}{
		{"default only", nil, nil, "", SourceDefault},
		{"environment over default", map[string]string{"SEARCH_URL": "http://env:9200"}, nil,
			"http://env:9200", SourceEnv},
		{"database over environment", map[string]string{"SEARCH_URL": "http://env:9200"},
			[]domain.RuntimeConfigRow{rowJSON("search.url", `"http://db:9200"`, 2)},
			"http://db:9200", SourceDB},
		{"database over default", nil,
			[]domain.RuntimeConfigRow{rowJSON("search.url", `"http://db:9200"`, 1)},
			"http://db:9200", SourceDB},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, skipped := resolve(DefaultRegistry(), envOf(tc.env), tc.rows, 3)
			assert.Empty(t, skipped)
			value, ok := resolved.Get("search.url")
			require.True(t, ok)
			assert.Equal(t, tc.wantURL, value.Value)
			assert.Equal(t, tc.wantSource, value.Source)
			assert.Equal(t, tc.wantURL, resolved.Search().URL)
		})
	}
}

func TestResolveGivenEnvironmentDefaultsThenReportsDefaultSource(t *testing.T) {
	resolved, _ := resolve(DefaultRegistry(), envOf(map[string]string{
		"SEARCH_INDEX":                  "pax-search-v1",
		"VOICE_ADMIN_ONLY":              "true",
		"TRANSPORT_JOURNAL_GC_DISABLED": "false",
		"VOICE_PROVIDER":                "volcengine",
	}), nil, 0)
	for _, key := range []string{"search.index", "feature.voice_input", "transport.journal_gc_disabled"} {
		value, _ := resolved.Get(key)
		assert.Equal(t, SourceDefault, value.Source, key)
	}
	// Environment values keep their historical semantics even outside the
	// options an admin may select.
	assert.Equal(t, "volcengine", resolved.Voice().Provider)
	open, _ := resolve(DefaultRegistry(), envOf(map[string]string{"VOICE_ADMIN_ONLY": "false"}),
		nil, 0)
	assert.True(t, open.Enabled(principal("member@example.com", false), FeatureVoiceInput))
}

func TestResolveGivenOneBadRowThenOnlyThatRowFallsBack(t *testing.T) {
	rows := []domain.RuntimeConfigRow{
		rowJSON("search.url", `"http://db:9200"`, 1),
		rowJSON("search.index", `"Not A Valid Index"`, 1),
		rowJSON("voice.provider", `"whisper"`, 1),
		rowJSON("feature.secretary", `{"stage":`, 1),
		rowJSON("removed.key", `"x"`, 1),
		rowJSON("search.api_key", `"sk-must-not-be-read-from-db"`, 1),
		rowJSON("feature.notifications", `{"stage":"ga","beta_emails":[]}`, 2),
	}
	env := envOf(map[string]string{
		"SEARCH_INDEX": "env-index", "VOICE_PROVIDER": "qwen", "SEARCH_API_KEY": testSecret,
	})
	resolved, skipped := resolve(DefaultRegistry(), env, rows, 9)

	keys := []string{}
	for _, row := range skipped {
		keys = append(keys, row.Key)
		assert.NotContains(t, row.Reason, "Not A Valid Index")
	}
	assert.ElementsMatch(t, []string{
		"search.index", "voice.provider", "feature.secretary", "removed.key", "search.api_key",
	}, keys)
	assert.Equal(t, SearchConfig{URL: "http://db:9200", Index: "env-index", APIKey: testSecret},
		resolved.Search(), "credentials always come from the environment")
	assert.Equal(t, "qwen", resolved.Voice().Provider)
	assert.False(t, resolved.Enabled(principal("m@example.com", false), FeatureSecretary))
	assert.True(t, resolved.Enabled(principal("m@example.com", false), FeatureNotifications))
}

// flakyRepository wraps the memory repository and fails while down is set.
type flakyRepository struct {
	*storage.MemoryRuntimeConfig
	down  atomic.Bool
	loads atomic.Int32
}

var errDown = errors.New("database unavailable")

func (r *flakyRepository) RuntimeConfigRevision(ctx context.Context) (int64, error) {
	if r.down.Load() {
		return 0, errDown
	}
	return r.MemoryRuntimeConfig.RuntimeConfigRevision(ctx)
}

func (r *flakyRepository) LoadRuntimeConfig(
	ctx context.Context,
) (int64, []domain.RuntimeConfigRow, error) {
	if r.down.Load() {
		return 0, nil, errDown
	}
	r.loads.Add(1)
	return r.MemoryRuntimeConfig.LoadRuntimeConfig(ctx)
}

func newFlaky() *flakyRepository {
	return &flakyRepository{MemoryRuntimeConfig: storage.NewMemoryRuntimeConfig(time.Now)}
}

func TestStoreGivenDatabaseDownAtStartupThenServesEnvironmentAndReportsDegraded(t *testing.T) {
	repo := newFlaky()
	repo.down.Store(true)
	store := New(Options{
		Repository: repo, Env: envOf(map[string]string{"SEARCH_URL": "http://env:9200"}),
		RefreshInterval: 10 * time.Millisecond,
	})
	started := time.Now()
	stop := store.Start(t.Context())
	defer stop()
	assert.Less(t, time.Since(started), 2*time.Second, "a failed load must not block startup")
	assert.Equal(t, "http://env:9200", store.Search().URL)
	health := store.Health()
	assert.True(t, health.Degraded)
	assert.Nil(t, health.LastRefreshAt)
	assert.GreaterOrEqual(t, health.RefreshFailures, int64(1))

	repo.down.Store(false)
	require.Eventually(
		t,
		func() bool { return !store.Health().Degraded },
		time.Second,
		5*time.Millisecond,
	)
	assert.NotNil(t, store.Health().LastRefreshAt)
}

func writeVia(t *testing.T, store *Store, key, raw string, expected int64) EntryView {
	t.Helper()
	view, err := store.Set(
		t.Context(),
		key,
		json.RawMessage(raw),
		expected,
		"test change",
		"admin@example.com",
	)
	require.NoError(t, err)
	return view
}

func TestStoreGivenDatabaseDropsAtRuntimeThenKeepsLastSnapshotAndRecovers(t *testing.T) {
	repo := newFlaky()
	store := New(Options{Repository: repo, RefreshInterval: 10 * time.Millisecond})
	stop := store.Start(t.Context())
	defer stop()
	writeVia(t, store, "search.url", `"http://one:9200"`, 0)
	require.Equal(t, "http://one:9200", store.Search().URL)

	// Another instance writes while this one cannot reach the database.
	other := New(Options{Repository: repo.MemoryRuntimeConfig})
	repo.down.Store(true)
	writeVia(t, other, "search.url", `"http://two:9200"`, 1)
	require.Eventually(
		t,
		func() bool { return store.Health().Degraded },
		time.Second,
		5*time.Millisecond,
	)
	assert.Equal(t, "http://one:9200", store.Search().URL, "last-known-good snapshot stays")

	repo.down.Store(false)
	require.Eventually(t, func() bool { return store.Search().URL == "http://two:9200" },
		time.Second, 5*time.Millisecond)
	assert.False(t, store.Health().Degraded)
}

func TestStoreGivenUnchangedRevisionThenDoesNotReloadTable(t *testing.T) {
	repo := newFlaky()
	store := New(Options{Repository: repo})
	require.NoError(t, store.Refresh(t.Context()))
	require.NoError(t, store.Refresh(t.Context()))
	require.NoError(t, store.Refresh(t.Context()))
	assert.Equal(t, int32(1), repo.loads.Load())
}

func TestStoreGivenInstanceAWritesThenInstanceBSeesItWithinOneInterval(t *testing.T) {
	repo := storage.NewMemoryRuntimeConfig(time.Now)
	a := New(Options{Repository: repo, RefreshInterval: time.Second})
	b := New(Options{Repository: repo, RefreshInterval: time.Second})
	defer a.Start(t.Context())()
	defer b.Start(t.Context())()
	writeVia(t, a, "search.url", `"http://new:9200"`, 0)
	assert.Equal(t, "http://new:9200", a.Search().URL, "the writer applies immediately")
	require.Eventually(t, func() bool { return b.Search().URL == "http://new:9200" },
		1500*time.Millisecond, 20*time.Millisecond)
}

func TestSetGivenCredentialKeysThenRejectsThemAsUnknown(t *testing.T) {
	store := New(Options{Repository: storage.NewMemoryRuntimeConfig(time.Now)})
	for _, key := range []string{
		"search.api_key", "voice.openai.api_key", "voice.qwen.api_key",
		"voice.soniox.api_key", "models.deepseek.api_key", "SEARCH_API_KEY",
	} {
		_, err := store.Set(t.Context(), key, json.RawMessage(`"`+testSecret+`"`), 0,
			"rotate", "admin@example.com")
		var configErr *Error
		require.ErrorAs(t, err, &configErr, key)
		assert.Equal(t, http.StatusNotFound, configErr.Status, key)
		assert.Equal(t, CodeUnknownKey, configErr.Code, key)
	}
	for i := range store.Snapshot().Entries() {
		assert.NotContains(t, store.Snapshot().Entries()[i].Key, "api_key")
	}
}

func TestSetGivenInvalidInputsThenReturnsStableErrorCodes(t *testing.T) {
	store := New(Options{Repository: storage.NewMemoryRuntimeConfig(time.Now)})
	cases := []struct {
		key, raw, reason string
		expected         int64
		status           int
		code             string
		field            string
	}{
		{"nope.key", `"x"`, "r", 0, 404, CodeUnknownKey, ""},
		{"search.url", `"ftp://x"`, "r", 0, 400, CodeValidationFailed, "value"},
		{"search.url", `"http://x:9200"`, " ", 0, 400, CodeValidationFailed, "reason"},
		{
			"search.url",
			`"http://x:9200"`,
			strings.Repeat("r", 501),
			0,
			400,
			CodeValidationFailed,
			"reason",
		},
		{"voice.provider", `"volcengine"`, "r", 0, 400, CodeValidationFailed, "value"},
		{"voice.openai.delay", `"30s"`, "r", 0, 400, CodeValidationFailed, "value"},
		{"feature.secretary", `{"stage":"everyone"}`, "r", 0, 400, CodeValidationFailed, "value"},
		{
			"feature.secretary",
			`{"stage":"ga","extra":1}`,
			"r",
			0,
			400,
			CodeValidationFailed,
			"value",
		},
		{"transport.journal_gc_disabled", `"yes"`, "r", 0, 400, CodeValidationFailed, "value"},
		{"search.url", `"http://x:9200"`, "r", 5, 409, CodeVersionConflict, ""},
	}
	for _, tc := range cases {
		_, err := store.Set(
			t.Context(),
			tc.key,
			json.RawMessage(tc.raw),
			tc.expected,
			tc.reason,
			"a@x.com",
		)
		var configErr *Error
		require.ErrorAs(t, err, &configErr, tc.key)
		assert.Equal(t, tc.status, configErr.Status, tc.key)
		assert.Equal(t, tc.code, configErr.Code, tc.key)
		if tc.field != "" {
			assert.Contains(t, configErr.FieldErrors, tc.field, tc.key)
		}
		if tc.code == CodeVersionConflict {
			assert.Equal(t, int64(0), *configErr.Version)
		}
	}
	view := writeVia(t, store, "voice.openai.delay", `"medium"`, 0)
	assert.Equal(t, "medium", store.Voice().OpenAI.Delay)
	assert.Equal(t, SourceDB, view.Source)
}

func TestSetGivenConcurrentWritersOnOneKeyThenExactlyOneSucceeds(t *testing.T) {
	store := New(Options{Repository: storage.NewMemoryRuntimeConfig(time.Now)})
	assertSingleWinner(t, store)
}

// assertSingleWinner runs eight writers with the same expected version.
func assertSingleWinner(t *testing.T, store *Store) {
	t.Helper()
	var wg sync.WaitGroup
	var wins, conflicts atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Set(context.Background(), "search.index",
				json.RawMessage(`"index-`+string(rune('a'+i))+`"`), 0, "race", "a@x.com")
			var configErr *Error
			switch {
			case err == nil:
				wins.Add(1)
			case errors.As(err, &configErr) && configErr.Code == CodeVersionConflict:
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load())
	assert.Equal(t, int32(7), conflicts.Load())
}

// captureLogs redirects slog for one test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(
		slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})),
	)
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

func TestCredentialsGivenEnvironmentThenNeverAppearInViewAuditOrLogs(t *testing.T) {
	logs := captureLogs(t)
	repo := storage.NewMemoryRuntimeConfig(time.Now)
	store := New(Options{Repository: repo, Env: envOf(map[string]string{
		"SEARCH_API_KEY": testSecret, "OPENAI_API_KEY": "sk-openai-env-000000000000",
	})})
	writeVia(t, store, "search.url", `"http://a:9200"`, 0)
	writeVia(t, store, "voice.provider", `"openai"`, 0)
	assert.Equal(t, testSecret, store.Search().APIKey)
	assert.Equal(t, "sk-openai-env-000000000000", store.Voice().OpenAI.APIKey)
	page, err := store.Audit(t.Context(), "", "", 0)
	require.NoError(t, err)
	_, err = store.Test(t.Context(), "search", nil)
	require.NoError(t, err)
	_, rows, err := repo.LoadRuntimeConfig(t.Context())
	require.NoError(t, err)

	encoded, err := json.Marshal(map[string]any{"view": store.View(), "audit": page, "rows": rows})
	require.NoError(t, err)
	for _, plaintext := range []string{testSecret, "sk-openai-env-000000000000"} {
		assert.NotContains(t, string(encoded), plaintext)
		assert.NotContains(t, logs.String(), plaintext)
	}
}

func TestRevertGivenNonSecretHistoryThenRestoresEachOldValue(t *testing.T) {
	store := New(Options{Repository: storage.NewMemoryRuntimeConfig(time.Now)})
	writeVia(t, store, "search.url", `"http://a:9200"`, 0)
	writeVia(t, store, "search.url", `"http://b:9200"`, 1)
	page, err := store.Audit(t.Context(), "", "", 0)
	require.NoError(t, err)
	// The oldest record had no previous override: reverting it resets.
	oldest := page.Items[len(page.Items)-1]
	assert.Nil(t, oldest.OldDisplay)
	view, err := store.Revert(t.Context(), oldest.ID, 2, "clear", "a@x.com")
	require.NoError(t, err)
	assert.Equal(t, SourceDefault, view.Source)
	assert.Equal(t, int64(0), view.Version)
	assert.Equal(t, "", store.Search().URL)

	view, err = store.Revert(t.Context(), page.Items[0].ID, 0, "restore a", "a@x.com")
	require.NoError(t, err)
	assert.Equal(t, "http://a:9200", store.Search().URL)
	assert.Equal(t, int64(3), view.Version, "versions never repeat after a reset")

	_, err = store.Revert(t.Context(), page.Items[0].ID, 1, "stale", "a@x.com")
	var configErr *Error
	require.ErrorAs(t, err, &configErr)
	assert.Equal(t, CodeVersionConflict, configErr.Code)
}

func TestViewGivenFeatureAndSettingsThenFollowsContractShape(t *testing.T) {
	store := New(Options{
		Repository: storage.NewMemoryRuntimeConfig(time.Now), Region: "hk",
		Env: envOf(
			map[string]string{"SEARCH_API_KEY": testSecret, "SEARCH_URL": "http://env:9200"},
		),
	})
	view := store.View()
	assert.Equal(t, "hk", view.Region)
	byKey := map[string]EntryView{}
	for _, entry := range view.Entries {
		byKey[entry.Key] = entry
		assert.False(t, entry.Secret, entry.Key)
	}
	url := byKey["search.url"]
	assert.Equal(t, "http://env:9200", url.Value)
	assert.Equal(t, SourceEnv, url.Source)
	assert.Equal(t, "SEARCH_URL", url.EnvVar)
	assert.True(t, url.Testable)
	feature := byKey["feature.secretary"]
	assert.Equal(t, Feature{Stage: StageAdmin}, feature.Value)
	assert.Equal(t, KindFeature, feature.Kind)
	assert.False(t, byKey["voice.provider"].Testable)
	assert.Equal(t, []string{"openai", "qwen", "soniox"}, byKey["voice.provider"].Options)
	for _, key := range []string{"search.api_key", "voice.seedasr.api_key", "models.deepseek.api_key"} {
		_, ok := byKey[key]
		assert.False(t, ok, key)
	}
	assert.Equal(t, "local", New(Options{}).View().Region)
}
