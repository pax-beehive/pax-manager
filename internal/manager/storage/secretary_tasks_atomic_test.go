package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryTasksPostgresGivenRetryOrRecoveryPersistenceFailureThenRollsBackTheControl(
	t *testing.T,
) {
	for _, operation := range []string{"retry", "claim", "release"} {
		t.Run(operation, func(t *testing.T) {
			store := secretaryPostgresFixture(t)
			f := &secretaryLifecycleFixture{store: store, postgres: store}
			f.settings = secretaryEnable(t, store)
			secretaryTaskUnknown(t, f, "provider error")
			var job domain.SecretaryJob
			if operation == "release" {
				detail := secretaryReadTask(t, f)
				var err error
				job, err = store.ClaimSecretaryTaskRecovery(
					t.Context(),
					"owner",
					"1",
					secretaryTaskObserved(detail.Task, "recheck"),
					time.Minute,
				)
				require.NoError(t, err)
			}
			before := secretaryReadTask(t, f)
			sql := `CREATE FUNCTION reject_task_control() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'injected task persistence failure'; END $$;
 CREATE TRIGGER reject_task_control BEFORE UPDATE ON secretary_jobs FOR EACH ROW EXECUTE FUNCTION reject_task_control()`
			if operation == "claim" {
				sql = `CREATE FUNCTION reject_task_control() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'injected task persistence failure'; END $$;
 CREATE TRIGGER reject_task_control BEFORE UPDATE ON secretary_batches FOR EACH ROW EXECUTE FUNCTION reject_task_control()`
			}
			_, err := store.db.ExecContext(t.Context(), sql)
			require.NoError(t, err)
			switch operation {
			case "retry":
				action := secretaryTaskObserved(before.Task, "retry")
				action.ConfirmRestart = true
				_, err = store.ControlSecretaryTask(t.Context(), "owner", "1", action)
			case "claim":
				_, err = store.ClaimSecretaryTaskRecovery(
					t.Context(),
					"owner",
					"1",
					secretaryTaskObserved(before.Task, "recheck"),
					time.Minute,
				)
			case "release":
				err = store.ReleaseSecretaryTaskRecovery(t.Context(), job)
			}
			require.ErrorContains(t, err, "injected task persistence failure")
			after := secretaryReadTask(t, f)
			assert.Equal(t, before, after)
		})
	}
}
