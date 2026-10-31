package storage

import (
	"context"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryProgressStore = (*MemoryStore)(nil)

func (s *MemoryStore) ReadSecretaryProgress(
	ctx context.Context,
	owner string,
	query domain.SecretaryProgressQuery,
) (domain.SecretaryProgressPage, error) {
	after, limit, err := secretaryProgressQuery(owner, query)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.secretaryLocked()
	progress := secretaryProgressAccumulator{}
	for _, session := range s.sessions {
		if err = ctx.Err(); err != nil {
			return domain.SecretaryProgressPage{}, err
		}
		if session.ArchivedAt != nil || session.Source == domain.SecretaryInternalSource ||
			strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) ||
			state.internal[sessionKey(session.AgentID, session.SessionID)] ||
			!s.secretaryAgentOwnedLocked(owner, session.AgentID) ||
			!s.searchNodesVisibleLocked(session.NodeID) {
			continue
		}
		id := secretarySourceID(session.ID)
		if session.ID == 0 {
			id = "memory:" + domain.SearchContentHash(
				sessionKey(session.AgentID, session.SessionID),
			)
		}
		title := session.SessionName
		if session.NameIsCustom || session.CustomSessionName != "" {
			title = session.CustomSessionName
		}
		row := domain.SecretarySessionProgress{
			SourceID:  id,
			AgentID:   session.AgentID,
			SessionID: session.SessionID,
			Title:     title,
		}
		job := state.jobs[id]
		if job != nil && job.work.OwnerUserID == owner {
			row.LastCompletedAt = job.completed
		}
		for _, message := range s.messages {
			if message.AgentID != session.AgentID || message.SessionID != session.SessionID {
				continue
			}
			doc, readErr := s.readSearchMessageLocked(owner, message.MessageID)
			if readErr != nil {
				continue
			}
			completed := job != nil && job.work.OwnerUserID == owner &&
				job.checkpoint[message.MessageID] == doc.ContentHash
			progress.add(row, message.TurnID, completed)
		}
	}
	return progress.page(owner, after, limit, s.now().UTC()), nil
}
