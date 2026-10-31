package storage

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) ReadSearchDocument(
	_ context.Context,
	ownerID, kind, sourceID string,
) (domain.SearchDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ownerID == "" {
		return domain.SearchDocument{}, ErrNotFound
	}
	if kind == "message" {
		return s.readSearchMessageLocked(ownerID, sourceID)
	}
	if kind == "session" {
		for _, session := range s.sessions {
			if strconv.FormatInt(session.ID, 10) != sourceID || session.ArchivedAt != nil ||
				session.Source == domain.SecretaryInternalSource || strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) {
				continue
			}
			agent, ok := s.agents[session.AgentID]
			if !ok || agent.OwnerUserID != ownerID ||
				!s.searchNodesVisibleLocked(agent.NodeID, session.NodeID) {
				return domain.SearchDocument{}, ErrNotFound
			}
			title := session.SessionName
			if session.NameIsCustom || session.CustomSessionName != "" {
				title = session.CustomSessionName
			}
			if strings.TrimSpace(title) == "" {
				return domain.SearchDocument{}, ErrNotFound
			}
			return domain.SearchDocument{Kind: "session", SourceID: sourceID, OwnerUserID: ownerID,
				AgentID: agent.AgentID, SessionID: session.SessionID, ConversationID: session.ConversationID,
				Title: title, ContentHash: domain.SearchContentHash(title), CreatedAt: session.CreatedAt,
				CanonicalizationVersion: domain.SearchCanonicalizationVersion}, nil
		}
	}
	return domain.SearchDocument{}, ErrNotFound
}

func (s *MemoryStore) readSearchMessageLocked(
	ownerID, messageID string,
) (domain.SearchDocument, error) {
	message, ok := s.messages[messageID]
	if !ok {
		return domain.SearchDocument{}, ErrNotFound
	}
	agent, ok := s.agents[message.AgentID]
	session, sessionExists := s.sessions[sessionKey(message.AgentID, message.SessionID)]
	if !ok || agent.OwnerUserID != ownerID || session.ArchivedAt != nil ||
		session.Source == domain.SecretaryInternalSource || strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) ||
		(message.SessionID != "" && !sessionExists) ||
		!s.searchNodesVisibleLocked(agent.NodeID, message.NodeID, session.NodeID) {
		return domain.SearchDocument{}, ErrNotFound
	}
	rows := []domain.MessageWithParts{{Message: message}}
	for _, candidate := range s.messages {
		if candidate.MessageID != messageID && candidate.AgentID == message.AgentID &&
			candidate.SessionID == message.SessionID {
			rows = append(rows, domain.MessageWithParts{Message: candidate})
		}
	}
	visible := false
	for _, row := range domain.NormalTranscriptMessages(rows) {
		if row.MessageID == messageID {
			visible = true
		}
	}
	if !visible {
		return domain.SearchDocument{}, ErrNotFound
	}
	var parts []domain.MessagePart
	for key, part := range s.messageParts {
		if key.MessageID == messageID {
			parts = append(parts, part)
		}
	}
	doc, err := domain.CanonicalSearchMessage(
		domain.MessageWithParts{Message: message, Parts: parts},
	)
	doc.OwnerUserID = ownerID
	return doc, err
}

func (s *MemoryStore) searchNodesVisibleLocked(nodeIDs ...string) bool {
	for _, id := range nodeIDs {
		if id != "" {
			if _, ok := s.nodes[id]; !ok {
				return false
			}
		}
	}
	return true
}

func (s *MemoryStore) ReadSearchContext(
	_ context.Context,
	ownerID, messageID string,
	before, after int,
) ([]domain.SearchDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ownerID == "" || before < 0 || after < 0 || before > 100 || after > 100 {
		return nil, ErrNotFound
	}
	anchor, err := s.readSearchMessageLocked(ownerID, messageID)
	if err != nil {
		return nil, err
	}
	var docs []domain.SearchDocument
	for _, message := range s.messages {
		if message.AgentID != anchor.AgentID || message.SessionID != anchor.SessionID {
			continue
		}
		doc, err := s.readSearchMessageLocked(ownerID, message.MessageID)
		if err == nil {
			docs = append(docs, doc)
		}
	}
	sortSearchContext(docs)
	for i, doc := range docs {
		if doc.SourceID == messageID {
			start := max(0, i-before)
			end := min(len(docs), i+after+1)
			return docs[start:end], nil
		}
	}
	return nil, ErrNotFound
}

func sortSearchContext(docs []domain.SearchDocument) {
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].SessionSeq != docs[j].SessionSeq {
			return docs[i].SessionSeq < docs[j].SessionSeq
		}
		if !docs[i].CreatedAt.Equal(docs[j].CreatedAt) {
			return docs[i].CreatedAt.Before(docs[j].CreatedAt)
		}
		return docs[i].SourceID < docs[j].SourceID
	})
}
