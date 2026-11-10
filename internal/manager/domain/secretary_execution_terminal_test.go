package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func terminalDomainFixture(t *testing.T) (SecretaryJob, Message, Message) {
	t.Helper()
	job := SecretaryJob{
		OwnerUserID:      "owner",
		AgentID:          "agent",
		SourceID:         "source",
		BatchID:          "batch",
		AttemptID:        "attempt",
		RunID:            "attempt",
		SettingsRevision: 2,
		RetryGeneration:  3,
		RetryEpoch:       4,
	}
	sum := sha256.Sum256([]byte(job.OwnerUserID + "\x00" + job.AttemptID))
	suffix := hex.EncodeToString(sum[:16])
	correlation := SecretaryExecutionCorrelation{
		Version:   1,
		Method:    "session/prompt",
		RequestID: json.RawMessage("41"),
		Binding: &SecretaryExecutionBinding{
			SourceID:         job.SourceID,
			BatchID:          job.BatchID,
			AttemptID:        job.AttemptID,
			SettingsRevision: job.SettingsRevision,
			RetryGeneration:  job.RetryGeneration,
			RetryEpoch:       job.RetryEpoch,
		},
	}
	raw, err := json.Marshal(correlation)
	require.NoError(t, err)
	request := Message{
		MessageID:   SecretaryExecutionRequestMessageID(job),
		OwnerUserID: job.OwnerUserID,
		AgentID:     job.AgentID,
		SessionID:   SecretaryInternalSessionPrefix + suffix,
		TurnID:      "turn_secretary_" + suffix,
		Source:      MessageSourceACPTunnel,
		Direction:   MessageDirectionUserToAgent,
		Role:        "system",
		Status:      "prepared",
		MessageType: "secretary_execution_request",
		RawJSON:     raw,
	}
	correlation.Origin, correlation.Failure = "reliable_inbox", "cancelled"
	raw, err = json.Marshal(correlation)
	require.NoError(t, err)
	terminal := request
	terminal.MessageID = SecretaryExecutionTerminalMessageID(request)
	terminal.Direction, terminal.MessageType, terminal.Status, terminal.RawJSON = MessageDirectionAgentToUser, "secretary_execution_failed", "failed", raw
	return job, request, terminal
}

func TestSecretaryExecutionTerminalGivenExactDurableRequestThenMatchesOnlyItsCurrentBinding(
	t *testing.T,
) {
	job, request, terminal := terminalDomainFixture(t)
	assert.True(t, SecretaryExecutionTerminalMatches(request, terminal, job))
	for name, mutate := range map[string]func(*SecretaryJob, *Message, *Message){
		"owner":          func(job *SecretaryJob, _, _ *Message) { job.OwnerUserID = "other" },
		"agent":          func(job *SecretaryJob, _, _ *Message) { job.AgentID = "other" },
		"source":         func(job *SecretaryJob, _, _ *Message) { job.SourceID = "other" },
		"batch":          func(job *SecretaryJob, _, _ *Message) { job.BatchID = "other" },
		"attempt":        func(job *SecretaryJob, _, _ *Message) { job.AttemptID = "other" },
		"settings":       func(job *SecretaryJob, _, _ *Message) { job.SettingsRevision++ },
		"generation":     func(job *SecretaryJob, _, _ *Message) { job.RetryGeneration++ },
		"epoch":          func(job *SecretaryJob, _, _ *Message) { job.RetryEpoch++ },
		"session":        func(_ *SecretaryJob, request, _ *Message) { request.SessionID = "wrong" },
		"turn":           func(_ *SecretaryJob, request, _ *Message) { request.TurnID = "wrong" },
		"terminal owner": func(_ *SecretaryJob, _, terminal *Message) { terminal.OwnerUserID = "other" },
		"terminal request": func(_ *SecretaryJob, _, terminal *Message) {
			terminal.RawJSON = json.RawMessage(`{"version":1,"method":"session/prompt","request_id":42,"failure":"cancelled","origin":"reliable_inbox"}`)
		},
		"missing seal": func(_ *SecretaryJob, _, terminal *Message) {
			terminal.RawJSON = json.RawMessage(`{"version":1,"method":"session/prompt","request_id":41,"failure":"cancelled"}`)
		},
		"raw invalid": func(_ *SecretaryJob, _, terminal *Message) { terminal.RawJSON = json.RawMessage(`{`) },
		"tool method": func(_ *SecretaryJob, request, _ *Message) {
			request.RawJSON = json.RawMessage(`{"version":1,"method":"session/request_permission","request_id":41}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, prepared, failed := job, request, terminal
			mutate(&candidate, &prepared, &failed)
			assert.False(t, SecretaryExecutionTerminalMatches(prepared, failed, candidate))
		})
	}
}

func TestSecretaryExecutionTerminalIDsGivenMalformedIdentityThenFailClosed(t *testing.T) {
	assert.Empty(t, SecretaryExecutionRequestMessageID(SecretaryJob{}))
	for _, raw := range []string{`{`, `{"version":2,"method":"session/prompt","request_id":41}`, `{"version":1,"method":"session/prompt","request_id":null}`, `{"version":1,"method":"session/prompt","request_id":0}`, `{"version":1,"method":"session/prompt","request_id":{}}`} {
		assert.Empty(t, SecretaryExecutionTerminalMessageID(Message{RawJSON: json.RawMessage(raw)}))
	}
	for _, raw := range []string{`1`, `"request"`} {
		assert.True(t, validSecretaryExecutionRequestID(json.RawMessage(raw)))
	}
	for _, raw := range []string{``, `null`, `-1`, `""`, `{}`, `1.5`} {
		assert.False(t, validSecretaryExecutionRequestID(json.RawMessage(raw)))
	}
}
