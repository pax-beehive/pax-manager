package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryTaskViewGivenBlockedPausedOrIncompleteSourceThenDoesNotAdvertiseAutomaticRetry(
	t *testing.T,
) {
	now := time.Date(2026, 11, 4, 23, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"retry", "paused", "unknown", "blocked", "incomplete", "execution_blocked"} {
		t.Run(scenario, func(t *testing.T) {
			record := secretaryTaskRecord{desired: 2, processed: 1, enabled: true,
				available: now.Add(time.Minute), sourceRuntime: "idle"}
			expected := "waiting_retry"
			switch scenario {
			case "paused":
				record.task.Paused = true
			case "unknown", "blocked":
				record.batchState, expected = scenario, scenario
			case "incomplete":
				record.failure, expected = domain.ErrSecretarySourceIncomplete.Error(), "waiting_source"
			case "execution_blocked":
				record.batchState, record.blocked, expected = "pending", true, "blocked"
			}
			task := secretaryTaskView(record, now)
			assert.Equal(t, expected, task.ExecutionState)
			if scenario == "retry" {
				assert.NotNil(t, task.NextRetryAt)
			} else {
				assert.Nil(t, task.NextRetryAt)
			}
			if scenario == "incomplete" {
				assert.Equal(t, "source_incomplete", task.ErrorCode)
			}
		})
	}
}

func TestSecretaryTaskMetadataGivenKnownFailureKindsThenUsesOnlyFixedSafeMessages(t *testing.T) {
	for _, tc := range []struct {
		state, attempt, failure, code string
		notDispatched                 bool
	}{
		{state: "unknown", failure: secretaryModelConnectionFailure, code: "model_connection_failed"},
		{state: "unknown", code: "execution_unknown"},
		{failure: secretaryInputLimitPrefix + "bounded batch", code: "input_size_limit"},
		{state: "pending", notDispatched: true, code: "connection_backoff"},
		{state: "blocked", notDispatched: true, code: "connection_retries_exhausted"},
		{attempt: "invalid", code: "invalid_output"},
		{attempt: "failed", code: "execution_failed"},
		{attempt: "canceled", code: "retry_required"},
		{state: "blocked", code: "retry_required"},
		{failure: "secret tool output", code: "retry_required"},
		{},
	} {
		code, message := secretaryTaskFailure(tc.state, tc.attempt, tc.failure, tc.notDispatched)
		assert.Equal(t, tc.code, code)
		assert.NotContains(t, message, "secret tool output")
	}
	assert.Equal(
		t,
		"custom",
		secretaryTaskSessionTitle(
			domain.AgentSession{CustomSessionName: "custom", SessionName: "generated"},
		),
	)
	assert.Equal(
		t,
		"generated",
		secretaryTaskSessionTitle(domain.AgentSession{SessionName: "generated"}),
	)
	assert.Equal(t, "session", secretaryTaskSessionTitle(domain.AgentSession{SessionID: "session"}))
	assert.Equal(t, 2, secretaryTaskBatchPriority("pending"))
	assert.Equal(t, 1, secretaryTaskBatchPriority("blocked"))
	assert.Equal(t, 0, secretaryTaskBatchPriority("committed"))
}

func TestSecretaryTaskViewGivenExplicitReprocessingThenDoesNotReportCompletedFromOldWatermark(
	t *testing.T,
) {
	now := time.Now()
	record := secretaryTaskRecord{desired: 10, processed: 10, enabled: true, operationPending: true}
	assert.Equal(t, "queued", secretaryTaskView(record, now).ExecutionState)
	record.batchState = "unknown"
	assert.Equal(t, "unknown", secretaryTaskView(record, now).ExecutionState)
	record.operationPending = false
	assert.Equal(t, "completed", secretaryTaskView(record, now).ExecutionState)
}

func TestSecretaryTaskQueryGivenMalformedBoundCursorThenRejectsIt(t *testing.T) {
	owner := "owner"
	_, _, err := secretaryTaskAfter(owner, domain.SecretaryTaskQuery{
		Cursor: secretaryTaskNextCursor(owner, "list", "-1"),
	})
	assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	for _, after := range []string{"no-separator", "invalid-time\x00attempt", "2026-11-04T00:00:00Z\x00", "0001-01-01T00:00:00Z\x00attempt"} {
		_, _, _, err = secretaryTaskAttemptAfter(owner, "1", domain.SecretaryTaskQuery{
			Cursor: secretaryTaskNextCursor(owner, "attempts:1", after),
		})
		assert.ErrorIs(t, err, domain.ErrInvalidCursor)
	}
	_, _, err = secretaryTaskQuery(
		owner,
		"list",
		domain.SecretaryTaskQuery{Cursor: strings.Repeat("a", 4097)},
	)
	assert.ErrorIs(t, err, domain.ErrInvalidCursor)
}
