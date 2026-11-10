package secretary

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func validationFeedbackJob(category, failure string) domain.SecretaryJob {
	job := testJob()
	job.BatchID, job.AttemptID = "feedback-batch", "previous-attempt"
	job.ValidationFeedback = category
	job.Attempt = &domain.SecretaryAttempt{
		ID: job.AttemptID, BatchID: job.BatchID, State: "invalid", Error: failure,
	}
	return job
}

func nodePromptPayload(t *testing.T, job domain.SecretaryJob) map[string]json.RawMessage {
	t.Helper()
	prompt, err := BuildNodePrompt(job)
	require.NoError(t, err)
	var payload map[string]json.RawMessage
	require.NoError(
		t,
		json.Unmarshal([]byte(strings.TrimPrefix(prompt, nodeSummaryInstructions)), &payload),
	)
	return payload
}

func assertValidationFeedback(t *testing.T, payload map[string]json.RawMessage, code, hint string) {
	t.Helper()
	var feedback struct {
		Code string `json:"code"`
		Hint string `json:"hint"`
	}
	require.True(
		t,
		len(payload["validation_feedback"]) > 0,
		"safe correction feedback must be present",
	)
	require.NoError(t, json.Unmarshal(payload["validation_feedback"], &feedback))
	assert.Equal(t, code, feedback.Code)
	assert.Contains(t, feedback.Hint, hint)
	assert.Less(t, len(feedback.Hint), 512)
}

func TestNodeValidationFeedbackGivenWhitelistedPreviousInvalidAttemptThenExplainsTheCorrection(
	t *testing.T,
) {
	for _, scenario := range []struct {
		name, category, failure, code, hint string
	}{
		{"bound capacity", "schema", "secretary batch publication exceeds 64 KiB after source binding", "source_binding_capacity", "bound"},
		{"persisted capacity", "schema", "summary batch exceeds the 64 KiB budget: conflict", "source_binding_capacity", "bound"},
		{"compact stored capacity", "schema", "secretary batch publication exceeds 64 KiB after source binding: conflict", "source_binding_capacity", "bound"},
		{"quote not visible", "evidence", "secretary evidence was not present in the supplied or verified text", "quote_not_visible", "last paragraph"},
		{"empty quote", "evidence", "secretary evidence quote is empty", "quote_not_visible", "exact quote"},
		{"source unavailable", "evidence", "summary selected an unavailable source message", "source_unavailable", "exact"},
		{"baseline unavailable", "evidence", "summary revision requires its exact read baseline", "source_unavailable", "read"},
		{"reference mismatch", "evidence", "secretary evidence does not match its trusted version or view", "source_unavailable", "version"},
		{"unknown parser failure", "schema", "private JSON parser output and user text", "schema", "JSON"},
		{"unknown evidence failure", "evidence", "private evidence details", "evidence", "verified"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			payload := nodePromptPayload(
				t,
				validationFeedbackJob(scenario.category, scenario.failure),
			)
			assert.JSONEq(
				t,
				`"`+scenario.category+`"`,
				string(payload["previous_validation_failure"]),
			)
			assertValidationFeedback(t, payload, scenario.code, scenario.hint)
			assert.NotContains(t, string(payload["validation_feedback"]), scenario.failure)
		})
	}
}

func TestNodeValidationFeedbackGivenUntrustedOrUnrelatedErrorThenNeverCopiesItOrMatchesSubstrings(
	t *testing.T,
) {
	known := "secretary batch publication exceeds 64 KiB after source binding"
	for _, scenario := range []struct {
		name string
		edit func(*domain.SecretaryJob)
	}{
		{"raw prefix", func(job *domain.SecretaryJob) { job.Attempt.Error = "private-source " + known }},
		{"raw suffix", func(job *domain.SecretaryJob) { job.Attempt.Error = known + " private-source" }},
		{"leading newline", func(job *domain.SecretaryJob) { job.Attempt.Error = "\n" + known }},
		{"huge raw error", func(job *domain.SecretaryJob) { job.Attempt.Error = strings.Repeat("private-source", 100000) }},
		{"no attempt", func(job *domain.SecretaryJob) { job.Attempt = nil }},
		{"unknown state", func(job *domain.SecretaryJob) { job.Attempt.State = "unknown" }},
		{"running state", func(job *domain.SecretaryJob) { job.Attempt.State = "running" }},
		{"foreign batch", func(job *domain.SecretaryJob) { job.Attempt.BatchID = "other-batch" }},
		{"old attempt", func(job *domain.SecretaryJob) { job.Attempt.ID = "other-attempt" }},
		{"not dispatched", func(job *domain.SecretaryJob) { job.Attempt.NotDispatched = true }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			job := validationFeedbackJob("schema", known)
			scenario.edit(&job)
			payload := nodePromptPayload(t, job)
			assertValidationFeedback(t, payload, "schema", "JSON")
			assert.NotContains(t, string(payload["validation_feedback"]), "private-source")
			assert.NotContains(t, string(payload["validation_feedback"]), known)
		})
	}
}

func TestNodeValidationFeedbackGivenNoSafeFailureThenOmitsOptionalFeedback(t *testing.T) {
	for _, category := range []string{"", "private-source", "untrusted"} {
		job := validationFeedbackJob(category, "private-source")
		payload := nodePromptPayload(t, job)
		assert.NotContains(t, payload, "previous_validation_failure")
		assert.NotContains(t, payload, "validation_feedback")
	}
}

func TestNodeWorkerGivenPreviousInvalidAttemptThenNextExecutionReceivesOnlySafeCorrection(
	t *testing.T,
) {
	store := newNodeWorkerStore()
	store.stored = &domain.SecretaryAttempt{
		ID: "previous-attempt", BatchID: "fixed-batch", State: "invalid",
		Error: "secretary evidence was not present in the supplied or verified text",
	}
	store.jobs[0].AttemptID = store.stored.ID
	store.jobs[0].ValidationFeedback = "evidence"
	worker := NewWorker(
		store,
		ExecutorFunc(func(_ context.Context, request ExecutionRequest) (ExecutionResult, error) {
			var payload map[string]json.RawMessage
			require.NoError(
				t,
				json.Unmarshal(
					[]byte(strings.TrimPrefix(request.Prompt, nodeSummaryInstructions)),
					&payload,
				),
			)
			assertValidationFeedback(t, payload, "quote_not_visible", "last paragraph")
			assert.Equal(t, "attempt-one", request.RunID)
			return ExecutionResult{
				Text: nodeBatchOutput(
					t,
					[]generatedNodeSummary{newNodeProposal()},
					summarizedNodeOutcome(),
				),
			}, nil
		}),
		WorkerConfig{},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()))
	assert.Equal(t, []string{"prepare", "begin", "save", "publish"}, store.events)
}
