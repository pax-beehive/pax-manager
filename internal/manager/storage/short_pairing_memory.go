package storage

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) shortRequest(request E2EEPairingRequest) (E2EEPairingRequest, error) {
	stored, ok := s.e2eePairingRequests[request.PairingID]
	if !ok || stored.OwnerUserID != request.OwnerUserID || stored.AgentID != request.AgentID {
		return stored, ErrNotFound
	}
	return stored, nil
}

func (s *MemoryStore) CreateShortPairingAttempt(
	_ context.Context,
	request E2EEPairingRequest,
	input domain.ShortPairingAttempt,
) (domain.ShortPairingAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.shortRequest(request)
	if err != nil {
		return input, err
	}
	if !validShortAttempt(input) {
		return input, ErrConflict
	}
	now := s.now().UTC()
	deadline, err := shortAttemptDeadline(stored, input.Generation, now)
	if err != nil {
		return input, err
	}
	if s.shortPairingAttempts == nil {
		s.shortPairingAttempts = make(map[string]domain.ShortPairingAttempt)
	}
	if old, ok := s.shortPairingAttempts[input.AttemptID]; ok {
		if old.PairingID == stored.PairingID && old.Generation == input.Generation &&
			old.ClientHello == input.ClientHello &&
			bytes.Equal(old.ApproverCapabilityHash, input.ApproverCapabilityHash) {
			return cloneShortAttempt(old), nil
		}
		return input, ErrConflict
	}
	if s.shortPairingBudgets == nil {
		s.shortPairingBudgets = make(map[string]shortPairingBudget)
	}
	for _, key := range shortBudgetKeys(stored) {
		b := s.shortPairingBudgets[key]
		if now.Before(b.Started.Add(10*time.Minute)) && b.Count >= 30 {
			return input, domain.ErrShortPairingLimited
		}
	}
	for _, key := range shortBudgetKeys(stored) {
		b := s.shortPairingBudgets[key]
		if !now.Before(b.Started.Add(10 * time.Minute)) {
			b = shortPairingBudget{Started: now}
		}
		b.Count++
		s.shortPairingBudgets[key] = b
	}
	input.PairingID = stored.PairingID
	input.CreatedAt = now
	input.ExpiresAt = deadline
	input.Stage = 0
	s.shortPairingAttempts[input.AttemptID] = cloneShortAttempt(input)
	return cloneShortAttempt(input), nil
}

func (s *MemoryStore) ListShortPairingAttempts(
	_ context.Context,
	request E2EEPairingRequest,
	capability []byte,
) ([]domain.ShortPairingAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.shortRequest(request)
	if err != nil {
		return nil, err
	}
	if len(capability) != 32 || !bytes.Equal(capability, stored.RecipientCapabilityHash) {
		return nil, ErrNotFound
	}
	if err = shortPairingPending(stored, s.now().UTC()); err != nil {
		return nil, err
	}
	results := make([]domain.ShortPairingAttempt, 0)
	for _, a := range s.shortPairingAttempts {
		if a.PairingID == stored.PairingID && a.Stage < 3 && a.ExpiresAt.After(s.now().UTC()) {
			results = append(results, cloneShortAttempt(a))
		}
	}
	sort.Slice(
		results,
		func(i, j int) bool { return results[i].CreatedAt.Before(results[j].CreatedAt) },
	)
	return results, nil
}

func (s *MemoryStore) GetShortPairingAttempt(
	_ context.Context,
	request E2EEPairingRequest,
	id string,
	capability []byte,
) (domain.ShortPairingAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.shortRequest(request)
	if err != nil {
		return domain.ShortPairingAttempt{}, err
	}
	a, ok := s.shortPairingAttempts[id]
	if !ok || a.PairingID != stored.PairingID || len(capability) != 32 ||
		!bytes.Equal(capability, a.ApproverCapabilityHash) {
		return domain.ShortPairingAttempt{}, ErrNotFound
	}
	return cloneShortAttempt(a), nil
}

func (s *MemoryStore) AdvanceShortPairingAttempt(
	_ context.Context,
	request E2EEPairingRequest,
	id string,
	capability []byte,
	stage int,
	payload string,
) (domain.ShortPairingAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.shortRequest(request)
	if err != nil {
		return domain.ShortPairingAttempt{}, err
	}
	a, ok := s.shortPairingAttempts[id]
	if !ok || a.PairingID != stored.PairingID {
		return a, ErrNotFound
	}
	a, err = shortAdvance(stored, a, capability, stage, payload, s.now().UTC())
	if err != nil {
		return a, err
	}
	s.shortPairingAttempts[id] = cloneShortAttempt(a)
	return cloneShortAttempt(a), nil
}

func (s *MemoryStore) EndShortPairing(
	_ context.Context,
	request E2EEPairingRequest,
	capability []byte,
	reason string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.shortRequest(request)
	if err != nil {
		return err
	}
	if err = shortEnd(stored, request, s.shortPairingAttempts[request.ApprovalAttemptID], capability, reason, s.now().UTC()); err != nil {
		return err
	}
	if stored.CancelledAt != nil || stored.RejectedAt != nil {
		return nil
	}
	now := s.now().UTC()
	switch reason {
	case "cancelled":
		stored.CancelledAt = &now
	case "rejected":
		stored.RejectedAt = &now
	default:
		return ErrConflict
	}
	s.e2eePairingRequests[stored.PairingID] = stored
	return nil
}
func cloneShortAttempt(a domain.ShortPairingAttempt) domain.ShortPairingAttempt {
	a.ApproverCapabilityHash = bytes.Clone(a.ApproverCapabilityHash)
	if a.ConfirmationExpiresAt != nil {
		v := *a.ConfirmationExpiresAt
		a.ConfirmationExpiresAt = &v
	}
	return a
}
