package manager

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryCompletionGivenDurableACPResponseWithoutConversationMarkerThenRecoversOriginalOutput(
	t *testing.T,
) {
	f := newSecretaryTerminalFixture(t)
	output := domain.Message{
		MessageID: "native-output", OwnerUserID: f.job.OwnerUserID,
		AgentID: f.job.AgentID, SessionID: f.sessionID, TurnID: f.turnID,
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
		Role: "assistant", MessageType: "agent_message_chunk", Status: "received",
	}
	require.NoError(t, f.service.store.UpsertMessage(t.Context(), &output))
	require.NoError(t, f.service.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: output.MessageID, PartType: domain.MessagePartText, Text: "Original completed summary",
	}))
	f.frame.Payload = json.RawMessage(
		`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"end_turn"}}`,
	)
	require.NoError(t, f.agent.dispatchReliableACPFrame(
		t.Context(), f.service.agentACPFramePipeline(), f.frame,
	))
	late := output
	late.MessageID = "late-notification"
	late.ID, late.SessionSeq, late.ConversationSeq = 0, 0, 0
	require.NoError(t, f.service.store.UpsertMessage(t.Context(), &late))
	require.NoError(t, f.service.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: late.MessageID, PartType: domain.MessagePartText, Text: "Unrelated late notification",
	}))
	text, started, completed, err := f.service.readSecretaryExecution(
		t.Context(), f.job.AgentID, f.sessionID, f.turnID,
	)
	require.NoError(t, err)
	assert.True(t, started)
	assert.True(t, completed)
	assert.Equal(t, "Original completed summary", text)
	messages, err := f.service.store.ListMessages(t.Context(), f.job.AgentID, f.sessionID, 1000)
	require.NoError(t, err)
	for _, message := range messages {
		assert.NotEqual(t, "turn_done", message.MessageType)
	}
}

type secretaryCompletionHistoryStore struct {
	domain.Store
	request []domain.Message
	err     error
}

func (s secretaryCompletionHistoryStore) ListMessageHistoryPage(
	context.Context, string, string, int64, int,
) (domain.MessageHistoryPage, error) {
	return domain.MessageHistoryPage{Messages: s.request}, s.err
}

func TestSecretaryCompletionGivenUncorrelatedOrUnavailableHistoryThenRemainsUnknown(
	t *testing.T,
) {
	job := domain.SecretaryJob{
		OwnerUserID: "owner", AgentID: "agent", SourceID: "source", BatchID: "batch",
		AttemptID: "attempt", SettingsRevision: 1, RetryEpoch: 2, RetryGeneration: 3,
	}
	sessionID, turnID := secretary.ExecutionSessionID(
		"owner",
		"attempt",
	), secretary.ExecutionTurnID(
		"owner",
		"attempt",
	)
	correlation := secretaryExecutionCorrelation{
		Version: 1, Method: "session/prompt", RequestID: json.RawMessage(`41`),
		Binding: &secretaryExecutionBinding{
			SourceID: job.SourceID, BatchID: job.BatchID, AttemptID: job.AttemptID,
			SettingsRevision: job.SettingsRevision, RetryEpoch: job.RetryEpoch,
			RetryGeneration: job.RetryGeneration,
		},
	}
	raw, err := json.Marshal(correlation)
	require.NoError(t, err)
	request := domain.Message{
		MessageID: domain.SecretaryExecutionRequestMessageID(job), OwnerUserID: "owner",
		AgentID: "agent", SessionID: sessionID, TurnID: turnID,
		Role: "system", Status: "prepared", MessageType: "secretary_execution_request",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionUserToAgent,
		RawJSON: raw,
	}
	completion := request
	completion.MessageID, completion.Role, completion.Status, completion.MessageType = "native-completion", "assistant", "received", "end_turn"
	completion.Direction = domain.MessageDirectionAgentToUser
	completion.RawJSON = json.RawMessage(
		`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"end_turn"}}`,
	)
	storageFailure := errors.New("history unavailable")
	for _, scenario := range []struct {
		name         string
		mutate       func(*domain.Message, *domain.Message)
		missing      bool
		historyError error
		wantID       string
	}{
		{name: "exact_response", wantID: completion.MessageID},
		{name: "missing_request", missing: true},
		{name: "history_failure", historyError: storageFailure},
		{name: "wrong_agent", mutate: func(_, c *domain.Message) { c.AgentID = "other" }},
		{name: "wrong_session", mutate: func(_, c *domain.Message) { c.SessionID = "other" }},
		{name: "wrong_turn", mutate: func(_, c *domain.Message) { c.TurnID = "other" }},
		{name: "wrong_type", mutate: func(_, c *domain.Message) { c.MessageType = "turn_done" }},
		{name: "wrong_owner", mutate: func(_, c *domain.Message) { c.OwnerUserID = "other" }},
		{name: "wrong_request_id", mutate: func(_, c *domain.Message) {
			c.RawJSON = json.RawMessage(`{"jsonrpc":"2.0","id":42,"result":{"stopReason":"end_turn"}}`)
		}},
		{name: "unbound_request", mutate: func(r, _ *domain.Message) {
			r.RawJSON = json.RawMessage(`{"version":1,"method":"session/prompt","request_id":41}`)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			prepared, response := request, completion
			if scenario.mutate != nil {
				scenario.mutate(&prepared, &response)
			}
			store := secretaryCompletionHistoryStore{
				request: []domain.Message{prepared},
				err:     scenario.historyError,
			}
			if scenario.missing {
				store.request = nil
			}
			id, err := secretaryKnownExecutionCompletion(
				t.Context(),
				store,
				[]domain.Message{response},
				"agent",
				sessionID,
				turnID,
			)
			require.ErrorIs(t, err, scenario.historyError)
			assert.Equal(t, scenario.wantID, id)
		})
	}
	id, err := secretaryKnownExecutionCompletion(
		t.Context(),
		nil,
		[]domain.Message{completion},
		"agent",
		"ordinary",
		turnID,
	)
	require.NoError(t, err)
	assert.Empty(t, id)
}
