package storage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryGuidanceGivenOperationSpanningBatchesThenDefaultsFreezeUntilOnlyThatScopeCompletes(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		guidance := f.store.(domain.SecretaryGuidanceStore)
		set := func(scope, instruction string, revision int64) {
			source := ""
			if scope == domain.SecretaryGuidanceSession {
				source = "1"
			}
			_, err := guidance.UpdateSecretaryGuidance(
				t.Context(),
				"owner",
				scope,
				source,
				domain.SecretaryGuidanceUpdate{
					Instruction:      instruction,
					ExpectedRevision: revision,
				},
			)
			require.NoError(t, err)
		}
		set(domain.SecretaryGuidanceUser, "Keep important decisions.", 0)
		set(domain.SecretaryGuidanceSession, "Condense recurring procedures.", 0)
		for i := 2; i <= 70; i++ {
			secretaryOperationMessage(
				t,
				f,
				fmt.Sprintf("m%d", i),
				"A completed recurring procedure",
				i,
			)
		}
		operation := secretaryOperationDraft(t, f, "guided_summary", "pending")
		require.NotNil(t, operation.Instructions)
		assert.EqualValues(t, 1, operation.Instructions.User.Revision)
		assert.EqualValues(t, 1, operation.Instructions.Session.Revision)
		set(domain.SecretaryGuidanceUser, "New user default.", 1)
		set(domain.SecretaryGuidanceSession, "New session preference.", 1)
		input := domain.SecretaryOperationSubmit{
			RequestID:        "frozen-guidance",
			ExpectedRevision: operation.Revision,
			RunInstruction:   "  Condense repetitive operations.\n",
		}
		queued, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		assert.Equal(t, operation.Instructions.User, queued.Instructions.User)
		assert.Equal(t, operation.Instructions.Session, queued.Instructions.Session)
		assert.Equal(t, "Condense repetitive operations.", queued.Instructions.Run)
		for index, count := range []int{64, 6} {
			work := secretaryNodesReadyWork(t, f.store, secretaryClaim(t, f.store))
			require.Len(t, work.Messages, count)
			assert.Equal(t, operation.OperationID, work.OperationID)
			assert.Equal(t, queued.Instructions, work.Instructions)
			publication := domain.SecretaryBatchPublication{RequestID: work.BatchID}
			for _, message := range work.Messages {
				assert.NotEqual(t, "m71", message.SourceID)
				publication.Outcomes = append(publication.Outcomes, domain.SecretaryMessageOutcome{
					MessageID: message.SourceID, Action: "no_summary", Reason: "Already represented"})
			}
			_, err = f.store.PublishSecretaryBatch(t.Context(), work, publication)
			require.NoError(t, err)
			if index == 0 {
				set(domain.SecretaryGuidanceUser, "Latest user default.", 2)
				set(domain.SecretaryGuidanceSession, "Latest session preference.", 2)
				secretaryOperationMessage(t, f, "m71", "A later independent conclusion", 71)
			}
		}
		completed, err := secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "owner", operation.OperationID)
		require.NoError(t, err)
		assert.Equal(t, "completed", completed.Status)
		assert.Equal(t, queued.Instructions, completed.Instructions)
		next := secretaryClaim(t, f.store)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "m71", next.Messages[0].SourceID)
		assert.Empty(t, next.OperationID)
		require.NotNil(t, next.Instructions)
		assert.EqualValues(t, 3, next.Instructions.User.Revision)
		assert.EqualValues(t, 3, next.Instructions.Session.Revision)
		assert.Equal(t, "Latest user default.", next.Instructions.User.Instruction)
		assert.Equal(t, "Latest session preference.", next.Instructions.Session.Instruction)
		assert.Empty(t, next.Instructions.Run)
	})
}
