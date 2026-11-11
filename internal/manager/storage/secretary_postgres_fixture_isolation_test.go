package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretaryPostgresFixtureGivenIndependentCasesWhenOneChangesDDLThenOthersKeepFreshSeeds(
	t *testing.T,
) {
	first := secretaryPostgresFixture(t)
	var firstDatabase, firstSchema string
	require.NoError(t, first.db.QueryRowContext(t.Context(),
		`SELECT current_database(),current_schema()`).Scan(&firstDatabase, &firstSchema))
	_, err := first.db.ExecContext(t.Context(), `ALTER TABLE messages ADD COLUMN fixture_only TEXT;
 DELETE FROM message_parts;
 UPDATE users SET display_name='changed' WHERE user_id='owner'`)
	require.NoError(t, err)
	var beforeSecondSeed time.Time
	require.NoError(
		t,
		first.db.QueryRowContext(t.Context(), `SELECT clock_timestamp()`).Scan(&beforeSecondSeed),
	)

	second := secretaryPostgresFixture(t)

	var secondDatabase, secondSchema string
	require.NoError(t, second.db.QueryRowContext(t.Context(),
		`SELECT current_database(),current_schema()`).Scan(&secondDatabase, &secondSchema))
	if os.Getenv("PAX_MANAGER_SECRETARY_TEST_TEMPLATE_DATABASE") != "" {
		assert.NotEqual(t, firstDatabase, secondDatabase)
	} else {
		assert.Equal(t, firstDatabase, secondDatabase)
		assert.NotEqual(t, firstSchema, secondSchema)
	}
	var extraColumns int
	require.NoError(t, second.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM information_schema.columns
 WHERE table_schema=current_schema() AND table_name='messages' AND column_name='fixture_only'`).
		Scan(&extraColumns))
	assert.Zero(t, extraColumns)
	var text, displayName string
	var createdAt time.Time
	require.NoError(t, second.db.QueryRowContext(t.Context(),
		`SELECT p.text,m.created_at,u.display_name FROM messages m
 JOIN message_parts p ON p.message_id=m.message_id JOIN users u ON u.user_id='owner'
 WHERE m.message_id='m1'`).Scan(&text, &createdAt, &displayName))
	assert.Equal(t, "original", text)
	assert.Empty(t, displayName)
	assert.False(t, createdAt.Before(beforeSecondSeed), "each case must seed fresh timestamps")
	if os.Getenv("PAX_MANAGER_SECRETARY_TEST_TEMPLATE_DATABASE") != "" {
		for _, setting := range []string{"fsync", "full_page_writes", "synchronous_commit"} {
			var value string
			require.NoError(t, second.db.QueryRowContext(t.Context(), "SHOW "+setting).Scan(&value))
			assert.Equal(t, "on", value, setting)
		}
		config, parseErr := pgx.ParseConfig(os.Getenv("PAX_MANAGER_SECRETARY_TEST_DATABASE_URL"))
		require.NoError(t, parseErr)
		config.Database = os.Getenv("PAX_MANAGER_SECRETARY_TEST_TEMPLATE_DATABASE")
		config.RuntimeParams["search_path"] = secretaryFixtureSchema
		templateDB := stdlib.OpenDB(*config)
		t.Cleanup(func() { require.NoError(t, templateDB.Close()) })
		var seeds int
		require.NoError(
			t,
			templateDB.QueryRowContext(t.Context(), `SELECT count(*) FROM users`).Scan(&seeds),
		)
		assert.Zero(t, seeds, "the template must not contain case seed data")
	}
}

func TestSecretaryPostgresFixtureGivenTemplateNameWhenValidatedThenRejectsUnsafeNames(
	t *testing.T,
) {
	for _, name := range []string{
		"", "postgres", "template0", "template1", "secretary_test", "pax_secretary_fixture_shared",
		"pax_secretary_fixture_bad-name", "pax_secretary_fixture_\x00", "pax_secretary_fixture_" + string(make([]byte, 64)),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateSecretaryTemplateDatabase(name, "pax_secretary_fixture_shared"))
		})
	}
	assert.NoError(
		t,
		validateSecretaryTemplateDatabase("pax_secretary_fixture_template", "secretary_test"),
	)
}

func TestSecretaryPostgresFixtureGivenCompletedCaseWhenCleanedThenItsStorageIsRemoved(
	t *testing.T,
) {
	var database, schema string
	t.Run("case", func(t *testing.T) {
		store := secretaryPostgresFixture(t)
		require.NoError(t, store.db.QueryRowContext(t.Context(),
			`SELECT current_database(),current_schema()`).Scan(&database, &schema))
	})
	if database == "" {
		return
	}
	config, err := pgx.ParseConfig(os.Getenv("PAX_MANAGER_SECRETARY_TEST_DATABASE_URL"))
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var remaining int
	if os.Getenv("PAX_MANAGER_SECRETARY_TEST_TEMPLATE_DATABASE") != "" {
		err = admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_database WHERE datname=$1`, database).
			Scan(&remaining)
	} else {
		err = admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_namespace WHERE nspname=$1`, schema).
			Scan(&remaining)
	}
	require.NoError(t, err)
	assert.Zero(t, remaining)
}
