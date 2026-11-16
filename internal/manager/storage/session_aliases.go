package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var (
	_ domain.SessionAliasStore = (*MemoryStore)(nil)
	_ domain.SessionAliasStore = (*PostgresStore)(nil)
)

func (s *MemoryStore) ResolveSessionAliases(
	ctx context.Context,
	userID, agentID, sessionID string,
) (domain.SessionAliases, error) {
	if err := ctx.Err(); err != nil {
		return domain.SessionAliases{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentID]
	if !ok || userID == "" || sessionID == "" ||
		!s.canAccessAgentLocked(UserPrincipal{User: User{UserID: userID}}, agent) {
		return domain.SessionAliases{}, ErrNotFound
	}
	if session, exists := s.sessions[sessionKey(agentID, sessionID)]; exists {
		return domain.SessionAliases{SessionID: session.SessionID, NativeID: session.NativeID}, nil
	}
	var found AgentSession
	for _, session := range s.sessions {
		if session.AgentID == agentID && session.NativeID == sessionID &&
			(found.SessionID == "" || session.UpdatedAt.After(found.UpdatedAt) ||
				session.UpdatedAt.Equal(found.UpdatedAt) && session.SessionID < found.SessionID) {
			found = session
		}
	}
	if found.SessionID == "" {
		return domain.SessionAliases{}, ErrNotFound
	}
	return domain.SessionAliases{SessionID: found.SessionID, NativeID: found.NativeID}, nil
}

func (s *PostgresStore) ResolveSessionAliases(
	ctx context.Context,
	userID, agentID, sessionID string,
) (domain.SessionAliases, error) {
	if userID == "" || agentID == "" || sessionID == "" {
		return domain.SessionAliases{}, ErrNotFound
	}
	var aliases domain.SessionAliases
	err := s.db.QueryRowContext(ctx, `
		SELECT s.session_id, COALESCE(s.native_id, '')
		FROM agent_sessions s
		JOIN agents a ON a.agent_id = s.agent_id
		WHERE s.agent_id = $1 AND a.deleted_at IS NULL
			AND (a.owner_user_id = $2 OR `+teamAgentAccessSQL("a.agent_id", "$2")+`)
			AND s.session_id IN ($3, COALESCE(
				(SELECT b.session_id FROM agent_native_session_bindings b
				 WHERE b.agent_id = $1 AND b.native_session_id = $3),
				(SELECT n.session_id FROM agent_sessions n
				 WHERE n.agent_id = $1 AND n.native_id = $3
				 ORDER BY n.updated_at DESC, n.session_id LIMIT 1)
			))
		ORDER BY (s.session_id = $3) DESC
		LIMIT 1
	`, agentID, userID, sessionID).Scan(&aliases.SessionID, &aliases.NativeID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionAliases{}, ErrNotFound
	}
	return aliases, err
}
