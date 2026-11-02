package storage

import (
	"context"
	"strconv"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) ReadSearchGroup(
	_ context.Context,
	owner string,
	doc domain.SearchDocument,
) (domain.SearchGroupMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" || doc.OwnerUserID != owner {
		return domain.SearchGroupMetadata{}, ErrNotFound
	}
	source := ""
	if doc.Kind == "summary" {
		versions := s.secretaryLocked().summaries[doc.SourceID]
		if doc.SummaryRevision <= 0 || doc.SummaryRevision > int64(len(versions)) {
			return domain.SearchGroupMetadata{}, ErrNotFound
		}
		summary := versions[doc.SummaryRevision-1]
		if summary.OwnerUserID != owner {
			return domain.SearchGroupMetadata{}, ErrNotFound
		}
		source = secretaryPrimarySource(summary)
		if source == "" {
			doc.AgentID, doc.SessionID = "", ""
			return domain.SearchGroupMetadata{
				Key:   domain.SearchDocumentGroupKey(doc),
				Title: searchGroupTitle(summary.Title, summary.SummaryID),
			}, nil
		}
	}
	if source == "" && (doc.AgentID == "" || doc.SessionID == "") {
		return domain.SearchGroupMetadata{
			Key:   domain.SearchDocumentGroupKey(doc),
			Title: searchGroupTitle(doc.Title, doc.SourceID),
		}, nil
	}
	return s.readSearchGroupSessionLocked(owner, source, doc)
}

func (s *MemoryStore) readSearchGroupSessionLocked(
	owner, source string,
	doc domain.SearchDocument,
) (domain.SearchGroupMetadata, error) {
	for _, session := range s.sessions {
		if source != "" && strconv.FormatInt(session.ID, 10) != source ||
			source == "" && (session.AgentID != doc.AgentID || session.SessionID != doc.SessionID) {
			continue
		}
		agent, ok := s.agents[session.AgentID]
		if !ok || agent.OwnerUserID != owner || session.ArchivedAt != nil ||
			session.Source == domain.SecretaryInternalSource || strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) ||
			!s.searchNodesVisibleLocked(agent.NodeID, session.NodeID) {
			return domain.SearchGroupMetadata{}, ErrNotFound
		}
		title := session.SessionName
		if session.NameIsCustom || session.CustomSessionName != "" {
			title = session.CustomSessionName
		}
		return domain.SearchGroupMetadata{
			Key: domain.SearchDocumentGroupKey(
				domain.SearchDocument{
					OwnerUserID: owner,
					AgentID:     session.AgentID,
					SessionID:   session.SessionID,
				},
			),
			Title: searchGroupTitle(
				title,
				session.SessionID,
			),
			AgentID:   session.AgentID,
			SessionID: session.SessionID,
		}, nil
	}
	return domain.SearchGroupMetadata{}, ErrNotFound
}
