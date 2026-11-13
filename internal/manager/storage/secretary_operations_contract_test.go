package storage

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryOperationStore(f *secretaryLifecycleFixture) domain.SecretaryOperationStore {
	return f.store.(domain.SecretaryOperationStore)
}

func secretaryOperationMessage(
	t *testing.T,
	f *secretaryLifecycleFixture,
	id, body string,
	seq int,
) {
	t.Helper()
	if f.memory != nil {
		secretaryMemoryMessage(f.memory, id, body, int64(seq))
		if state := f.memory.secretaryState; state != nil {
			for _, job := range state.jobs {
				if job.work.SourceAgentID == "source" && job.work.SourceSessionID == "session" {
					f.memory.refreshSecretaryMessagesLocked(job)
				}
			}
		}
		return
	}
	_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type,session_seq,status)
 VALUES($1,'source','session','test','inbound','user','chat',$2,'complete')
 ON CONFLICT(message_id) DO UPDATE SET session_seq=EXCLUDED.session_seq`, id, seq)
	require.NoError(t, err)
	_, err = f.postgres.db.ExecContext(
		t.Context(),
		`INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES($1,0,'text',$2)
 ON CONFLICT(message_id,part_index) DO UPDATE SET text=EXCLUDED.text`,
		id,
		body,
	)
	require.NoError(t, err)
}

func secretaryOperationDraft(t *testing.T, f *secretaryLifecycleFixture, kind, scope string,
	ids ...string,
) domain.SecretaryOperation {
	t.Helper()
	operation, err := secretaryOperationStore(f).PrepareSecretaryOperation(t.Context(), "owner",
		domain.SecretaryOperationPrepare{SourceID: "1", Kind: kind, Scope: scope, MessageIDs: ids})
	require.NoError(t, err)
	require.Equal(t, "draft", operation.Status)
	return operation
}

func secretaryHumanInput(operation domain.SecretaryOperation) domain.SecretaryOperationSubmit {
	return domain.SecretaryOperationSubmit{RequestID: "save:" + operation.OperationID,
		ExpectedRevision: operation.Revision, Title: "Repeated activity", Body: "Repeated manual operations."}
}

func secretaryProcessedCursor(t *testing.T, f *secretaryLifecycleFixture) int64 {
	t.Helper()
	if f.memory != nil {
		return f.memory.secretaryState.jobs["1"].processed
	}
	var value int64
	require.NoError(
		t,
		f.postgres.db.QueryRowContext(t.Context(), `SELECT processed_revision FROM secretary_jobs WHERE source_id=1`).
			Scan(&value),
	)
	return value
}

func TestSecretaryOperationsGivenManualSubsetThenAcknowledgesExactMessagesAndPreservesEarlierHoles(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretaryOperationMessage(t, f, "m2", "second", 2)
		secretaryOperationMessage(t, f, "m3", "third", 3)
		operation := secretaryOperationDraft(t, f, "human_summary", "all")
		require.Len(t, operation.Targets, 3)
		input := secretaryHumanInput(operation)
		input.MessageIDs, input.CoverageMessageIDs = []string{"m2", "m3"}, []string{"m3"}
		result, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		assert.Equal(t, "completed", result.Status)
		require.Len(t, result.Targets, 2)
		require.Len(t, result.ResultRefs, 1)
		assert.Zero(
			t,
			secretaryProcessedCursor(t, f),
			"a later selection must not skip an earlier pending message",
		)
		summary, err := f.store.(domain.SummaryStore).GetSummary(
			t.Context(),
			"owner",
			result.ResultRefs[0].SummaryID,
			0,
		)
		require.NoError(t, err)
		require.Len(t, summary.Coverage, 1)
		assert.Equal(t, "m3", summary.Coverage[0].MessageID)
		assert.Equal(t, "user", summary.AuthorKind)
		assert.Equal(t, "owner", summary.AuthorID)
		assert.Equal(t, operation.OperationID, summary.OperationID)
		replayed, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		assert.Equal(t, result, replayed)
		input.Body = "Changed retry payload"
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		assert.ErrorIs(t, err, ErrConflict)
		work := secretaryClaim(t, f.store)
		require.Len(t, work.Messages, 1)
		assert.Equal(t, "m1", work.Messages[0].SourceID)
	})
}

func TestSecretaryOperationsGivenDisabledAgentAndOversizedMessageThenHumanSaveNeedsNoModelAndSkipsBeforeLoading(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		body := "Opening\n\n" + strings.Repeat("x", domain.SecretaryMaxSourceBytes+1) + "\n\nEnding"
		secretaryOperationMessage(t, f, "m2", body, 2)
		_, err := f.store.UpdateSecretarySettings(
			t.Context(),
			"owner",
			domain.SecretarySettingsUpdate{
				AgentID: "secretary", Enabled: false, ExpectedRevision: f.settings.Revision},
		)
		require.NoError(t, err)
		operation := secretaryOperationDraft(t, f, "human_summary", "selected", "m2")
		input := secretaryHumanInput(operation)
		result, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		assert.Equal(t, "completed", result.Status)
		assert.Zero(t, secretaryProcessedCursor(t, f))
		_, err = f.store.UpdateSecretarySettings(
			t.Context(),
			"owner",
			domain.SecretarySettingsUpdate{
				AgentID: "secretary", Enabled: true, ExpectedRevision: f.settings.Revision + 1},
		)
		require.NoError(t, err)
		work := secretaryClaim(t, f.store)
		require.Len(t, work.Messages, 1)
		assert.Equal(t, "m1", work.Messages[0].SourceID)
		if f.postgres != nil {
			var count int
			require.NoError(
				t,
				f.postgres.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_attempts`).
					Scan(&count),
			)
			assert.Zero(t, count)
		}
	})
}

