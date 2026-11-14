package runtimeconfig

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

// openRuntimeConfigDatabase creates an isolated schema, applies the
// migration, and returns a pool whose connections all use that schema.
func openRuntimeConfigDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PAX_MANAGER_RUNTIME_CONFIG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip(
			"set PAX_MANAGER_RUNTIME_CONFIG_TEST_DATABASE_URL to an isolated PostgreSQL database",
		)
	}
	suffix := make([]byte, 6)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	schema := "runtime_config_test_" + hex.EncodeToString(suffix)
	admin, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = admin.Close()
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = db.Close() })
	ddl, err := os.ReadFile(filepath.Join("..", "..", "..", storage.RuntimeConfigSchemaPath))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(ddl))
	require.NoError(t, err)
	// The migration is idempotent because EnsureSchema runs it on every start.
	_, err = db.ExecContext(t.Context(), string(ddl))
	require.NoError(t, err)
	return db
}

func TestPostgresGivenConcurrentWritersOnOneKeyThenExactlyOneSucceeds(t *testing.T) {
	db := openRuntimeConfigDatabase(t)
	store := New(Options{Repository: storage.NewPostgresRuntimeConfig(db)})
	assertSingleWinner(t, store)
	var audits, revision int64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM runtime_config_audit`).Scan(&audits))
	require.NoError(t, db.QueryRow(`SELECT revision FROM runtime_config_revision`).Scan(&revision))
	assert.Equal(t, int64(1), audits, "a conflicting write leaves no audit record")
	assert.Equal(t, int64(1), revision, "a conflicting write does not bump the revision")
}

func TestPostgresGivenInstanceAWritesThenInstanceBSeesItWithinOneInterval(t *testing.T) {
	db := openRuntimeConfigDatabase(t)
	a := New(
		Options{Repository: storage.NewPostgresRuntimeConfig(db), RefreshInterval: time.Second},
	)
	b := New(
		Options{Repository: storage.NewPostgresRuntimeConfig(db), RefreshInterval: time.Second},
	)
	defer a.Start(t.Context())()
	defer b.Start(t.Context())()
	require.False(t, b.Health().Degraded)
	writeVia(t, a, "search.url", `"http://hk-search:9200"`, 0)
	require.Eventually(t, func() bool { return b.Search().URL == "http://hk-search:9200" },
		2*time.Second, 20*time.Millisecond)
	assert.Equal(t, a.Snapshot().Revision, b.Snapshot().Revision)
}

func TestPostgresGivenCorruptRowThenOnlyThatKeyFallsBack(t *testing.T) {
	db := openRuntimeConfigDatabase(t)
	store := New(Options{
		Repository: storage.NewPostgresRuntimeConfig(db),
		Env:        envOf(map[string]string{"SEARCH_INDEX": "env-index"}),
	})
	writeVia(t, store, "search.url", `"http://ok:9200"`, 0)
	_, err := db.Exec(`
INSERT INTO runtime_config (key, value_json, version, updated_by) VALUES
  ('search.index', '42', 1, 'dba@example.com'),
  ('feature.notifications', '{"stage":"ga","beta_emails":[]}', 1, 'dba@example.com');
UPDATE runtime_config_revision SET revision = revision + 1`)
	require.NoError(t, err)
	require.NoError(t, store.Refresh(t.Context()))
	assert.Equal(t, "http://ok:9200", store.Search().URL)
	assert.Equal(t, "env-index", store.Search().Index)
	assert.True(t, store.Feature(principal("m@example.com", false), FeatureNotifications))
	assert.False(t, store.Health().Degraded)
}

func TestPostgresGivenRevertThenRestoresOldValueAndAudits(t *testing.T) {
	db := openRuntimeConfigDatabase(t)
	store := New(Options{Repository: storage.NewPostgresRuntimeConfig(db)})
	first := writeVia(t, store, "voice.openai.model", `"model-a"`, 0)
	second := writeVia(t, store, "voice.openai.model", `"model-b"`, first.Version)
	page, err := store.Audit(t.Context(), "voice.openai.model", "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, "model-a", *page.Items[0].OldDisplay)
	assert.True(t, page.Items[0].Revertible)
	reverted, err := store.Revert(
		t.Context(),
		page.Items[0].ID,
		second.Version,
		"bad model",
		"a@x.com",
	)
	require.NoError(t, err)
	assert.Equal(t, "model-a", store.Voice().OpenAI.Model)
	assert.Equal(t, int64(3), reverted.Version)

	rows, err := db.Query(
		`SELECT action, COALESCE(reason, '') FROM runtime_config_audit ORDER BY id`,
	)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var actions []string
	for rows.Next() {
		var action, reason string
		require.NoError(t, rows.Scan(&action, &reason))
		actions = append(actions, action+":"+reason)
	}
	assert.Equal(t, []string{"set:test change", "set:test change", "revert:bad model"}, actions)

	page, err = store.Audit(t.Context(), "", "", 1)
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)
	next, err := store.Audit(t.Context(), "", *page.NextCursor, 5)
	require.NoError(t, err)
	assert.Len(t, next.Items, 2)
	_, err = json.Marshal(next)
	require.NoError(t, err)
}
