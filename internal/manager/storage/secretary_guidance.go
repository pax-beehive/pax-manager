package storage

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryInstructionsHash(snapshot *domain.SecretaryInstructionSnapshot) string {
	if snapshot == nil {
		return ""
	}
	raw, _ := json.Marshal(snapshot)
	return domain.SearchContentHash(string(raw))
}

func secretaryGuidanceKey(owner, scope, source string) string {
	return owner + "\x00" + scope + "\x00" + source
}

func secretaryDefaultGuidance(owner, scope, source string) domain.SecretaryGuidance {
	return domain.SecretaryGuidance{OwnerUserID: owner, Scope: scope, SourceID: source}
}

func (s *MemoryStore) secretaryGuidanceSourceLocked(owner, source string) bool {
	for _, session := range s.sessions {
		if strconv.FormatInt(session.ID, 10) != source || session.ArchivedAt != nil ||
			session.Source == domain.SecretaryInternalSource ||
			strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) ||
			s.secretaryLocked().internal[sessionKey(session.AgentID, session.SessionID)] {
			continue
		}
		agent, ok := s.agents[session.AgentID]
		return ok && agent.OwnerUserID == owner &&
			s.searchNodesVisibleLocked(agent.NodeID, session.NodeID)
	}
	return false
}

func (s *MemoryStore) secretaryGuidanceLocked(
	owner, scope, source string,
) domain.SecretaryGuidance {
	if result, ok := s.secretaryLocked().guidance[secretaryGuidanceKey(owner, scope, source)]; ok {
		return result
	}
	return secretaryDefaultGuidance(owner, scope, source)
}

func (s *MemoryStore) secretaryInstructionsLocked(
	owner, source string,
) *domain.SecretaryInstructionSnapshot {
	return &domain.SecretaryInstructionSnapshot{
		User:    s.secretaryGuidanceLocked(owner, domain.SecretaryGuidanceUser, ""),
		Session: s.secretaryGuidanceLocked(owner, domain.SecretaryGuidanceSession, source),
	}
}
