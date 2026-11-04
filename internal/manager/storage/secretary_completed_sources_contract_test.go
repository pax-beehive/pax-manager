package storage

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryCompletionGivenUnrelatedOrNonterminalRowsThenDoesNotTreatThemAsCompletion(
	t *testing.T,
) {
	source := domain.Message{AgentID: "agent", SessionID: "session", TurnID: "turn", SessionSeq: 5}
	terminal := domain.Message{
		AgentID:     "agent",
		SessionID:   "session",
		TurnID:      "turn",
		SessionSeq:  6,
		MessageType: "turn_done",
		Status:      "complete",
	}
	cases := []struct {
		name   string
		change func(*domain.Message)
	}{
		{"another agent", func(m *domain.Message) { m.AgentID = "other" }},
		{"another session", func(m *domain.Message) { m.SessionID = "other" }},
		{"earlier sequence", func(m *domain.Message) { m.SessionSeq = 4 }},
		{"another turn", func(m *domain.Message) { m.TurnID = "other" }},
		{"incomplete terminal", func(m *domain.Message) { m.Status = "running" }},
		{"ordinary row", func(m *domain.Message) { m.MessageType = "agent_message_chunk" }},
		{"thought row", func(m *domain.Message) { m.MessageType = "agent_thought_chunk" }},
		{"oversized native response", func(m *domain.Message) {
			m.MessageType = "end_turn"
			m.RawJSON = json.RawMessage(strings.Repeat("x", 64*1024+1))
		}},
		{"native refusal", func(m *domain.Message) {
			m.MessageType = "end_turn"
			m.RawJSON = json.RawMessage(`{"result":{"stopReason":"refusal"}}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := terminal
			tc.change(&row)
			assert.False(t, secretarySourceComplete(source, row))
		})
	}
	assert.True(t, secretarySourceComplete(source, terminal))
}

func TestSecretaryCompletionPostgresGivenNoMessagesOrMissingSourceThenReturnsExplicitCompletion(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	complete, err := secretarySourcesCompleteTx(t.Context(), tx, domain.SecretaryJob{})
	require.NoError(t, err)
	assert.True(t, complete)
	job.Messages[0].SourceID = "missing"
	complete, err = secretarySourcesCompleteTx(t.Context(), tx, job)
	require.NoError(t, err)
	assert.False(t, complete)
	require.NoError(t, tx.Rollback())
	_, err = secretarySourcesCompleteTx(t.Context(), tx, job)
	assert.Error(t, err)
}

func TestSecretaryCompletionMemoryGivenMissingMessageThenNeverAssumesComplete(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	delete(store.messages, "m1")
	assert.False(t, store.secretarySourcesCompleteLocked(job))
}
