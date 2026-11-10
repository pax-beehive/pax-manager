package userapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryTaskAPIFixture struct {
	domain.SecretaryTaskStore
	owner      string
	query      domain.SecretaryTaskQuery
	task       domain.SecretaryTask
	attempts   []domain.SecretaryTaskAttempt
	err        error
	controlled int
	reads      int
	action     domain.SecretaryTaskAction
}

func (f *secretaryTaskAPIFixture) ListSecretaryTasks(
	_ context.Context,
	owner string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskPage, error) {
	f.owner, f.query = owner, query
	return domain.SecretaryTaskPage{
		Items:      []domain.SecretaryTask{f.task},
		NextCursor: "next",
	}, f.err
}

func (f *secretaryTaskAPIFixture) GetSecretaryTask(
	_ context.Context,
	owner, id string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskDetail, error) {
	f.owner, f.query = owner, query
	f.reads++
	return domain.SecretaryTaskDetail{Task: f.task, Attempts: f.attempts, NextCursor: "next"}, f.err
}

func (f *secretaryTaskAPIFixture) ControlSecretaryTask(
	_ context.Context,
	owner, id string,
	action domain.SecretaryTaskAction,
) (domain.SecretaryTask, error) {
	f.owner, f.action = owner, action
	f.controlled++
	return f.task, f.err
}

func taskAPIAction(action string) domain.SecretaryTaskAction {
	return domain.SecretaryTaskAction{
		Action:           action,
		ExpectedRevision: 3,
		SettingsRevision: 2,
		RetryEpoch:       1,
		BatchID:          "batch",
		AttemptID:        "attempt",
	}
}

func TestSecretaryTaskAPIGivenOwnerThenBoundsPagesAndSanitizesAllErrors(t *testing.T) {
	store := &secretaryTaskAPIFixture{
		task: domain.SecretaryTask{
			TaskID:       "task",
			ErrorCode:    "model_connection_failed",
			ErrorMessage: "private provider text",
		},
		attempts: []domain.SecretaryTaskAttempt{
			{ErrorCode: "private", ErrorMessage: "private provider text"},
		},
	}
	service := NewSecretaryTaskService(workspacePrincipal{owner: "owner"}, store, nil)
	page, err := service.List(t.Context(), auth.RequestMetadata{}, domain.SecretaryTaskQuery{})
	require.NoError(t, err)
	assert.Equal(t, "owner", store.owner)
	assert.Equal(t, 20, store.query.Limit)
	assert.Equal(t, "next", page.NextCursor)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "model_connection_failed", page.Items[0].ErrorCode)
	assert.NotContains(t, page.Items[0].ErrorMessage, "private")
	detail, err := service.Get(
		t.Context(),
		auth.RequestMetadata{},
		"task",
		domain.SecretaryTaskQuery{Limit: 100, Cursor: "cursor"},
	)
	require.NoError(t, err)
	assert.Equal(t, 100, store.query.Limit)
	assert.Equal(t, "cursor", store.query.Cursor)
	require.Len(t, detail.Attempts, 1)
	assert.Empty(t, detail.Attempts[0].ErrorCode)
	assert.Empty(t, detail.Attempts[0].ErrorMessage)
	encoded, err := json.Marshal(detail)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private provider text")
}

func TestSecretaryTaskAPIGivenBadPaginationOrIDThenDoesNotReadStore(t *testing.T) {
	store := &secretaryTaskAPIFixture{}
	service := NewSecretaryTaskService(workspacePrincipal{owner: "owner"}, store, nil)
	for _, query := range []domain.SecretaryTaskQuery{{Limit: -1}, {Limit: 101}, {Cursor: "bad\n"}, {Cursor: strings.Repeat("a", 2049)}} {
		_, err := service.List(t.Context(), auth.RequestMetadata{}, query)
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
		_, err = service.Get(t.Context(), auth.RequestMetadata{}, "task", query)
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
	}
	for _, id := range []string{"", " bad", strings.Repeat("x", 513)} {
		_, err := service.Get(t.Context(), auth.RequestMetadata{}, id, domain.SecretaryTaskQuery{})
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
	}
	assert.Zero(t, store.reads)
}

