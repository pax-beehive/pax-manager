package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretaryTaskHTTPStore struct {
	domain.SecretaryTaskStore
	owner   string
	task    domain.SecretaryTask
	actions int
	checked int
}

func (f *secretaryTaskHTTPStore) ListSecretaryTasks(
	_ context.Context,
	owner string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskPage, error) {
	if query.Cursor == "invalid" {
		return domain.SecretaryTaskPage{}, domain.ErrInvalidCursor
	}
	if owner != f.owner {
		return domain.SecretaryTaskPage{}, nil
	}
	return domain.SecretaryTaskPage{Items: []domain.SecretaryTask{f.task}}, nil
}

func (f *secretaryTaskHTTPStore) GetSecretaryTask(
	_ context.Context,
	owner, id string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskDetail, error) {
	if owner != f.owner || id != f.task.TaskID {
		return domain.SecretaryTaskDetail{}, domain.ErrNotFound
	}
	if query.Cursor == "invalid" {
		return domain.SecretaryTaskDetail{}, domain.ErrInvalidCursor
	}
	return domain.SecretaryTaskDetail{Task: f.task}, nil
}

func (f *secretaryTaskHTTPStore) ControlSecretaryTask(
	_ context.Context,
	owner, id string,
	action domain.SecretaryTaskAction,
) (domain.SecretaryTask, error) {
	if owner != f.owner || id != f.task.TaskID {
		return domain.SecretaryTask{}, domain.ErrNotFound
	}
	if action.ExpectedRevision != f.task.Revision || action.BatchID != f.task.BatchID ||
		action.AttemptID != f.task.AttemptID ||
		action.SettingsRevision != f.task.SettingsRevision ||
		action.RetryEpoch != f.task.RetryEpoch {
		return domain.SecretaryTask{}, domain.ErrConflict
	}
	f.actions++
	return f.task, nil
}

func secretaryTaskHTTPFixture(t *testing.T) (*Server, *secretaryTaskHTTPStore) {
	t.Helper()
	server, _ := testServer(t, "task-owner@example.test")
	owner := testUserPrincipal(t, server, "task-owner@example.test").User.UserID
	store := &secretaryTaskHTTPStore{
		owner: owner,
		task: domain.SecretaryTask{
			TaskID:           "source",
			Title:            "Visible task",
			Revision:         7,
			SettingsRevision: 2,
			RetryEpoch:       3,
			BatchID:          "batch",
			AttemptID:        "attempt",
			ExecutionState:   "unknown",
			ErrorCode:        "model_connection_failed",
			ErrorMessage:     "private raw provider error",
		},
	}
	server.secretaryTasks = userapi.NewSecretaryTaskService(
		server.auth,
		store,
		func(_ context.Context, gotOwner, id string, action domain.SecretaryTaskAction) (string, error) {
			_, err := store.ControlSecretaryTask(t.Context(), gotOwner, id, action)
			if err != nil {
				return "", err
			}
			store.checked++
			return "still_unknown", nil
		},
	)
	return server, store
}

func taskHTTPRequest(
	t *testing.T,
	server *Server,
	method, path, email, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-User-Email", email)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	return response
}

func TestSecretaryTaskHTTPGivenOwnerThenReadsSafeMetadataAndSelectedActions(t *testing.T) {
	server, store := secretaryTaskHTTPFixture(t)
	for _, path := range []string{"/api/v1/user/self/secretary/tasks?limit=100", "/api/v1/user/" + store.owner + "/secretary/tasks/source?limit=1"} {
		response := taskHTTPRequest(t, server, http.MethodGet, path, "task-owner@example.test", "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), "Visible task")
		assert.NotContains(t, response.Body.String(), "private raw provider error")
	}
	for _, action := range []string{"pause", "resume", "retry", "recheck"} {
		body := domain.SecretaryTaskAction{
			Action:           action,
			ExpectedRevision: 7,
			BatchID:          "batch",
			AttemptID:        "attempt",
			SettingsRevision: 2,
			RetryEpoch:       3,
			ConfirmRestart:   true,
		}
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		response := taskHTTPRequest(
			t,
			server,
			http.MethodPost,
			"/api/v1/user/self/secretary/tasks/source/actions",
			"task-owner@example.test",
			string(raw),
		)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), "private raw provider error")
	}
	assert.Equal(t, 4, store.actions)
	assert.Equal(t, 1, store.checked)
}

