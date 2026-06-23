package storage

import (
	"context"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) CreateEnvelope(
	ctx context.Context,
	envelope Envelope,
) (Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[envelope.SenderUserID]; !ok {
		return Envelope{}, ErrNotFound
	}
	if envelope.RecipientUserID != "" {
		if _, ok := s.users[envelope.RecipientUserID]; !ok {
			return Envelope{}, ErrNotFound
		}
	}
	if envelope.CreatedAt.IsZero() {
		envelope.CreatedAt = s.now().UTC()
	}
	s.envelopes[envelope.EnvelopeID] = envelope
	return envelope, nil
}

func (s *MemoryStore) ListEnvelopes(
	ctx context.Context,
	filter ListEnvelopesFilter,
) ([]Envelope, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	recipientEmail := normalizeEmail(filter.Principal.User.Email)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Envelope, 0)
	for _, envelope := range s.envelopes {
		if !canReceiveEnvelope(filter.Principal, recipientEmail, envelope) {
			continue
		}
		if filter.Status != "" && envelope.Status != filter.Status {
			continue
		}
		out = append(out, envelope)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) GetEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
) (Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	envelope, ok := s.envelopes[envelopeID]
	if !ok || !canReceiveEnvelope(principal, normalizeEmail(principal.User.Email), envelope) {
		return Envelope{}, ErrNotFound
	}
	return envelope, nil
}

func (s *MemoryStore) AcceptEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	acceptedAt time.Time,
) (Envelope, error) {
	return s.updateRecipientEnvelope(ctx, principal, envelopeID, func(envelope Envelope) Envelope {
		envelope.Status = domain.EnvelopeStatusAccepted
		envelope.AcceptedAt = &acceptedAt
		return envelope
	})
}

func (s *MemoryStore) ArchiveEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	archivedAt time.Time,
) (Envelope, error) {
	return s.updateRecipientEnvelope(ctx, principal, envelopeID, func(envelope Envelope) Envelope {
		envelope.Status = domain.EnvelopeStatusArchived
		envelope.ArchivedAt = &archivedAt
		return envelope
	})
}

func (s *MemoryStore) updateRecipientEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	update func(Envelope) Envelope,
) (Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	envelope, ok := s.envelopes[envelopeID]
	if !ok || !canReceiveEnvelope(principal, normalizeEmail(principal.User.Email), envelope) {
		return Envelope{}, ErrNotFound
	}
	envelope = update(envelope)
	if envelope.RecipientUserID == "" {
		envelope.RecipientUserID = principal.User.UserID
	}
	s.envelopes[envelopeID] = envelope
	return envelope, nil
}

func canReceiveEnvelope(principal UserPrincipal, recipientEmail string, envelope Envelope) bool {
	return envelope.RecipientUserID == principal.User.UserID ||
		(envelope.RecipientUserID == "" && normalizeEmail(envelope.RecipientEmail) == recipientEmail)
}