func TestSecretaryTaskAPIGivenMutationThenOnlyControlsTheSelectedSource(t *testing.T) {
	for action, outcome := range map[string]string{"pause": "paused", "resume": "resumed", "retry": "queued"} {
		t.Run(action, func(t *testing.T) {
			store := &secretaryTaskAPIFixture{
				task: domain.SecretaryTask{TaskID: "task", ExecutionState: "blocked"},
			}
			service := NewSecretaryTaskService(
				workspacePrincipal{owner: "owner"},
				store,
				func(context.Context, string, string, domain.SecretaryTaskAction) (string, error) {
					t.Fatal("not a recheck")
					return "", nil
				},
			)
			request := taskAPIAction(action)
			result, err := service.Control(t.Context(), auth.RequestMetadata{}, "task", request)
			require.NoError(t, err)
			assert.Equal(t, "owner", store.owner)
			assert.Equal(t, request, store.action)
			assert.Equal(t, 1, store.controlled)
			assert.Equal(t, outcome, result.Outcome)
		})
	}
}

func TestSecretaryTaskAPIGivenUnknownRetryThenRequiresDuplicateRiskConfirmation(t *testing.T) {
	for _, state := range []string{"unknown", "needs_recovery", "running"} {
		store := &secretaryTaskAPIFixture{task: domain.SecretaryTask{ExecutionState: state}}
		service := NewSecretaryTaskService(workspacePrincipal{owner: "owner"}, store, nil)
		request := taskAPIAction("retry")
		_, err := service.Control(t.Context(), auth.RequestMetadata{}, "task", request)
		assertTaskAPIStatus(t, err, http.StatusConflict)
		assert.Zero(t, store.controlled)
		request.ConfirmRestart = true
		_, err = service.Control(t.Context(), auth.RequestMetadata{}, "task", request)
		require.NoError(t, err)
		assert.Equal(t, 1, store.controlled)
	}
}

func TestSecretaryTaskAPIGivenRecheckThenOnlyObservesExistingResultAndReloads(t *testing.T) {
	for _, outcome := range []string{"recovered", "still_unknown", "result_invalid", "result_failed", "private result"} {
		store := &secretaryTaskAPIFixture{task: domain.SecretaryTask{TaskID: "task"}}
		calls := 0
		service := NewSecretaryTaskService(
			workspacePrincipal{owner: "owner"},
			store,
			func(_ context.Context, owner, id string, request domain.SecretaryTaskAction) (string, error) {
				calls++
				assert.Equal(t, "owner", owner)
				assert.Equal(t, "task", id)
				assert.Equal(t, "recheck", request.Action)
				return outcome, nil
			},
		)
		result, err := service.Control(
			t.Context(),
			auth.RequestMetadata{},
			"task",
			taskAPIAction("recheck"),
		)
		require.NoError(t, err)
		assert.Equal(t, 1, calls)
		assert.Equal(t, 1, store.reads)
		assert.Zero(t, store.controlled)
		assert.NotContains(t, result.Outcome, "private")
		if outcome == "private result" {
			assert.Equal(t, "status_refreshed", result.Outcome)
		} else {
			assert.Equal(t, outcome, result.Outcome)
		}
	}
}

func TestSecretaryTaskAPIGivenInvalidActionThenNeverMutates(t *testing.T) {
	store := &secretaryTaskAPIFixture{}
	service := NewSecretaryTaskService(workspacePrincipal{owner: "owner"}, store, nil)
	invalid := []domain.SecretaryTaskAction{
		taskAPIAction("other"),
		taskAPIAction("pause"),
		taskAPIAction("pause"),
		taskAPIAction("pause"),
		taskAPIAction("pause"),
		taskAPIAction("pause"),
	}
	invalid[1].ExpectedRevision = -1
	invalid[2].SettingsRevision = -1
	invalid[3].RetryEpoch = -1
	invalid[4].BatchID = strings.Repeat("b", 513)
	invalid[5].AttemptID = "bad\n"
	for _, request := range invalid {
		_, err := service.Control(t.Context(), auth.RequestMetadata{}, "task", request)
		assertTaskAPIStatus(t, err, http.StatusBadRequest)
	}
	_, err := service.Control(t.Context(), auth.RequestMetadata{}, "", taskAPIAction("pause"))
	assertTaskAPIStatus(t, err, http.StatusBadRequest)
	assert.Zero(t, store.controlled)
}