func TestSecretaryOperationsGivenChangedSourceOrStaleDraftThenKeepsDraftAndRejectsCommit(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		operation := secretaryOperationDraft(t, f, "human_summary", "pending")
		input := secretaryHumanInput(operation)
		input.ExpectedRevision++
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		assert.ErrorIs(t, err, ErrConflict)
		input.ExpectedRevision = operation.Revision
		secretaryOperationMessage(t, f, "m1", "Changed original", 1)
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		assert.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
		unchanged, err := secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "owner", operation.OperationID)
		require.NoError(t, err)
		assert.Equal(t, "draft", unchanged.Status)
		assert.Empty(t, unchanged.ResultRefs)
		assert.Zero(t, secretaryProcessedCursor(t, f))
		_, err = secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "other", operation.OperationID)
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "other", operation.OperationID, input)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSecretaryOperationsGivenRunningAttemptThenRequiresConfirmationAndFencesLateOutput(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		work := f.begin(t, f.prepare(t))
		operation := secretaryOperationDraft(t, f, "human_summary", "pending")
		input := secretaryHumanInput(operation)
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		assert.ErrorIs(t, err, ErrConflict)
		input.ConfirmReplace = true
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		active, err := f.store.SecretaryJobActive(t.Context(), work)
		require.NoError(t, err)
		assert.False(t, active)
		err = f.store.SaveSecretaryAttemptOutput(
			t.Context(),
			work,
			work.AttemptID,
			"{}",
			domain.SecretaryToolReads{},
		)
		assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	})
}

