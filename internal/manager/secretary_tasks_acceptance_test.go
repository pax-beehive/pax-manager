package manager

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func TestSecretaryTaskControlGivenActualStoredUnknownThenChecksAndCommitsOriginalWithoutAgentTunnel(
	t *testing.T,
) {
	srv, _ := testServer(t, "task-acceptance@example.test")
	fixture := testNodeAgent(t, srv, "task-acceptance@example.test")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	createConversationTestSession(t, srv, fixture, "task-source-session", "task-source-native")
	message := domain.Message{
		MessageID: "task-source", AgentID: fixture.agentID, SessionID: "task-source-session",
		Role: "user", Status: "complete", MessageType: "user_message",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionUserToAgent,
		SessionSeq: 1,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: message.MessageID, PartType: domain.MessagePartText, Text: "Hello.",
	}))
	_, err := srv.secretaryStore.UpdateSecretarySettings(t.Context(), principal.User.UserID,
		domain.SecretarySettingsUpdate{AgentID: fixture.agentID, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, srv.secretaryStore.BackfillSecretaryJobs(t.Context()))
	jobs, err := srv.secretaryStore.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	executions := srv.secretaryStore.(domain.SecretaryExecutionStore)
	job, err := executions.PrepareSecretaryBatch(t.Context(), jobs[0])
	require.NoError(t, err)
	attempt, err := executions.BeginSecretaryAttempt(t.Context(), job)
	require.NoError(t, err)
	job.AttemptID, job.RunID, job.Attempt = attempt.ID, attempt.ID, &attempt
	executionID := secretary.ExecutionSessionID(job.OwnerUserID, attempt.ID)
	require.NoError(
		t,
		srv.secretaryStore.RegisterSecretaryInternalSession(
			t.Context(),
			job,
			fixture.agentID,
			executionID,
		),
	)
	failure := errors.Join(
		domain.ErrSecretaryExecutionUnknown,
		errors.New(
			"Internal error: The model connection failed. This request did not complete. You can retry.",
		),
	)
	require.NoError(t, executions.FailSecretaryAttempt(t.Context(), job, attempt.ID, failure))
	require.NoError(t, srv.secretaryStore.FailSecretaryJob(t.Context(), job, failure))
	tasks := srv.secretaryStore.(domain.SecretaryTaskStore)
	detail, err := tasks.GetSecretaryTask(
		t.Context(),
		job.OwnerUserID,
		job.SourceID,
		domain.SecretaryTaskQuery{},
	)
	require.NoError(t, err)
	require.Equal(t, "model_connection_failed", detail.Task.ErrorCode)

	check := func(task domain.SecretaryTask) userapi.SecretaryTaskActionResponse {
		t.Helper()
		raw, marshalErr := json.Marshal(domain.SecretaryTaskAction{
			Action: "recheck", ExpectedRevision: task.Revision, BatchID: task.BatchID,
			AttemptID: task.AttemptID, RetryEpoch: task.RetryEpoch, SettingsRevision: task.SettingsRevision,
		})
		require.NoError(t, marshalErr)
		response := taskHTTPRequest(
			t,
			srv,
			http.MethodPost,
			"/api/v1/user/self/secretary/tasks/"+task.TaskID+"/actions",
			fixture.userEmail,
			string(raw),
		)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var result struct {
			Data userapi.SecretaryTaskActionResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		return result.Data
	}
	unknown := check(detail.Task)
	assert.Equal(t, "still_unknown", unknown.Outcome)
	assert.Equal(t, "model_connection_failed", unknown.Task.ErrorCode,
		"checking must not erase the original provider failure")
	assert.Equal(t, job.BatchID, unknown.Task.BatchID)
	assert.Equal(t, attempt.ID, unknown.Task.AttemptID)
	assert.Equal(t, job.RetryEpoch, unknown.Task.RetryEpoch)

	createConversationTestSession(t, srv, fixture, executionID, "original-secretary-native")
	turnID := secretary.ExecutionTurnID(job.OwnerUserID, attempt.ID)
	output := domain.Message{
		MessageID: "task-completed-output", AgentID: fixture.agentID, SessionID: executionID,
		Role: "assistant", Status: "received", MessageType: "agent_message_chunk",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
		TurnID: turnID, SessionSeq: 1,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &output))
	require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: output.MessageID, PartType: domain.MessagePartText,
		Text: `{"summaries":[],"outcomes":[{"message_id":"task-source","action":"no_summary","reason":"Greeting only"}]}`,
	}))
	done := domain.Message{
		MessageID: "task-completed-terminal", AgentID: fixture.agentID, SessionID: executionID,
		Role: "assistant", Status: "complete", MessageType: "turn_done",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
		TurnID: turnID, SessionSeq: 2,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &done))
	completed := check(unknown.Task)
	assert.Equal(t, "recovered", completed.Outcome)
	assert.Equal(t, "completed", completed.Task.ExecutionState)
	assert.False(t, completed.Task.LeaseActive)
	page, err := tasks.ListSecretaryTasks(t.Context(), job.OwnerUserID, domain.SecretaryTaskQuery{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	detail, err = tasks.GetSecretaryTask(
		t.Context(),
		job.OwnerUserID,
		job.SourceID,
		domain.SecretaryTaskQuery{},
	)
	require.NoError(t, err)
	require.Len(t, detail.Attempts, 1)
	assert.Equal(t, attempt.ID, detail.Attempts[0].AttemptID)
	assert.True(t, detail.Attempts[0].HasOutput)
	messages, err := srv.store.ListMessages(t.Context(), fixture.agentID, executionID, 100)
	require.NoError(t, err)
	assert.Len(t, messages, 2, "checking must not create a prompt or another native execution")
}