func TestSecretaryTaskAPIGivenUnavailableOrFailingDependenciesThenReturnsSafeErrors(t *testing.T) {
	for _, principal := range []PrincipalResolver{nil, workspacePrincipal{}, workspacePrincipal{err: domain.ErrUnauthorized}} {
		service := NewSecretaryTaskService(principal, &secretaryTaskAPIFixture{}, nil)
		_, err := service.List(t.Context(), auth.RequestMetadata{}, domain.SecretaryTaskQuery{})
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		_, err = service.Get(
			t.Context(),
			auth.RequestMetadata{},
			"task",
			domain.SecretaryTaskQuery{},
		)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		_, err = service.Control(
			t.Context(),
			auth.RequestMetadata{},
			"task",
			taskAPIAction("pause"),
		)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
	}
	service := NewSecretaryTaskService(workspacePrincipal{owner: "owner"}, nil, nil)
	_, err := service.List(t.Context(), auth.RequestMetadata{}, domain.SecretaryTaskQuery{})
	assertTaskAPIStatus(t, err, http.StatusServiceUnavailable)
	for _, failure := range []error{domain.ErrInvalidCursor, domain.ErrConflict, domain.ErrNotFound, domain.ErrUnauthorized, errors.New("private database error"), apperr.Error{Status: 400, Message: "private server error"}} {
		store := &secretaryTaskAPIFixture{err: failure}
		service = NewSecretaryTaskService(workspacePrincipal{owner: "owner"}, store, nil)
		_, err = service.List(t.Context(), auth.RequestMetadata{}, domain.SecretaryTaskQuery{})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private")
		_, err = service.Get(
			t.Context(),
			auth.RequestMetadata{},
			"task",
			domain.SecretaryTaskQuery{},
		)
		require.Error(t, err)
		_, err = service.Control(
			t.Context(),
			auth.RequestMetadata{},
			"task",
			taskAPIAction("pause"),
		)
		require.Error(t, err)
		_, err = service.Control(
			t.Context(),
			auth.RequestMetadata{},
			"task",
			taskAPIAction("retry"),
		)
		require.Error(t, err)
	}
	service = NewSecretaryTaskService(
		workspacePrincipal{owner: "owner"},
		&secretaryTaskAPIFixture{},
		nil,
	)
	_, err = service.Control(t.Context(), auth.RequestMetadata{}, "task", taskAPIAction("recheck"))
	assertTaskAPIStatus(t, err, http.StatusServiceUnavailable)
	service = NewSecretaryTaskService(
		workspacePrincipal{owner: "owner"},
		&secretaryTaskAPIFixture{},
		func(context.Context, string, string, domain.SecretaryTaskAction) (string, error) {
			return "", errors.New("private provider error")
		},
	)
	_, err = service.Control(t.Context(), auth.RequestMetadata{}, "task", taskAPIAction("recheck"))
	assertTaskAPIStatus(t, err, http.StatusInternalServerError)
}

func assertTaskAPIStatus(t *testing.T, err error, status int) {
	t.Helper()
	var response apperr.Error
	require.ErrorAs(t, err, &response)
	assert.Equal(t, status, response.Status)
}

func TestSecretaryTaskAPIGivenKnownFailureCodesThenUsesOnlyFixedMessages(t *testing.T) {
	for _, code := range []string{"model_connection_failed", "execution_unknown", "connection_backoff", "connection_retries_exhausted", "invalid_output", "execution_failed", "retry_required", "source_incomplete", "private provider failure"} {
		got, message := safeSecretaryTaskFailure(code)
		if code == "private provider failure" {
			assert.Empty(t, got)
			assert.Empty(t, message)
		} else {
			assert.Equal(t, code, got)
			assert.NotEmpty(t, message)
		}
		assert.NotContains(t, message, "private provider failure")
		if code == "source_incomplete" {
			assert.Equal(t, "Waiting for the source turn to finish.", message)
		}
	}
}

func TestSecretaryTaskAPIGivenRecheckReadFailureThenDoesNotReturnStaleTask(t *testing.T) {
	store := &secretaryTaskAPIFixture{err: domain.ErrNotFound}
	service := NewSecretaryTaskService(
		workspacePrincipal{owner: "owner"},
		store,
		func(context.Context, string, string, domain.SecretaryTaskAction) (string, error) {
			return "recovered", nil
		},
	)
	_, err := service.Control(t.Context(), auth.RequestMetadata{}, "task", taskAPIAction("recheck"))
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSecretaryTaskAPIGivenChangedRecheckSourcesThenReportsConflictWithoutStartingAgain(
	t *testing.T,
) {
	for _, failure := range []error{domain.ErrSourceVersionUnavailable, domain.ErrSummaryVersionMismatch} {
		store := &secretaryTaskAPIFixture{}
		service := NewSecretaryTaskService(
			workspacePrincipal{owner: "owner"},
			store,
			func(context.Context, string, string, domain.SecretaryTaskAction) (string, error) { return "", failure },
		)
		_, err := service.Control(
			t.Context(),
			auth.RequestMetadata{},
			"task",
			taskAPIAction("recheck"),
		)
		assertTaskAPIStatus(t, err, http.StatusConflict)
		assert.Contains(t, err.Error(), "source or summary changed")
		assert.Zero(t, store.controlled)
	}
}
