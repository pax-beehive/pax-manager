package storage

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryGuidanceStore = (*MemoryStore)(nil)

func (s *MemoryStore) GetSecretaryGuidance(
	_ context.Context,
	owner, scope, source string,
) (domain.SecretaryGuidance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" {
		return domain.SecretaryGuidance{}, ErrUnauthorized
	}
	if err := domain.ValidateSecretaryGuidanceScope(scope, source); err != nil {
		return domain.SecretaryGuidance{}, err
	}
	if scope == domain.SecretaryGuidanceSession && !s.secretaryGuidanceSourceLocked(owner, source) {
		return domain.SecretaryGuidance{}, ErrNotFound
	}
	return s.secretaryGuidanceLocked(owner, scope, source), nil
}

func (s *MemoryStore) UpdateSecretaryGuidance(
	_ context.Context,
	owner, scope, source string,
	update domain.SecretaryGuidanceUpdate,
) (domain.SecretaryGuidance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" {
		return domain.SecretaryGuidance{}, ErrUnauthorized
	}
	if err := domain.ValidateSecretaryGuidanceScope(scope, source); err != nil {
		return domain.SecretaryGuidance{}, err
	}
	instruction, err := domain.NormalizeSecretaryInstruction(update.Instruction)
	if err != nil {
		return domain.SecretaryGuidance{}, err
	}
	if scope == domain.SecretaryGuidanceSession && !s.secretaryGuidanceSourceLocked(owner, source) {
		return domain.SecretaryGuidance{}, ErrNotFound
	}
	old := s.secretaryGuidanceLocked(owner, scope, source)
	if update.ExpectedRevision < 0 || old.Revision != update.ExpectedRevision {
		return domain.SecretaryGuidance{}, ErrConflict
	}
	result := domain.SecretaryGuidance{
		OwnerUserID: owner, Scope: scope, SourceID: source,
		Revision: old.Revision + 1, Instruction: instruction, UpdatedAt: s.now().UTC(),
	}
	state := s.secretaryLocked()
	if state.guidance == nil {
		state.guidance = map[string]domain.SecretaryGuidance{}
	}
	state.guidance[secretaryGuidanceKey(owner, scope, source)] = result
	return result, nil
}
