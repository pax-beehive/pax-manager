package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

type taskRecoveryTestStore struct {
	domain.SecretaryStore
	domain.SecretaryTaskStore
	domain.SecretaryExecutionStore
	domain.SecretaryNodeStore
	job                              domain.SecretaryJob
	claimErr, releaseErr, publishErr error
	events                           []string
	owner, taskID                    string
	action                           domain.SecretaryTaskAction
}

func (s *taskRecoveryTestStore) ClaimSecretaryTaskRecovery(
	ctx context.Context,
	owner, id string,
	action domain.SecretaryTaskAction,
	lease time.Duration,
) (domain.SecretaryJob, error) {
	s.events = append(s.events, "claim")
	s.owner, s.taskID, s.action = owner, id, action
	return s.job, s.claimErr
}

func (s *taskRecoveryTestStore) ReleaseSecretaryTaskRecovery(
	ctx context.Context,
	job domain.SecretaryJob,
) error {
	s.events = append(s.events, "release")
	return errors.Join(ctx.Err(), s.releaseErr)
}

func (s *taskRecoveryTestStore) SecretaryJobActive(
	context.Context,
	domain.SecretaryJob,
) (bool, error) {
	return true, nil
}

func (s *taskRecoveryTestStore) SaveSecretaryAttemptOutput(
	context.Context,
	domain.SecretaryJob,
	string,
	string,
	domain.SecretaryToolReads,
) error {
	s.events = append(s.events, "save")
	return nil
}

func (s *taskRecoveryTestStore) FailSecretaryAttempt(
	context.Context,
	domain.SecretaryJob,
	string,
	error,
) error {
	s.events = append(s.events, "fail-attempt")
	return nil
}

func (s *taskRecoveryTestStore) PublishSecretaryBatch(
	context.Context,
	domain.SecretaryJob,
	domain.SecretaryBatchPublication,
) ([]domain.SummaryVersion, error) {
	s.events = append(s.events, "publish")
	return nil, s.publishErr
}

type taskRecoveryTestExecutor struct {
	secretary.Executor
	output    string
	completed bool
	err       error
}

func (e taskRecoveryTestExecutor) Recover(
	context.Context,
	secretary.ExecutionRequest,
) (secretary.ExecutionResult, bool, error) {
	return secretary.ExecutionResult{Text: e.output}, e.completed, e.err
}

func taskRecoveryStoreFixture() *taskRecoveryTestStore {
	message := domain.SearchDocument{
		Kind: "message", SourceID: "m1", OwnerUserID: "owner", AgentID: "source", SessionID: "source-session",
		Body: "A greeting.", ContentHash: domain.SearchContentHash("A greeting."),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
	}
	return &taskRecoveryTestStore{job: domain.SecretaryJob{
		OwnerUserID: "owner", AgentID: "secretary", SourceID: "12", SourceAgentID: "source", SourceSessionID: "source-session",
		BatchID: "batch", AttemptID: "attempt", RunID: "attempt", LeaseToken: "recheck-lease",
		Attempt:  &domain.SecretaryAttempt{ID: "attempt", BatchID: "batch", State: "unknown"},
		Messages: []domain.SearchDocument{message},
	}}
}

const taskRecoveryNoSummary = `{"summaries":[],"outcomes":[{"message_id":"m1","action":"no_summary","reason":"Greeting only"}]}`

func TestSecretaryTaskRecoveryGivenCompletedOriginalThenPublishesAndReleasesOnlyItsLease(
	t *testing.T,
) {
	store := taskRecoveryStoreFixture()
	action := domain.SecretaryTaskAction{
		Action:           "recheck",
		BatchID:          "batch",
		AttemptID:        "attempt",
		ExpectedRevision: 7,
	}
	outcome, err := recoverSecretaryTask(
		t.Context(),
		store,
		taskRecoveryTestExecutor{output: taskRecoveryNoSummary, completed: true},
		"owner",
		"12",
		action,
	)
	require.NoError(t, err)
	assert.Equal(t, "recovered", outcome)
	assert.Equal(t, []string{"claim", "save", "publish", "release"}, store.events)
	assert.Equal(t, "owner", store.owner)
	assert.Equal(t, "12", store.taskID)
	assert.Equal(t, action, store.action)
}

