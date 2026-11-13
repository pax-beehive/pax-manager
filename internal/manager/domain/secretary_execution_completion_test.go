package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretaryExecutionCompletionGivenPreparedDispatchThenRequiresExactResponseAndBinding(
	t *testing.T,
) {
	job, request, completion := terminalDomainFixture(t)
	completion.MessageID = "raw-acp-response"
	completion.Role, completion.MessageType, completion.Status = "assistant", "end_turn", "received"
	completion.RawJSON = json.RawMessage(
		`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"end_turn"}}`,
	)
	assert.True(t, SecretaryExecutionCompletionMatches(request, completion, job))
	for name, mutate := range map[string]func(*SecretaryJob, *Message, *Message){
		"job_owner":      func(j *SecretaryJob, _, _ *Message) { j.OwnerUserID = "other" },
		"job_agent":      func(j *SecretaryJob, _, _ *Message) { j.AgentID = "other" },
		"job_source":     func(j *SecretaryJob, _, _ *Message) { j.SourceID = "other" },
		"job_batch":      func(j *SecretaryJob, _, _ *Message) { j.BatchID = "other" },
		"job_attempt":    func(j *SecretaryJob, _, _ *Message) { j.AttemptID = "other" },
		"job_settings":   func(j *SecretaryJob, _, _ *Message) { j.SettingsRevision++ },
		"job_generation": func(j *SecretaryJob, _, _ *Message) { j.RetryGeneration++ },
		"job_epoch":      func(j *SecretaryJob, _, _ *Message) { j.RetryEpoch++ },
		"request_id":     func(_ *SecretaryJob, r, _ *Message) { r.MessageID = "forged" },
		"request_session": func(_ *SecretaryJob, r, _ *Message) {
			r.SessionID = "other"
		},
		"request_turn":      func(_ *SecretaryJob, r, _ *Message) { r.TurnID = "other" },
		"request_type":      func(_ *SecretaryJob, r, _ *Message) { r.MessageType = "user_message" },
		"request_status":    func(_ *SecretaryJob, r, _ *Message) { r.Status = "sent" },
		"request_role":      func(_ *SecretaryJob, r, _ *Message) { r.Role = "assistant" },
		"request_source":    func(_ *SecretaryJob, r, _ *Message) { r.Source = "local" },
		"request_direction": func(_ *SecretaryJob, r, _ *Message) { r.Direction = MessageDirectionAgentToUser },
		"completion_id":     func(_ *SecretaryJob, _, c *Message) { c.MessageID = "" },
		"completion_owner":  func(_ *SecretaryJob, _, c *Message) { c.OwnerUserID = "other" },
		"completion_agent":  func(_ *SecretaryJob, _, c *Message) { c.AgentID = "other" },
		"completion_session": func(_ *SecretaryJob, _, c *Message) {
			c.SessionID = "other"
		},
		"completion_turn":      func(_ *SecretaryJob, _, c *Message) { c.TurnID = "other" },
		"completion_type":      func(_ *SecretaryJob, _, c *Message) { c.MessageType = "agent_message_chunk" },
		"completion_status":    func(_ *SecretaryJob, _, c *Message) { c.Status = "complete" },
		"completion_role":      func(_ *SecretaryJob, _, c *Message) { c.Role = "user" },
		"completion_source":    func(_ *SecretaryJob, _, c *Message) { c.Source = "local" },
		"completion_direction": func(_ *SecretaryJob, _, c *Message) { c.Direction = MessageDirectionUserToAgent },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, prepared, response := job, request, completion
			mutate(&candidate, &prepared, &response)
			assert.False(t, SecretaryExecutionCompletionMatches(prepared, response, candidate))
		})
	}
	for name, raw := range map[string]string{
		"malformed": `{`, "wrong_rpc_version": `{"jsonrpc":"1.0","id":41,"result":{"stopReason":"end_turn"}}`,
		"wrong_id":        `{"jsonrpc":"2.0","id":42,"result":{"stopReason":"end_turn"}}`,
		"string_id_alias": `{"jsonrpc":"2.0","id":"41","result":{"stopReason":"end_turn"}}`,
		"missing_id":      `{"jsonrpc":"2.0","result":{"stopReason":"end_turn"}}`,
		"method":          `{"jsonrpc":"2.0","id":41,"method":"session/update","result":{"stopReason":"end_turn"}}`,
		"error":           `{"jsonrpc":"2.0","id":41,"error":{"code":-1},"result":{"stopReason":"end_turn"}}`,
		"cancelled":       `{"jsonrpc":"2.0","id":41,"result":{"stopReason":"cancelled"}}`,
		"missing_result":  `{"jsonrpc":"2.0","id":41}`,
		"oversized":       strings.Repeat(" ", 64*1024) + string(completion.RawJSON),
	} {
		t.Run(name, func(t *testing.T) {
			response := completion
			response.RawJSON = json.RawMessage(raw)
			assert.False(t, SecretaryExecutionCompletionMatches(request, response, job))
		})
	}
	for name, mutate := range map[string]func(*SecretaryExecutionCorrelation){
		"version":         func(c *SecretaryExecutionCorrelation) { c.Version++ },
		"method":          func(c *SecretaryExecutionCorrelation) { c.Method = "session/update" },
		"failure":         func(c *SecretaryExecutionCorrelation) { c.Failure = "cancelled" },
		"origin":          func(c *SecretaryExecutionCorrelation) { c.Origin = "untrusted" },
		"missing_binding": func(c *SecretaryExecutionCorrelation) { c.Binding = nil },
		"null_id":         func(c *SecretaryExecutionCorrelation) { c.RequestID = json.RawMessage(`null`) },
	} {
		t.Run(name, func(t *testing.T) {
			var correlation SecretaryExecutionCorrelation
			require.NoError(t, json.Unmarshal(request.RawJSON, &correlation))
			mutate(&correlation)
			raw, err := json.Marshal(correlation)
			require.NoError(t, err)
			prepared := request
			prepared.RawJSON = raw
			assert.False(t, SecretaryExecutionCompletionMatches(prepared, completion, job))
		})
	}
	prepared := request
	prepared.RawJSON = json.RawMessage(`{`)
	assert.False(t, SecretaryExecutionCompletionMatches(prepared, completion, job))
}