func TestSecretaryTaskHTTPGivenForeignOwnerThenHidesTaskAndCannotMutate(t *testing.T) {
	server, store := secretaryTaskHTTPFixture(t)
	for _, prefix := range []string{"/api/v1/user/self/secretary/tasks/source", "/api/v1/user/" + store.owner + "/secretary/tasks/source"} {
		response := taskHTTPRequest(t, server, http.MethodGet, prefix, "other@example.test", "")
		assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
		response = taskHTTPRequest(
			t,
			server,
			http.MethodPost,
			prefix+"/actions",
			"other@example.test",
			`{"action":"pause","expected_revision":7,"batch_id":"batch","attempt_id":"attempt","retry_epoch":3,"settings_revision":2}`,
		)
		assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), "Visible task")
	}
	assert.Zero(t, store.actions)
}

func TestSecretaryTaskHTTPGivenMalformedParametersThenRejectsBeforeMutation(t *testing.T) {
	server, store := secretaryTaskHTTPFixture(t)
	for _, suffix := range []string{"", "/source"} {
		for _, query := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=1.2", "?limit=", "?limit=99999999999999999999", "?limit=1&limit=2", "?cursor=a&cursor=b", "?owner_user_id=other", "?cursor=invalid"} {
			response := taskHTTPRequest(
				t,
				server,
				http.MethodGet,
				"/api/v1/user/self/secretary/tasks"+suffix+query,
				"task-owner@example.test",
				"",
			)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		}
	}
	valid := `{"action":"pause","expected_revision":7,"batch_id":"batch","attempt_id":"attempt","retry_epoch":3,"settings_revision":2}`
	for _, body := range []string{"", "null", "[]", "{", `{}`, valid + "{}", strings.Replace(valid, `"action":"pause"`, `"action":"pause","action":"retry"`, 1), strings.Replace(valid, `"action":"pause"`, `"owner_user_id":"other","action":"pause"`, 1), strings.Replace(valid, `"expected_revision":7`, `"expected_revision":null`, 1), strings.Replace(valid, `"expected_revision":7`, `"expected_revision":1.5`, 1), strings.Replace(valid, `"action":"pause"`, `"action":"other"`, 1), strings.Repeat("x", 8193), strings.Replace(valid, `"action":"pause"`, `"action":"pause","Action":"retry"`, 1)} {
		response := taskHTTPRequest(
			t,
			server,
			http.MethodPost,
			"/api/v1/user/self/secretary/tasks/source/actions",
			"task-owner@example.test",
			body,
		)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	response := taskHTTPRequest(
		t,
		server,
		http.MethodPost,
		"/api/v1/user/self/secretary/tasks/source/actions?limit=1",
		"task-owner@example.test",
		valid,
	)
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Zero(t, store.actions)
}

func TestSecretaryTaskHTTPGivenStaleControlOrUnconfirmedRestartThenReturnsConflict(t *testing.T) {
	server, store := secretaryTaskHTTPFixture(t)
	for _, body := range []string{
		`{"action":"pause","expected_revision":6,"batch_id":"batch","attempt_id":"attempt","retry_epoch":3,"settings_revision":2}`,
		`{"action":"retry","expected_revision":7,"batch_id":"batch","attempt_id":"attempt","retry_epoch":3,"settings_revision":2}`,
	} {
		response := taskHTTPRequest(
			t,
			server,
			http.MethodPost,
			"/api/v1/user/self/secretary/tasks/source/actions",
			"task-owner@example.test",
			body,
		)
		assert.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	}
	assert.Zero(t, store.actions)
}