func TestSecretaryTaskRecoveryGivenUnknownOrInvalidResultThenReturnsAnExplicitOutcome(
	t *testing.T,
) {
	for _, test := range []struct {
		name, output, outcome string
		completed             bool
	}{
		{"unknown", "", "still_unknown", false},
		{"invalid", "not valid summary JSON", "result_invalid", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := taskRecoveryStoreFixture()
			outcome, err := recoverSecretaryTask(
				t.Context(),
				store,
				taskRecoveryTestExecutor{output: test.output, completed: test.completed},
				"owner",
				"12",
				domain.SecretaryTaskAction{Action: "recheck"},
			)
			require.NoError(t, err)
			assert.Equal(t, test.outcome, outcome)
			assert.Equal(t, "release", store.events[len(store.events)-1])
			assert.NotContains(t, store.events, "publish")
		})
	}
}

func TestSecretaryTaskRecoveryGivenLostRequestOrStorageFailureThenReleasesWithIndependentContext(
	t *testing.T,
) {
	for _, phase := range []string{"canceled", "claim", "publish", "release"} {
		t.Run(phase, func(t *testing.T) {
			store := taskRecoveryStoreFixture()
			failure := errors.New("storage temporarily unavailable")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch phase {
			case "canceled":
				cancel()
				failure = context.Canceled
			case "claim":
				store.claimErr = failure
			case "publish":
				store.publishErr = failure
			case "release":
				store.releaseErr = failure
			}
			outcome, err := recoverSecretaryTask(
				ctx,
				store,
				taskRecoveryTestExecutor{output: taskRecoveryNoSummary, completed: true},
				"owner",
				"12",
				domain.SecretaryTaskAction{Action: "recheck"},
			)
			assert.ErrorIs(t, err, failure)
			assert.Empty(t, outcome)
			if phase == "claim" {
				assert.Equal(t, []string{"claim"}, store.events)
			} else {
				assert.Equal(t, "release", store.events[len(store.events)-1])
			}
		})
	}
}

func TestSecretaryTaskRecoveryGivenSavedOutputThenServiceNeedsNoModelExecutor(t *testing.T) {
	store := taskRecoveryStoreFixture()
	store.job.Attempt.State, store.job.Attempt.Output = "output_received", taskRecoveryNoSummary
	srv := &Service{secretaryStore: store}
	outcome, err := srv.recheckSecretaryTask(
		t.Context(),
		"owner",
		"12",
		domain.SecretaryTaskAction{Action: "recheck"},
	)
	require.NoError(t, err)
	assert.Equal(t, "recovered", outcome)
	assert.Equal(t, []string{"claim", "publish", "release"}, store.events)
	_, err = (&Service{}).recheckSecretaryTask(
		t.Context(),
		"owner",
		"12",
		domain.SecretaryTaskAction{Action: "recheck"},
	)
	require.Error(t, err)
}

func TestSecretaryTaskRecoveryGivenVerifiedFailureThenReturnsFailedWithoutSavingOutput(
	t *testing.T,
) {
	for _, test := range []struct {
		name    string
		failure error
		outcome string
		events  []string
	}{
		{"verified_failure", domain.ErrSecretaryExecutionFailed, "result_failed", []string{"claim", "fail-attempt", "release"}},
		{"conflicting_terminal", errors.Join(domain.ErrSecretaryExecutionFailed, domain.ErrSecretaryExecutionUnknown), "still_unknown", []string{"claim", "release"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := taskRecoveryStoreFixture()
			outcome, err := recoverSecretaryTask(
				t.Context(),
				store,
				taskRecoveryTestExecutor{output: "partial output", err: test.failure},
				"owner",
				"12",
				domain.SecretaryTaskAction{Action: "recheck"},
			)
			require.NoError(t, err)
			assert.Equal(t, test.outcome, outcome)
			assert.Equal(t, test.events, store.events)
		})
	}
}
