package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryOperationsGivenPersistenceFailureThenRollsBackSummaryProgressAndOperation(
	t *testing.T,
) {
	for _, table := range []string{"summary_versions", "message_snapshots", "summary_sources", "secretary_message_state", "secretary_jobs", "secretary_operations"} {
		t.Run(table, func(t *testing.T) {
			store := secretaryPostgresFixture(t)
			f := &secretaryLifecycleFixture{store: store, postgres: store}
			f.settings = secretaryEnable(t, store)
			operation := secretaryOperationDraft(t, f, "human_summary", "pending")
			predicate := "TRUE"
			if table == "secretary_jobs" {
				predicate = "NEW.retry_epoch>OLD.retry_epoch"
			}
			_, err := store.db.ExecContext(
				t.Context(),
				fmt.Sprintf(
					`CREATE FUNCTION reject_human_operation() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF %s THEN RAISE EXCEPTION 'injected human operation persistence failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_human_operation BEFORE INSERT OR UPDATE ON %s
 FOR EACH ROW EXECUTE FUNCTION reject_human_operation()`,
					predicate,
					table,
				),
			)
			require.NoError(t, err)
			_, err = store.SubmitSecretaryOperation(
				t.Context(),
				"owner",
				operation.OperationID,
				secretaryHumanInput(operation),
			)
			require.ErrorContains(t, err, "injected human operation persistence failure")
			current, err := store.GetSecretaryOperation(t.Context(), "owner", operation.OperationID)
			require.NoError(t, err)
			assert.Equal(t, "draft", current.Status)
			assert.Empty(t, current.ResultRefs)
			for _, checked := range []string{"summary_versions", "message_snapshots", "secretary_attempts"} {
				var count int
				require.NoError(
					t,
					store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+checked).
						Scan(&count),
				)
				assert.Zero(t, count)
			}
			assert.Zero(t, secretaryProcessedCursor(t, f))
		})
	}
}

func TestSecretaryOperationsGivenPausedSourceThenManualSavePreservesPauseAndGuidedRunRejects(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		if f.memory != nil {
			f.memory.secretaryState.jobs["1"].paused = true
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE secretary_jobs SET paused=TRUE WHERE source_id=1`)
			require.NoError(t, err)
		}
		guided := secretaryOperationDraft(t, f, "guided_summary", "pending")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", guided.OperationID,
			domain.SecretaryOperationSubmit{
				RequestID:        "paused-guided",
				ExpectedRevision: guided.Revision,
			})
		assert.ErrorIs(t, err, ErrConflict)
		manual := secretaryOperationDraft(t, f, "human_summary", "pending")
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", manual.OperationID, secretaryHumanInput(manual))
		require.NoError(t, err)
		if f.memory != nil {
			assert.True(t, f.memory.secretaryState.jobs["1"].paused)
		} else {
			var paused bool
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT paused FROM secretary_jobs WHERE source_id=1`).Scan(&paused))
			assert.True(t, paused)
		}
	})
}

func TestSecretaryOperationsGivenMessageRemovalThenOperationPreviewCannotBypassCurrentAccess(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		operation := secretaryOperationDraft(t, f, "human_summary", "pending")
		if f.memory != nil {
			delete(f.memory.messages, "m1")
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
			require.NoError(t, err)
		}
		_, err := secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "owner", operation.OperationID)
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, secretaryHumanInput(operation))
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSecretaryOperationsGivenCanceledRequestOrForeignOwnerThenCannotCreateOrReadDraft(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := secretaryOperationStore(f)
		input := domain.SecretaryOperationPrepare{
			SourceID: "1",
			Kind:     "human_summary",
			Scope:    "pending",
		}
		for _, owner := range []string{"", "other"} {
			_, err := store.PrepareSecretaryOperation(t.Context(), owner, input)
			require.Error(t, err)
		}
		operation, err := store.PrepareSecretaryOperation(t.Context(), "owner", input)
		require.NoError(t, err)
		_, err = store.GetSecretaryOperation(t.Context(), "", operation.OperationID)
		assert.ErrorIs(t, err, ErrUnauthorized)
		_, err = store.SubmitSecretaryOperation(
			t.Context(),
			"",
			operation.OperationID,
			secretaryHumanInput(operation),
		)
		assert.ErrorIs(t, err, ErrUnauthorized)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = store.PrepareSecretaryOperation(ctx, "owner", input)
		assert.ErrorIs(t, err, context.Canceled)
		_, err = store.GetSecretaryOperation(ctx, "owner", operation.OperationID)
		assert.ErrorIs(t, err, context.Canceled)
		_, err = store.SubmitSecretaryOperation(
			ctx,
			"owner",
			operation.OperationID,
			secretaryHumanInput(operation),
		)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestSecretaryOperationsGivenLegacyCursorThenPartialCompletionKeepsHistoricalProgress(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		work := secretaryClaim(t, f.store)
		_, err := f.store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
		require.NoError(t, err)
		before := secretaryProcessedCursor(t, f)
		if f.memory != nil {
			f.memory.secretaryState.jobs["1"].processedChanges = nil
		} else {
			_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE secretary_message_state SET processed_change_revision=0`)
			require.NoError(t, err)
		}
		secretaryOperationMessage(t, f, "m2", "Earlier pending", 2)
		secretaryOperationMessage(t, f, "m3", "Later completed manually", 3)
		operation := secretaryOperationDraft(t, f, "human_summary", "selected", "m3")
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, secretaryHumanInput(operation))
		require.NoError(t, err)
		after := secretaryProcessedCursor(t, f)
		assert.GreaterOrEqual(t, after, before)
		remaining := secretaryClaim(t, f.store)
		require.Len(t, remaining.Messages, 1)
		assert.Equal(t, "m2", remaining.Messages[0].SourceID)
	})
}
