package storage

import (
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const userSessionListVisibleSQL = `
	COALESCE(agent_sessions.source, '') <> 'secretary_internal'
	AND NOT starts_with(agent_sessions.session_id, 'sess_secretary_')
	AND NOT EXISTS (
		SELECT 1 FROM secretary_internal_sessions i
		WHERE i.agent_id = agent_sessions.agent_id
			AND i.session_id = agent_sessions.session_id
	)`

func (s *MemoryStore) userSessionListVisibleLocked(session AgentSession) bool {
	if session.Source == domain.SecretaryInternalSource ||
		strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) {
		return false
	}
	return s.secretaryState == nil ||
		!s.secretaryState.internal[sessionKey(session.AgentID, session.SessionID)]
}
