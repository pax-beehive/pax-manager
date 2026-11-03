package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryBootstrapGivenStartupThenSerializesAndCommitsIncrementalRepair(t *testing.T) {
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	require.NoError(t, store.BackfillSecretaryJobs(context.Background()))
	require.Len(t, script.beginOptions, 1)
	assert.Equal(t, driver.IsolationLevel(sql.LevelReadCommitted), script.beginOptions[0].Isolation)
	require.Len(t, script.execTexts, 2)
	assert.Contains(t, script.execTexts[0], "pg_advisory_xact_lock")
	assert.Empty(t, script.execArgs[0])
	require.Len(t, script.execArgs[1], 3)
	assert.Equal(t, "", script.execArgs[1][0].Value)
	assert.Equal(t, false, script.execArgs[1][1].Value)
	assert.Equal(t, domain.SearchCanonicalizationVersion, script.execArgs[1][2].Value)
	assert.True(t, script.committed)
	assert.False(t, script.rolled)
}

func TestSecretaryBootstrapGivenExplicitOwnerReconcileThenKeepsExistingLockOrder(t *testing.T) {
	script := &scriptedPostgresScript{}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	tx, err := store.db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	require.NoError(t, backfillSecretaryTx(context.Background(), tx, "owner_one"))
	require.Len(t, script.execTexts, 1)
	assert.NotContains(t, script.execTexts[0], "pg_advisory_xact_lock")
	require.Len(t, script.execArgs[0], 3)
	assert.Equal(t, "owner_one", script.execArgs[0][0].Value)
	assert.Equal(t, true, script.execArgs[0][1].Value)
	assert.Equal(t, domain.SearchCanonicalizationVersion, script.execArgs[0][2].Value)
	assert.False(t, script.committed, "The caller owns the transaction")
}

func TestSecretaryBootstrapGivenTransactionFailureThenReturnsItWithoutPartialCommit(t *testing.T) {
	failure := errors.New("bootstrap transaction failed")
	for _, tc := range []struct {
		name         string
		script       scriptedPostgresScript
		wantExecs    int
		wantCommit   bool
		wantRollback bool
	}{
		{name: "begin", script: scriptedPostgresScript{beginErr: failure}},
		{name: "lock", script: scriptedPostgresScript{execErrors: []error{failure}}, wantExecs: 1, wantRollback: true},
		{name: "repair", script: scriptedPostgresScript{execErrors: []error{nil, failure}}, wantExecs: 2, wantRollback: true},
		{name: "commit", script: scriptedPostgresScript{commitErr: failure}, wantExecs: 2, wantCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := scriptedPostgresStore(t, &tc.script)
			defer cleanup()

			require.ErrorIs(t, store.BackfillSecretaryJobs(context.Background()), failure)
			assert.Equal(t, tc.wantExecs, tc.script.execCount)
			assert.Equal(t, tc.wantCommit, tc.script.committed)
			assert.Equal(t, tc.wantRollback, tc.script.rolled)
		})
	}
}
