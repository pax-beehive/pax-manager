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

func (s *MemoryStore) GetEnvelopeAgentRecipient(
	ctx context.Context,
	principal UserPrincipal,
	fromAgentID string,
	toAgentID string,
) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	targetAgent, ok := s.agents[toAgentID]
	if !ok {
		return User{}, ErrNotFound
	}
	targetUser, ok := s.users[targetAgent.OwnerUserID]
	if !ok {
		return User{}, ErrNotFound
	}
	sourceAgent, ok := s.agents[fromAgentID]
	if !ok || sourceAgent.OwnerUserID != principal.User.UserID {
		return User{}, ErrNotFound
	}
	if !s.envelopeUsersShareTeamLocked(principal.User.UserID, targetAgent.OwnerUserID) {
		return User{}, ErrNotFound
	}
	return targetUser, nil
}

func (s *MemoryStore) envelopeUsersShareTeamLocked(
	senderUserID string,
	receiverUserID string,
) bool {
	for _, senderMember := range s.teamMembers {
		if senderMember.UserID != senderUserID ||
			senderMember.Status != domain.TeamMemberStatusActive {
			continue
		}
		team, ok := s.teams[senderMember.TeamID]
		if !ok || team.Status != domain.TeamStatusActive {
			continue
		}
		receiverMember, ok := s.teamMembers[teamMemberKey{
			TeamID: senderMember.TeamID,
			UserID: receiverUserID,
		}]
		if ok && receiverMember.Status == domain.TeamMemberStatusActive {
			return true
		}
	}
	return false
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
		if !canViewEnvelope(filter.Principal, recipientEmail, filter.Direction, envelope) {
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
	if !ok || !canReadEnvelope(principal, normalizeEmail(principal.User.Email), envelope) {
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
	return s.updateRecipientEnvelope(
		ctx,
		principal,
		envelopeID,
		func(envelope Envelope) (Envelope, error) {
			if envelope.Status != domain.EnvelopeStatusPending {
				return Envelope{}, ErrNotFound
			}
			envelope.Status = domain.EnvelopeStatusAccepted
			envelope.AcceptedAt = &acceptedAt
			return envelope, nil
		},
	)
}

func (s *MemoryStore) ArchiveEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	archivedAt time.Time,
) (Envelope, error) {
	return s.updateRecipientEnvelope(
		ctx,
		principal,
		envelopeID,
		func(envelope Envelope) (Envelope, error) {
			envelope.Status = domain.EnvelopeStatusArchived
			envelope.ArchivedAt = &archivedAt
			return envelope, nil
		},
	)
}

func (s *MemoryStore) updateRecipientEnvelope(
	ctx context.Context,
	principal UserPrincipal,
	envelopeID string,
	update func(Envelope) (Envelope, error),
) (Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	envelope, ok := s.envelopes[envelopeID]
	if !ok || !canReceiveEnvelope(principal, normalizeEmail(principal.User.Email), envelope) {
		return Envelope{}, ErrNotFound
	}
	envelope, err := update(envelope)
	if err != nil {
		return Envelope{}, err
	}
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

func canSendEnvelope(principal UserPrincipal, envelope Envelope) bool {
	return envelope.SenderUserID == principal.User.UserID
}

func canViewEnvelope(
	principal UserPrincipal,
	recipientEmail string,
	direction string,
	envelope Envelope,
) bool {
	switch direction {
	case domain.EnvelopeDirectionSent:
		return canSendEnvelope(principal, envelope)
	case "", domain.EnvelopeDirectionReceived:
		return canReceiveEnvelope(principal, recipientEmail, envelope)
	default:
		return false
	}
}

func canReadEnvelope(principal UserPrincipal, recipientEmail string, envelope Envelope) bool {
	return canSendEnvelope(principal, envelope) ||
		canReceiveEnvelope(principal, recipientEmail, envelope)
}
