package secretary

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestRollingWorkerGivenEmptyResetThenPublishesNeutralStateWithoutModel(t *testing.T) {
	store := newWorkerStore()
	store.jobs[0].Messages = nil
	store.jobs[0].RemovedMessageIDs = []string{"deleted-message"}
	store.jobs[0].ResetRequired = true
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			t.Fatal("an empty reset must not invoke the agent")
			return ExecutionResult{}, nil
		}),
		WorkerConfig{},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()))
	require.Len(t, store.publications, 1)
	assert.Equal(
		t,
		"No readable source messages were available in this update.",
		store.publications[0].Body,
	)
	assert.Empty(t, store.publications[0].Coverage)
}

func TestRollingWorkerGivenOptionalRelatedReadThenAuthorizesOnlySelectedExactSources(t *testing.T) {
	store := newWorkerStore()
	job := store.jobs[0]
	ref := domain.MessageVersionRef{
		MessageID:               "related",
		ContentHash:             domain.SearchContentHash("Related fact"),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		ReadReceipt:             "verified-receipt",
	}
	output, err := json.Marshal(
		map[string]any{
			"body":            "Updated summary",
			"related_sources": []domain.MessageVersionRef{ref},
		},
	)
	require.NoError(t, err)
	worker := NewWorker(
		store,
		ExecutorFunc(func(_ context.Context, request ExecutionRequest) (ExecutionResult, error) {
			assert.Equal(t, job, request.Job)
			return ExecutionResult{
				Text: string(output),
				Reads: domain.SecretaryToolReads{
					Messages: []domain.SecretaryReadMessage{
						{
							Ref:         ref,
							Text:        "Related fact",
							View:        "full",
							ReadReceipt: ref.ReadReceipt,
						},
					},
				},
			}, nil
		}),
		WorkerConfig{},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()))
	require.Len(t, store.publishedJobs, 1)
	assert.Equal(t, []domain.MessageVersionRef{ref}, store.publishedJobs[0].AllowedRelatedMessages)
	assert.Len(
		t,
		store.publishedJobs[0].Messages,
		len(job.Messages),
		"related reads do not become mandatory source-session work",
	)
}
