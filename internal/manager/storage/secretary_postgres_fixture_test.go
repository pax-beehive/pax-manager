package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

const secretaryFixtureSchema = "secretary_fixture"

var (
	secretaryFixtureTemplates sync.Map
	secretaryFixtureSequence  atomic.Uint64
)

type secretaryFixtureTemplate struct {
	once sync.Once
	err  error
}

func secretaryPostgresCaseDatabase(t *testing.T, config *pgx.ConnConfig) *sql.DB {
	t.Helper()
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	template := os.Getenv("PAX_MANAGER_SECRETARY_TEST_TEMPLATE_DATABASE")
	if template == "" {
		return secretaryPostgresCaseSchema(t, admin, config)
	}
	require.NoError(t, validateSecretaryTemplateDatabase(template, config.Database))
	key := config.ConnString() + "\x00" + template
	value, _ := secretaryFixtureTemplates.LoadOrStore(key, &secretaryFixtureTemplate{})
	state := value.(*secretaryFixtureTemplate)
	state.once.Do(func() {
		state.err = createSecretaryTemplateDatabase(t.Context(), admin, config, template)
	})
	require.NoError(t, state.err)
	name := fmt.Sprintf("pax_secretary_case_%d_%d", os.Getpid(), secretaryFixtureSequence.Add(1))
	_, err := admin.ExecContext(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+
		" TEMPLATE "+pgx.Identifier{template}.Sanitize())
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, dropErr := admin.ExecContext(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		require.NoError(t, dropErr)
	})
	caseConfig := config.Copy()
	caseConfig.Database = name
	caseConfig.RuntimeParams["search_path"] = secretaryFixtureSchema
	db := stdlib.OpenDB(*caseConfig)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func secretaryPostgresCaseSchema(
	t *testing.T,
	admin *sql.DB,
	config *pgx.ConnConfig,
) *sql.DB {
	t.Helper()
	schema := fmt.Sprintf("secretary_%d", time.Now().UnixNano())
	_, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, dropErr := admin.ExecContext(
			ctx,
			"DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE",
		)
		require.NoError(t, dropErr)
	})
	caseConfig := config.Copy()
	caseConfig.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*caseConfig)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, initializeSecretaryPostgresSchema(t.Context(), db))
	return db
}

func validateSecretaryTemplateDatabase(name, currentDatabase string) error {
	if len(name) > 63 || !strings.HasPrefix(name, "pax_secretary_fixture_") ||
		name == currentDatabase {
		return fmt.Errorf("Secretary template must be a separate pax_secretary_fixture_ database")
	}
	for _, char := range name {
		if char != '_' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return fmt.Errorf("Secretary template database name contains invalid characters")
		}
	}
	return nil
}

func createSecretaryTemplateDatabase(
	ctx context.Context,
	admin *sql.DB,
	config *pgx.ConnConfig,
	name string,
) (err error) {
	_, err = admin.ExecContext(
		ctx,
		"CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0",
	)
	if err != nil {
		return fmt.Errorf("create Secretary template database: %w", err)
	}
	// Successful templates live only for the disposable acceptance container's lifetime.
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, cleanupErr := admin.ExecContext(
				cleanupCtx,
				"DROP DATABASE "+pgx.Identifier{name}.Sanitize(),
			)
			err = errors.Join(err, cleanupErr)
		}
	}()
	templateConfig := config.Copy()
	templateConfig.Database = name
	templateConfig.RuntimeParams["search_path"] = secretaryFixtureSchema
	db := stdlib.OpenDB(*templateConfig)
	defer func() { err = errors.Join(err, db.Close()) }()
	if _, err = db.ExecContext(ctx, "CREATE SCHEMA "+secretaryFixtureSchema); err != nil {
		return err
	}
	return initializeSecretaryPostgresSchema(ctx, db)
}

func initializeSecretaryPostgresSchema(ctx context.Context, db *sql.DB) error {
	for _, name := range []string{"init.sql", "summaries.sql", "search.sql", "secretary.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", name))
		if err != nil {
			return err
		}
		if _, err = db.ExecContext(ctx, string(raw)); err != nil {
			return fmt.Errorf("initialize Secretary fixture %s: %w", name, err)
		}
	}
	_, err := db.ExecContext(ctx, `ALTER TABLE messages ALTER COLUMN status SET DEFAULT 'complete'`)
	return err
}

func seedSecretaryPostgresFixture(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(
		ctx,
		`INSERT INTO users(user_id,email) VALUES ('owner','owner@example.test'),('other','other@example.test');
 INSERT INTO agents(agent_id,owner_user_id,hostname,os,api_key_hash) VALUES ('source','owner','host','linux','source-key'),('secretary','owner','host','linux','secretary-key'),('other','other','host','linux','other-key');
 INSERT INTO agent_sessions(agent_id,session_id,session_name) VALUES ('source','session','Original session');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq) VALUES ('m1','source','session','test','inbound','assistant','agent_message_chunk',1);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m1',0,'text','original');`,
	)
	return err
}
