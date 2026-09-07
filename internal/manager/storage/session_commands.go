package storage

import (
	"context"
	"encoding/json"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) UpdateSessionACPCommands(
	ctx context.Context,
	agentID, sessionID string,
	commands domain.SessionACPCommands,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(agentID, sessionID)
	session, ok := s.sessions[key]
	if !ok {
		return ErrNotFound
	}
	object := map[string]json.RawMessage{}
	if len(session.Metadata) > 0 {
		if err := json.Unmarshal(session.Metadata, &object); err != nil {
			return err
		}
	}
	if object == nil {
		object = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(commands)
	if err != nil {
		return err
	}
	object["acp_commands"] = raw
	session.Metadata, err = json.Marshal(object)
	if err != nil {
		return err
	}
	session.UpdatedAt = s.now().UTC()
	s.sessions[key] = session
	return nil
}

func (s *PostgresStore) UpdateSessionACPCommands(
	ctx context.Context,
	agentID, sessionID string,
	commands domain.SessionACPCommands,
) error {
	raw, err := json.Marshal(commands)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_sessions
		SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{acp_commands}', $3::jsonb, true),
			updated_at = $4
		WHERE agent_id = $1 AND session_id = $2
	`, agentID, sessionID, raw, s.now().UTC())
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return ErrNotFound
	}
	return nil
}
