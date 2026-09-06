package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryUpdateSessionACPConfig(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	store.sessions[sessionKey("agent_1", "sess_1")] = AgentSession{
		AgentID:   "agent_1",
		SessionID: "sess_1",
		Model:     "old-model",
	}
	config := domain.SessionACPConfig{
		Source:     domain.SessionConfigSourceUpdate,
		ObservedAt: now,
		Options: []domain.SessionConfigOption{{
			ID:           "model",
			Category:     "model",
			Type:         "select",
			CurrentValue: "gpt-6",
		}},
	}

	require.NoError(
		t,
		store.UpdateSessionACPConfig(context.Background(), "agent_1", "sess_1", config),
	)
	session := store.sessions[sessionKey("agent_1", "sess_1")]
	assert.Equal(t, "gpt-6", session.Model)
	assert.Equal(t, config, session.ACPConfig)
	assert.JSONEq(
		t,
		`{"acp_config":{"options":[{"id":"model","name":"","category":"model","type":"select","current_value":"gpt-6"}],"source":"config_option_update","observed_at":"2026-09-05T12:00:00Z"}}`,
		string(session.Metadata),
	)

	restored := sessionACPConfigFromMetadata(session.Metadata)
	assert.Equal(t, config, restored)
}

func TestMemorySessionReportPreservesObservedModelWhenReportOmitsModel(t *testing.T) {
	store := NewMemoryStore(time.Now)
	key := sessionKey("agent_1", "sess_1")
	store.sessions[key] = AgentSession{
		AgentID:   "agent_1",
		SessionID: "sess_1",
		Model:     "gpt-6",
	}

	updated := store.upsertSessionLocked("node_1", "agent_1", domain.SessionStatusInput{
		SessionID: "sess_1",
		Status:    "idle",
	}, "", time.Now())
	assert.Equal(t, "gpt-6", updated.Model)
}
