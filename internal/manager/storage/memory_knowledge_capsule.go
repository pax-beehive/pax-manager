package storage

import (
	"context"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) CreateKnowledgeCapsule(
	ctx context.Context,
	capsule KnowledgeCapsule,
) (KnowledgeCapsule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[capsule.OwnerUserID]; !ok {
		return KnowledgeCapsule{}, ErrNotFound
	}
	if capsule.CreatedAt.IsZero() {
		capsule.CreatedAt = s.now().UTC()
	}
	s.knowledgeCapsules[capsule.CapsuleID] = capsule
	return capsule, nil
}

func (s *MemoryStore) ListKnowledgeCapsules(
	ctx context.Context,
	filter ListKnowledgeCapsulesFilter,
) ([]KnowledgeCapsule, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]KnowledgeCapsule, 0)
	for _, capsule := range s.knowledgeCapsules {
		if capsule.OwnerUserID != filter.Principal.User.UserID {
			continue
		}
		if filter.Status != "" && capsule.Status != filter.Status {
			continue
		}
		if filter.Keyword != "" && capsule.Keyword != filter.Keyword {
			continue
		}
		if filter.SourceSessionID != "" && capsule.SourceSessionID != filter.SourceSessionID {
			continue
		}
		out = append(out, capsule)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) GetKnowledgeCapsule(
	ctx context.Context,
	principal UserPrincipal,
	capsuleID string,
) (KnowledgeCapsule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capsule, ok := s.knowledgeCapsules[capsuleID]
	if !ok || capsule.OwnerUserID != principal.User.UserID {
		return KnowledgeCapsule{}, ErrNotFound
	}
	return capsule, nil
}

func (s *MemoryStore) ArchiveKnowledgeCapsule(
	ctx context.Context,
	principal UserPrincipal,
	capsuleID string,
	archivedAt time.Time,
) (KnowledgeCapsule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capsule, ok := s.knowledgeCapsules[capsuleID]
	if !ok || capsule.OwnerUserID != principal.User.UserID {
		return KnowledgeCapsule{}, ErrNotFound
	}
	capsule.Status = domain.KnowledgeCapsuleStatusArchived
	capsule.ArchivedAt = &archivedAt
	s.knowledgeCapsules[capsuleID] = capsule
	return capsule, nil
}

func (s *MemoryStore) CreateKnowledgeInjection(
	ctx context.Context,
	injection SessionKnowledgeInjection,
) (SessionKnowledgeInjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[injection.OwnerUserID]; !ok {
		return SessionKnowledgeInjection{}, ErrNotFound
	}
	if _, ok := s.knowledgeCapsules[injection.CapsuleID]; !ok {
		return SessionKnowledgeInjection{}, ErrNotFound
	}
	if injection.CreatedAt.IsZero() {
		injection.CreatedAt = s.now().UTC()
	}
	s.knowledgeInjections[injection.InjectionID] = injection
	return injection, nil
}

func (s *MemoryStore) ListKnowledgeInjections(
	ctx context.Context,
	filter ListKnowledgeInjectionsFilter,
) ([]SessionKnowledgeInjection, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionKnowledgeInjection, 0)
	for _, injection := range s.knowledgeInjections {
		if injection.OwnerUserID != filter.Principal.User.UserID {
			continue
		}
		if filter.TargetSessionID != "" && injection.TargetSessionID != filter.TargetSessionID {
			continue
		}
		out = append(out, injection)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