func TestSecretaryOperationsGivenHumanEditThenInheritsSourcesWithoutAdvancingProgressAndBecomesNextBaseline(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		created := secretaryOperationDraft(t, f, "human_summary", "pending")
		first, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", created.OperationID, secretaryHumanInput(created))
		require.NoError(t, err)
		before := secretaryProcessedCursor(t, f)
		ref := first.ResultRefs[0]
		edit, err := secretaryOperationStore(
			f,
		).PrepareSecretaryOperation(t.Context(), "owner", domain.SecretaryOperationPrepare{
			SourceID: "1", Kind: "human_edit", SummaryID: ref.SummaryID})
		require.NoError(t, err)
		require.NotNil(t, edit.Summary)
		input := secretaryHumanInput(edit)
		input.SummaryID, input.ExpectedSummaryRevision, input.Body = ref.SummaryID, ref.Revision, "Corrected summary"
		result, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", edit.OperationID, input)
		require.NoError(t, err)
		assert.Equal(t, before, secretaryProcessedCursor(t, f))
		assert.Equal(t, ref.Revision+1, result.ResultRefs[0].Revision)
		updated, err := f.store.(domain.SummaryStore).GetSummary(
			t.Context(),
			"owner",
			ref.SummaryID,
			0,
		)
		require.NoError(t, err)
		assert.Equal(t, edit.Summary.Coverage, updated.Coverage)
		assert.Equal(t, "Corrected summary", updated.Body)
		f.addMessage(t)
		work := secretaryClaim(t, f.store)
		require.Len(t, work.CurrentSummaries, 1)
		assert.Equal(t, "Corrected summary", work.CurrentSummaries[0].Body)
	})
}

func TestSecretaryOperationsGivenGuidedReprocessThenRunsEvenWhenOriginalCursorIsComplete(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		manual := secretaryOperationDraft(t, f, "human_summary", "pending")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", manual.OperationID, secretaryHumanInput(manual))
		require.NoError(t, err)
		before := secretaryProcessedCursor(t, f)
		operation := secretaryOperationDraft(t, f, "guided_summary", "all")
		input := domain.SecretaryOperationSubmit{
			RequestID:        "guided",
			ExpectedRevision: operation.Revision,
			RunInstruction:   "Keep only important changes.",
		}
		queued, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		assert.Equal(t, "queued", queued.Status)
		work := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
		assert.Equal(t, operation.OperationID, work.OperationID)
		require.NotNil(t, work.Instructions)
		assert.Equal(t, input.RunInstruction, work.Instructions.Run)
		publication := domain.SecretaryBatchPublication{
			RequestID: work.BatchID,
			Outcomes: []domain.SecretaryMessageOutcome{
				{MessageID: "m1", Action: "no_summary", Reason: "Already represented"}},
		}
		_, err = f.store.PublishSecretaryBatch(t.Context(), work, publication)
		require.NoError(t, err)
		finished, err := secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "owner", operation.OperationID)
		require.NoError(t, err)
		assert.Equal(t, "completed", finished.Status)
		assert.Equal(t, before, secretaryProcessedCursor(t, f))
	})
}

func TestSecretaryOperationsGivenMultipleBatchesThenKeepsRunInstructionAndCompletesFrozenScopeOnly(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		for i := 2; i <= 70; i++ {
			secretaryOperationMessage(t, f, fmt.Sprintf("m%d", i), "A repeated operation", i)
		}
		operation := secretaryOperationDraft(t, f, "guided_summary", "pending")
		require.Len(t, operation.Targets, 70)
		input := domain.SecretaryOperationSubmit{
			RequestID:        "many",
			ExpectedRevision: operation.Revision,
			RunInstruction:   "Group repeated operations.",
		}
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		for _, expected := range []int{64, 6} {
			work := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
			require.Len(t, work.Messages, expected)
			assert.Equal(t, operation.OperationID, work.OperationID)
			assert.Equal(t, input.RunInstruction, work.Instructions.Run)
			publication := domain.SecretaryBatchPublication{RequestID: work.BatchID}
			for _, doc := range work.Messages {
				publication.Outcomes = append(publication.Outcomes, domain.SecretaryMessageOutcome{
					MessageID: doc.SourceID, Action: "no_summary", Reason: "Repeated work"})
			}
			_, err = f.store.PublishSecretaryBatch(t.Context(), work, publication)
			require.NoError(t, err)
		}
		finished, err := secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "owner", operation.OperationID)
		require.NoError(t, err)
		assert.Equal(t, "completed", finished.Status)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
	})
}
