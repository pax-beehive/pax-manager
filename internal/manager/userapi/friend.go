package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const friendAliasLimit = 64

func (s *Service) CreateFriend(
	c context.Context,
	meta auth.RequestMetadata,
	req domain.CreateFriendRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	recipientEmail := domain.NormalizeEmail(req.Email)
	if recipientEmail == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "email is required"}
	}
	if recipientEmail == domain.NormalizeEmail(principal.User.Email) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "cannot friend yourself",
		}
	}
	alias, err := normalizeFriendAlias(req.Alias, recipientEmail)
	if err != nil {
		return 0, nil, err
	}
	friendID, err := s.secrets.New("fr")
	if err != nil {
		return 0, nil, err
	}
	recipientUserID := ""
	recipient, err := s.store.GetUserByEmail(c, recipientEmail)
	if err == nil {
		recipientUserID = recipient.UserID
	} else if !errors.Is(err, domain.ErrNotFound) {
		return 0, nil, err
	}
	friend, err := s.store.CreateFriend(c, domain.Friend{
		FriendID:        friendID,
		RequesterUserID: principal.User.UserID,
		RequesterEmail:  principal.User.Email,
		RequesterAlias:  alias,
		RecipientUserID: recipientUserID,
		RecipientEmail:  recipientEmail,
		Status:          domain.FriendStatusPending,
		CreatedAt:       s.clock().UTC(),
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"friend": friend}, nil
}

func (s *Service) ListFriends(
	c context.Context,
	meta auth.RequestMetadata,
	filter domain.ListFriendsFilter,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	filter.Principal = principal
	friends, err := s.store.ListFriends(c, filter)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"friends": friends}, nil
}

func (s *Service) GetFriend(
	c context.Context,
	meta auth.RequestMetadata,
	friendID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	friend, err := s.store.GetFriend(c, principal, friendID)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"friend": friend}, nil
}

func (s *Service) AcceptFriend(
	c context.Context,
	meta auth.RequestMetadata,
	friendID string,
	req domain.AcceptFriendRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	alias, err := normalizeFriendAlias(req.Alias, "")
	if err != nil {
		return 0, nil, err
	}
	friend, err := s.store.AcceptFriend(c, principal, friendID, alias, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"friend": friend}, nil
}

func (s *Service) UpdateFriendAlias(
	c context.Context,
	meta auth.RequestMetadata,
	friendID string,
	req domain.UpdateFriendAliasRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	alias, err := normalizeFriendAlias(req.Alias, "")
	if err != nil {
		return 0, nil, err
	}
	if alias == "" {
		return 0, nil, apperr.Error{Status: http.StatusBadRequest, Message: "alias is required"}
	}
	friend, err := s.store.UpdateFriendAlias(c, principal, friendID, alias)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"friend": friend}, nil
}

func (s *Service) RemoveFriend(
	c context.Context,
	meta auth.RequestMetadata,
	friendID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	friend, err := s.store.RemoveFriend(c, principal, friendID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"friend": friend}, nil
}

func (s *Service) BlockFriend(
	c context.Context,
	meta auth.RequestMetadata,
	friendID string,
) (int, any, error) {
	principal, err := s.principal.Principal(c, meta)
	if err != nil {
		return 0, nil, err
	}
	friend, err := s.store.BlockFriend(c, principal, friendID, s.clock().UTC())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"friend": friend}, nil
}

func normalizeFriendAlias(rawAlias string, fallbackEmail string) (string, error) {
	alias := strings.TrimPrefix(strings.TrimSpace(rawAlias), "@")
	if alias == "" && fallbackEmail != "" {
		alias = strings.Split(fallbackEmail, "@")[0]
	}
	alias = strings.ToLower(alias)
	if alias == "" {
		return "", nil
	}
	if len(alias) > friendAliasLimit {
		return "", apperr.Error{Status: http.StatusBadRequest, Message: "alias is too long"}
	}
	for _, value := range alias {
		if (value >= 'a' && value <= 'z') ||
			(value >= '0' && value <= '9') ||
			value == '-' ||
			value == '_' ||
			value == '.' {
			continue
		}
		return "", apperr.Error{Status: http.StatusBadRequest, Message: "alias is invalid"}
	}
	return alias, nil
}
