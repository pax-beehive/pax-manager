package storage

import (
	"context"
	"encoding/json"
	"strings"
)

func sessionACPConfigFromMetadata(metadata []byte) SessionACPConfig {
	if len(metadata) == 0 || !json.Valid(metadata) {
		return SessionACPConfig{}
	}
	var object struct {
		ACPConfig SessionACPConfig `json:"acp_config"`
	}
	if err := json.Unmarshal(metadata, &object); err != nil {
		return SessionACPConfig{}
	}
	return object.ACPConfig
}

func sessionACPConfigMetadata(raw json.RawMessage, config SessionACPConfig) json.RawMessage {
	object := map[string]any{}
	if len(raw) > 0 && json.Valid(raw) {
		_ = json.Unmarshal(raw, &object)
	}
	object["acp_config"] = config
	data, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return data
}

func sessionACPConfigModel(config SessionACPConfig) string {
	for _, option := range config.Options {
		if !strings.EqualFold(strings.TrimSpace(option.Category), "model") &&
			!strings.EqualFold(strings.TrimSpace(option.ID), "model") &&
			!strings.EqualFold(strings.TrimSpace(option.ID), "models") {
			continue
		}
		if value, ok := option.CurrentValue.(string); ok {
			return strings.TrimSpace(value)
		}
	}
	if config.LegacyModels != nil {
		return strings.TrimSpace(config.LegacyModels.CurrentModelID)
	}
	return ""
}

func (s *MemoryStore) UpdateSessionACPConfig(
	ctx context.Context,
	agentID string,
	sessionID string,
	config SessionACPConfig,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(agentID, sessionID)
	session, ok := s.sessions[key]
	if !ok {
		return ErrNotFound
	}
	session.ACPConfig = config
	session.Metadata = sessionACPConfigMetadata(session.Metadata, config)
	if model := sessionACPConfigModel(config); model != "" {
		session.Model = model
	}
	session.UpdatedAt = s.now().UTC()
	s.sessions[key] = session
	return nil
}

func (s *PostgresStore) UpdateSessionACPConfig(
	ctx context.Context,
	agentID string,
	sessionID string,
	config SessionACPConfig,
) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}
	model := sessionACPConfigModel(config)
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_sessions
		SET metadata = jsonb_set(
				COALESCE(metadata, '{}'::jsonb),
				'{acp_config}',
				$3::jsonb,
				true
			),
			model = COALESCE(NULLIF($4, ''), model),
			updated_at = $5
		WHERE agent_id = $1 AND session_id = $2
	`, agentID, sessionID, configJSON, model, s.now().UTC())
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return ErrNotFound
	}
	return nil
}
