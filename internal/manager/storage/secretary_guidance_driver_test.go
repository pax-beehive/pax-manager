package storage

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryGuidanceRows(values ...[]driver.Value) scriptedRows {
	return scriptedRows{
		columns: []string{
			"owner_user_id",
			"scope",
			"source_id",
			"revision",
			"instruction",
			"updated_at",
		},
		values: values,
	}
}

func TestSecretaryGuidanceDriverGivenMissingOrSavedConfigurationThenGetCommitsAnOwnerScopedRead(
	t *testing.T,
) {
	for _, scope := range []string{domain.SecretaryGuidanceUser, domain.SecretaryGuidanceSession} {
		for _, present := range []bool{false, true} {
			source := ""
			var queries []scriptedRows
			if scope == domain.SecretaryGuidanceSession {
				source = "1"
				queries = append(
					queries,
					scriptedRows{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
				)
			}
			rows := secretaryGuidanceRows()
			if present {
				rows = secretaryGuidanceRows(
					[]driver.Value{
						"owner",
						scope,
						source,
						int64(2),
						"Existing preference",
						time.Now(),
					},
				)
			}
			queries = append(queries, rows)
			script := &scriptedPostgresScript{queries: queries}
			store, cleanup := scriptedPostgresStore(t, script)
			result, err := store.GetSecretaryGuidance(t.Context(), "owner", scope, source)
			require.NoError(t, err)
			assert.Equal(t, "owner", result.OwnerUserID)
			assert.Equal(t, scope, result.Scope)
			assert.Equal(t, source, result.SourceID)
			if present {
				assert.EqualValues(t, 2, result.Revision)
			} else {
				assert.Zero(t, result.Revision)
			}
			assert.True(t, script.committed)
			require.Len(t, script.beginOptions, 1)
			assert.True(t, script.beginOptions[0].ReadOnly)
			assert.Empty(t, script.execTexts)
			cleanup()
		}
	}
}

func TestSecretaryGuidanceDriverGivenCASUpdateThenItUsesItsOwnScopeAndDoesNotFenceExistingJobs(
	t *testing.T,
) {
	for _, scope := range []string{domain.SecretaryGuidanceUser, domain.SecretaryGuidanceSession} {
		source := ""
		var queries []scriptedRows
		if scope == domain.SecretaryGuidanceSession {
			source = "1"
			queries = append(
				queries,
				scriptedRows{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
			)
		}
		queries = append(
			queries,
			secretaryGuidanceRows(
				[]driver.Value{
					"owner",
					scope,
					source,
					int64(2),
					"Keep important decisions.",
					time.Now(),
				},
			),
		)
		script := &scriptedPostgresScript{queries: queries}
		store, cleanup := scriptedPostgresStore(t, script)
		result, err := store.UpdateSecretaryGuidance(
			t.Context(),
			"owner",
			scope,
			source,
			domain.SecretaryGuidanceUpdate{
				Instruction:      "  Keep important decisions.\n",
				ExpectedRevision: 1,
			},
		)
		require.NoError(t, err)
		assert.EqualValues(t, 2, result.Revision)
		assert.True(t, script.committed)
		assert.Empty(t, script.execTexts)
		query := script.queryTexts[len(script.queryTexts)-1]
		assert.Contains(t, query, "WHERE secretary_guidance.revision=$5")
		if scope == domain.SecretaryGuidanceSession {
			assert.Contains(t, query, "(owner_user_id,source_id) WHERE scope='session'")
			assert.Contains(t, script.queryTexts[0], "FOR SHARE OF s,a")
		} else {
			assert.Contains(t, query, "(owner_user_id) WHERE scope='user'")
		}
		args := script.queryArgs[len(script.queryArgs)-1]
		require.Len(t, args, 5)
		assert.Equal(t, "Keep important decisions.", args[3].Value)
		assert.NotContains(t, strings.Join(script.queryTexts, "\n"), "UPDATE secretary_jobs")
		cleanup()
	}
}

func TestSecretaryGuidanceDriverGivenStaleCASOrForeignSourceThenNoMutationCommits(t *testing.T) {
	for name, scenario := range map[string]struct {
		scope, source string
		revision      int64
		queries       []scriptedRows
		want          error
	}{
		"stale_update":              {domain.SecretaryGuidanceUser, "", 1, []scriptedRows{secretaryGuidanceRows()}, ErrConflict},
		"missing_expected_revision": {domain.SecretaryGuidanceUser, "", 5, []scriptedRows{secretaryGuidanceRows([]driver.Value{"owner", domain.SecretaryGuidanceUser, "", int64(1), "new", time.Now()})}, ErrConflict},
		"foreign_source":            {domain.SecretaryGuidanceSession, "1", 0, []scriptedRows{{columns: []string{"id"}}}, ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			script := &scriptedPostgresScript{queries: scenario.queries}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			_, err := store.UpdateSecretaryGuidance(
				t.Context(),
				"owner",
				scenario.scope,
				scenario.source,
				domain.SecretaryGuidanceUpdate{
					Instruction:      "new",
					ExpectedRevision: scenario.revision,
				},
			)
			assert.ErrorIs(t, err, scenario.want)
			assert.False(t, script.committed)
			assert.True(t, script.rolled)
			if name == "foreign_source" {
				require.Len(t, script.queryTexts, 1)
				assert.NotContains(t, script.queryTexts[0], "INSERT")
			}
		})
	}
}

func TestSecretaryGuidanceDriverGivenUnavailableDatabaseThenGetAndUpdateFailClosed(t *testing.T) {
	for _, operation := range []string{"get", "update"} {
		for _, mode := range []string{"begin", "foreign", "query", "scan", "commit"} {
			t.Run(operation+"_"+mode, func(t *testing.T) {
				sentinel := errors.New("database unavailable")
				script := &scriptedPostgresScript{}
				scope, source := domain.SecretaryGuidanceUser, ""
				switch mode {
				case "begin":
					script.beginErr = sentinel
				case "foreign":
					scope, source = domain.SecretaryGuidanceSession, "1"
					script.queries = []scriptedRows{{columns: []string{"id"}}}
				case "scan":
					script.queries = []scriptedRows{
						{columns: []string{"wrong"}, values: [][]driver.Value{{"bad"}}},
					}
				case "commit":
					script.queries = []scriptedRows{
						secretaryGuidanceRows(
							[]driver.Value{"owner", scope, source, int64(1), "text", time.Now()},
						),
					}
					script.commitErr = sentinel
				}
				store, cleanup := scriptedPostgresStore(t, script)
				defer cleanup()
				var err error
				if operation == "get" {
					_, err = store.GetSecretaryGuidance(t.Context(), "owner", scope, source)
				} else {
					_, err = store.UpdateSecretaryGuidance(t.Context(), "owner", scope, source, domain.SecretaryGuidanceUpdate{Instruction: "text"})
				}
				require.Error(t, err)
				if mode == "begin" || mode == "commit" {
					assert.ErrorIs(t, err, sentinel)
				}
				if mode != "commit" {
					assert.False(t, script.committed)
				}
			})
		}
	}
}

func TestSecretaryGuidanceDriverGivenMalformedSnapshotRowThenItIsNotSilentlyDropped(t *testing.T) {
	script := &scriptedPostgresScript{
		queries: []scriptedRows{{columns: []string{"wrong"}, values: [][]driver.Value{{"bad"}}}},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback()) }()
	snapshot, err := loadSecretaryInstructionsTx(t.Context(), tx, "owner", "1")
	require.Error(t, err)
	assert.Nil(t, snapshot)
}

func TestSecretaryGuidanceDriverGivenBothScopesWhenSnapshotLoadedThenEachLayerIsFrozen(
	t *testing.T,
) {
	script := &scriptedPostgresScript{queries: []scriptedRows{secretaryGuidanceRows(
		[]driver.Value{
			"owner",
			domain.SecretaryGuidanceSession,
			"1",
			int64(2),
			"Condense procedures.",
			time.Now(),
		},
		[]driver.Value{
			"owner",
			domain.SecretaryGuidanceUser,
			"",
			int64(1),
			"Keep decisions.",
			time.Now(),
		},
	)}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback()) }()
	snapshot, err := loadSecretaryInstructionsTx(t.Context(), tx, "owner", "1")
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	assert.EqualValues(t, 1, snapshot.User.Revision)
	assert.EqualValues(t, 2, snapshot.Session.Revision)
	assert.Equal(t, "Keep decisions.", snapshot.User.Instruction)
	assert.Equal(t, "Condense procedures.", snapshot.Session.Instruction)
	assert.Empty(t, snapshot.Run)
}
