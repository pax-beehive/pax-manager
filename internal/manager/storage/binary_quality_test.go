package storage

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualityStore interface {
	CreatePaxdArtifact(context.Context, CreatePaxdArtifactRequest, string) (PaxdArtifact, error)
	FindPaxdArtifact(context.Context, FindPaxdArtifactRequest) (PaxdArtifact, error)
}

func TestBinaryQualitySelection(t *testing.T) {
	t.Run("memory", func(t *testing.T) { exerciseBinaryQuality(t, NewMemoryStore(time.Now)) })
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("PAX_MANAGER_QUALITY_TEST_DATABASE_URL")
		if dsn == "" {
			t.Skip("requires isolated PostgreSQL")
		}
		db, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		_, err = db.ExecContext(t.Context(), `CREATE TEMP TABLE paxd_artifacts (
 artifact_id text PRIMARY KEY, product text, platform text, tags text[], version text, build_id text,
 bucket text, object text, generation bigint, sha256 text, size_bytes bigint, content_type text,
 created_by text, created_at timestamptz, deleted_at timestamptz, UNIQUE(bucket,object,generation))`)
		require.NoError(t, err)
		exerciseBinaryQuality(t, NewPostgresStore(db, time.Now))
	})
}

func exerciseBinaryQuality(t *testing.T, store qualityStore) {
	t.Helper()
	ctx := t.Context()
	for _, v := range []struct {
		version string
		tags    []string
	}{{"0.1.48", []string{"stable", "testing"}}, {"0.1.49", []string{"stable", "disabled"}}, {"0.1.50", nil}} {
		_, err := store.CreatePaxdArtifact(
			ctx,
			CreatePaxdArtifactRequest{
				Product:    "paxd",
				Platform:   "linux/amd64",
				Version:    v.version,
				Tags:       v.tags,
				Bucket:     "quality",
				Object:     v.version,
				Generation: 1,
			},
			"publisher",
		)
		require.NoError(t, err)
	}
	for _, state := range []string{"stable", "testing"} {
		a, err := store.FindPaxdArtifact(
			ctx,
			FindPaxdArtifactRequest{
				Product:  "paxd",
				Platform: "linux/amd64",
				Tags:     []string{state},
			},
		)
		require.NoError(t, err)
		if state == "stable" {
			require.Equal(t, "0.1.48", a.Version)
		} else {
			require.Equal(t, "0.1.50", a.Version)
		}
	}
	_, err := store.FindPaxdArtifact(
		ctx,
		FindPaxdArtifactRequest{Product: "paxd", Platform: "linux/amd64", Version: "0.1.49"},
	)
	require.ErrorIs(t, err, ErrNotFound)
	a, err := store.FindPaxdArtifact(
		ctx,
		FindPaxdArtifactRequest{
			Product:         "paxd",
			Platform:        "linux/amd64",
			Version:         "0.1.49",
			IncludeDisabled: true,
		},
	)
	require.NoError(t, err)
	require.Contains(t, a.Tags, "disabled")
	_, err = store.FindPaxdArtifact(
		ctx,
		FindPaxdArtifactRequest{
			Product:  "paxd",
			Platform: "linux/amd64",
			Version:  "0.1.48",
			Tags:     []string{"testing"},
		},
	)
	require.ErrorIs(t, err, ErrNotFound)
}
