package storage

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryConnectionDriverGivenGateFailureThenPropagatesOnlyCommittedWait(t *testing.T) {
	failure := errors.New("injected gate failure")
	for _, scenario := range []string{"update_failure", "commit_failure", "unrelated_failure"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			switch scenario {
			case "update_failure":
				script.execErrors = []error{failure}
			case "commit_failure":
				script.commitErr = failure
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			err = failure
			if scenario != "unrelated_failure" {
				err = gateSecretaryConnectionTx(t.Context(), tx, domain.SecretaryJob{SourceID: "1"},
					secretaryExecutionBatch{state: "blocked", connectionFailures: 9}, time.Now())
			}
			err = commitSecretaryConnectionGate(tx, err)
			assert.ErrorIs(t, err, failure)
			assert.Equal(t, scenario == "commit_failure", script.committed)
		})
	}
}

func TestSecretaryConnectionDriverGivenLegacyRepairFailureThenReturnsItBeforeCommit(t *testing.T) {
	for _, scenario := range []string{"query_failure", "scan_failure", "write_failure"} {
		t.Run(scenario, func(t *testing.T) {
			script := &scriptedPostgresScript{}
			switch scenario {
			case "scan_failure":
				script.queries = []scriptedRows{
					{columns: []string{"batch"}, values: [][]driver.Value{{"batch"}}},
				}
			case "write_failure":
				script.queries = []scriptedRows{{columns: []string{"batch", "attempt", "source"},
					values: [][]driver.Value{{"batch", "attempt", "1"}}}}
				script.execErrors = []error{assert.AnError}
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			tx, err := store.db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			assert.Error(t, repairSecretaryConnectionsTx(t.Context(), tx, time.Now()))
			assert.False(t, script.committed)
		})
	}
}
