package storage

import (
	"context"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) CreateFriend(ctx context.Context, friend Friend) (Friend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[friend.RequesterUserID]; !ok {
		return Friend{}, ErrNotFound
	}
	if friend.RecipientUserID != "" {
		if _, ok := s.users[friend.RecipientUserID]; !ok {
			return Friend{}, ErrNotFound
		}
	}
	if friend.CreatedAt.IsZero() {
		friend.CreatedAt = s.now().UTC()
	}
	s.friends[friend.FriendID] = friend
	return friend, nil
}

func (s *MemoryStore) ListFriends(
	ctx context.Context,
	filter ListFriendsFilter,
) ([]Friend, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	principalEmail := normalizeEmail(filter.Principal.User.Email)
	alias := normalizeFriendAlias(filter.Alias)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Friend, 0)
	for _, friend := range s.friends {
		if !friendVisibleToPrincipal(filter.Principal, principalEmail, friend) {
			continue
		}
		if filter.Status != "" && friend.Status != filter.Status {
			continue
		}
		if filter.Direction != "" &&
			!friendMatchesDirection(filter.Principal, principalEmail, friend, filter.Direction) {
			continue
		}
		if alias != "" && !friendMatchesAlias(filter.Principal, principalEmail, friend, alias) {
			continue
		}
		out = append(out, friend)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) GetFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
) (Friend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	friend, ok := s.friends[friendID]
	if !ok || !friendVisibleToPrincipal(principal, normalizeEmail(principal.User.Email), friend) {
		return Friend{}, ErrNotFound
	}
	return friend, nil
}

func (s *MemoryStore) GetAcceptedFriendByEmail(
	ctx context.Context,
	principal UserPrincipal,
	email string,
) (Friend, error) {
	principalEmail := normalizeEmail(principal.User.Email)
	counterpartyEmail := normalizeEmail(email)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, friend := range s.friends {
		if friend.Status != domain.FriendStatusAccepted {
			continue
		}
		if friendCounterpartyMatches(principal, principalEmail, friend, counterpartyEmail) {
			return friend, nil
		}
	}
	return Friend{}, ErrNotFound
}

func (s *MemoryStore) AcceptFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	alias string,
	acceptedAt time.Time,
) (Friend, error) {
	return s.updateVisibleFriend(ctx, principal, friendID, func(friend Friend) (Friend, error) {
		if !friendReceivedByPrincipal(principal, normalizeEmail(principal.User.Email), friend) {
			return Friend{}, ErrNotFound
		}
		if friend.Status != domain.FriendStatusPending {
			return Friend{}, ErrNotFound
		}
		friend.RecipientUserID = principal.User.UserID
		friend.RecipientAlias = alias
		friend.Status = domain.FriendStatusAccepted
		friend.AcceptedAt = &acceptedAt
		return friend, nil
	})
}

func (s *MemoryStore) UpdateFriendAlias(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	alias string,
) (Friend, error) {
	return s.updateVisibleFriend(ctx, principal, friendID, func(friend Friend) (Friend, error) {
		if !friendStatusAllowsAliasUpdate(friend.Status) {
			return Friend{}, ErrNotFound
		}
		column, ok := friendAliasColumnForPrincipal(
			principal,
			normalizeEmail(principal.User.Email),
			friend,
		)
		if !ok {
			return Friend{}, ErrNotFound
		}
		switch column {
		case "requester_alias":
			friend.RequesterAlias = alias
		case "recipient_alias":
			friend.RecipientAlias = alias
		default:
			return Friend{}, ErrNotFound
		}
		return friend, nil
	})
}

func (s *MemoryStore) RemoveFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	removedAt time.Time,
) (Friend, error) {
	return s.updateVisibleFriend(ctx, principal, friendID, func(friend Friend) (Friend, error) {
		friend.Status = domain.FriendStatusRemoved
		friend.RemovedAt = &removedAt
		return friend, nil
	})
}

func (s *MemoryStore) BlockFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	blockedAt time.Time,
) (Friend, error) {
	return s.updateVisibleFriend(ctx, principal, friendID, func(friend Friend) (Friend, error) {
		friend.Status = domain.FriendStatusBlocked
		friend.BlockedAt = &blockedAt
		return friend, nil
	})
}

func (s *MemoryStore) updateVisibleFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	update func(Friend) (Friend, error),
) (Friend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	friend, ok := s.friends[friendID]
	if !ok || !friendVisibleToPrincipal(principal, normalizeEmail(principal.User.Email), friend) {
		return Friend{}, ErrNotFound
	}
	updated, err := update(friend)
	if err != nil {
		return Friend{}, err
	}
	s.friends[friendID] = updated
	return updated, nil
}

func friendVisibleToPrincipal(principal UserPrincipal, principalEmail string, friend Friend) bool {
	return friend.RequesterUserID == principal.User.UserID ||
		friend.RecipientUserID == principal.User.UserID ||
		(friend.RecipientUserID == "" && normalizeEmail(friend.RecipientEmail) == principalEmail)
}

func friendReceivedByPrincipal(principal UserPrincipal, principalEmail string, friend Friend) bool {
	return friend.RecipientUserID == principal.User.UserID ||
		(friend.RecipientUserID == "" && normalizeEmail(friend.RecipientEmail) == principalEmail)
}

func friendMatchesDirection(
	principal UserPrincipal,
	principalEmail string,
	friend Friend,
	direction string,
) bool {
	switch direction {
	case domain.FriendDirectionSent:
		return friend.RequesterUserID == principal.User.UserID
	case domain.FriendDirectionReceived:
		return friendReceivedByPrincipal(principal, principalEmail, friend)
	default:
		return true
	}
}

func friendMatchesAlias(
	principal UserPrincipal,
	principalEmail string,
	friend Friend,
	alias string,
) bool {
	if friend.RequesterUserID == principal.User.UserID {
		return normalizeFriendAlias(friend.RequesterAlias) == alias
	}
	if friendReceivedByPrincipal(principal, principalEmail, friend) {
		return normalizeFriendAlias(friend.RecipientAlias) == alias
	}
	return false
}

func friendCounterpartyMatches(
	principal UserPrincipal,
	principalEmail string,
	friend Friend,
	counterpartyEmail string,
) bool {
	if friend.RequesterUserID == principal.User.UserID {
		return normalizeEmail(friend.RecipientEmail) == counterpartyEmail
	}
	if friendReceivedByPrincipal(principal, principalEmail, friend) {
		return normalizeEmail(friend.RequesterEmail) == counterpartyEmail
	}
	return false
}

func normalizeFriendAlias(alias string) string {
	return normalizeEmail(alias)
}

func friendStatusAllowsAliasUpdate(status string) bool {
	return status == domain.FriendStatusPending || status == domain.FriendStatusAccepted
}

func friendAliasColumnForPrincipal(
	principal UserPrincipal,
	principalEmail string,
	friend Friend,
) (string, bool) {
	if friend.RequesterUserID == principal.User.UserID {
		return "requester_alias", true
	}
	if friendReceivedByPrincipal(principal, principalEmail, friend) {
		return "recipient_alias", true
	}
	return "", false
}
