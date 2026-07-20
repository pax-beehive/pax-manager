package storage

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type friendRow struct {
	FriendID        string     `gorm:"column:friend_id;primaryKey"`
	RequesterUserID string     `gorm:"column:requester_user_id"`
	RequesterEmail  string     `gorm:"column:requester_email"`
	RequesterAlias  string     `gorm:"column:requester_alias"`
	RecipientUserID *string    `gorm:"column:recipient_user_id"`
	RecipientEmail  string     `gorm:"column:recipient_email"`
	RecipientAlias  string     `gorm:"column:recipient_alias"`
	Status          string     `gorm:"column:status"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	AcceptedAt      *time.Time `gorm:"column:accepted_at"`
	RemovedAt       *time.Time `gorm:"column:removed_at"`
	BlockedAt       *time.Time `gorm:"column:blocked_at"`
}

func (friendRow) TableName() string {
	return "friends"
}

func friendModel(friend Friend) *friendRow {
	var recipientUserID *string
	if friend.RecipientUserID != "" {
		recipientUserID = &friend.RecipientUserID
	}
	return &friendRow{
		FriendID:        friend.FriendID,
		RequesterUserID: friend.RequesterUserID,
		RequesterEmail:  friend.RequesterEmail,
		RequesterAlias:  friend.RequesterAlias,
		RecipientUserID: recipientUserID,
		RecipientEmail:  friend.RecipientEmail,
		RecipientAlias:  friend.RecipientAlias,
		Status:          friend.Status,
		CreatedAt:       friend.CreatedAt,
		AcceptedAt:      friend.AcceptedAt,
		RemovedAt:       friend.RemovedAt,
		BlockedAt:       friend.BlockedAt,
	}
}

func friendFromModel(row *friendRow) Friend {
	if row == nil {
		return Friend{}
	}
	return Friend{
		FriendID:        row.FriendID,
		RequesterUserID: row.RequesterUserID,
		RequesterEmail:  row.RequesterEmail,
		RequesterAlias:  row.RequesterAlias,
		RecipientUserID: friendRecipientUserID(row.RecipientUserID),
		RecipientEmail:  row.RecipientEmail,
		RecipientAlias:  row.RecipientAlias,
		Status:          row.Status,
		CreatedAt:       row.CreatedAt,
		AcceptedAt:      row.AcceptedAt,
		RemovedAt:       row.RemovedAt,
		BlockedAt:       row.BlockedAt,
	}
}

func friendsFromModels(rows []friendRow) []Friend {
	out := make([]Friend, 0, len(rows))
	for i := range rows {
		out = append(out, friendFromModel(&rows[i]))
	}
	return out
}

func (s *PostgresStore) CreateFriend(ctx context.Context, friend Friend) (Friend, error) {
	row := friendModel(friend)
	if err := s.gormDB.WithContext(ctx).Create(row).Error; err != nil {
		return Friend{}, err
	}
	return friendFromModel(row), nil
}

func (s *PostgresStore) ListFriends(
	ctx context.Context,
	filter ListFriendsFilter,
) ([]Friend, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	principalEmail := normalizeEmail(filter.Principal.User.Email)
	query := s.gormDB.WithContext(ctx).
		Model(&friendRow{}).
		Where(
			"requester_user_id = ? OR recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)",
			filter.Principal.User.UserID,
			filter.Principal.User.UserID,
			principalEmail,
		).
		Order("created_at DESC").
		Limit(limit)
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	} else {
		query = query.Where("status <> ?", domain.FriendStatusRemoved)
	}
	switch filter.Direction {
	case domain.FriendDirectionSent:
		query = query.Where("requester_user_id = ?", filter.Principal.User.UserID)
	case domain.FriendDirectionReceived:
		query = query.Where(
			"recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)",
			filter.Principal.User.UserID,
			principalEmail,
		)
	}
	if filter.Alias != "" {
		alias := normalizeFriendAlias(filter.Alias)
		query = query.Where(
			"(requester_user_id = ? AND requester_alias = ?) OR ((recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)) AND recipient_alias = ?)",
			filter.Principal.User.UserID,
			alias,
			filter.Principal.User.UserID,
			principalEmail,
			alias,
		)
	}
	if filter.Cursor != "" {
		query = query.Where("friend_id < ?", filter.Cursor)
	}
	var rows []friendRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, mapGormError(err)
	}
	return friendsFromModels(rows), nil
}

func (s *PostgresStore) GetAcceptedFriendByEmail(
	ctx context.Context,
	principal UserPrincipal,
	email string,
) (Friend, error) {
	var row friendRow
	err := s.friendsBetweenQuery(ctx, principal, email).
		Where("status = ?", domain.FriendStatusAccepted).
		First(&row).
		Error
	if err != nil {
		return Friend{}, mapGormError(err)
	}
	return friendFromModel(&row), nil
}

func (s *PostgresStore) ListFriendsBetween(
	ctx context.Context,
	principal UserPrincipal,
	email string,
) ([]Friend, error) {
	var rows []friendRow
	err := s.friendsBetweenQuery(ctx, principal, email).
		Order("created_at DESC").
		Find(&rows).
		Error
	if err != nil {
		return nil, mapGormError(err)
	}
	return friendsFromModels(rows), nil
}

func (s *PostgresStore) DeleteRemovedFriendsBetween(
	ctx context.Context,
	principal UserPrincipal,
	email string,
) error {
	result := s.friendsBetweenQuery(ctx, principal, email).
		Where("status = ?", domain.FriendStatusRemoved).
		Delete(&friendRow{})
	if result.Error != nil {
		return mapGormError(result.Error)
	}
	return nil
}

func (s *PostgresStore) friendsBetweenQuery(
	ctx context.Context,
	principal UserPrincipal,
	email string,
) *gorm.DB {
	principalEmail := normalizeEmail(principal.User.Email)
	counterpartyEmail := normalizeEmail(email)
	return s.gormDB.WithContext(ctx).
		Model(&friendRow{}).
		Where(
			`(
				(requester_user_id = ? AND recipient_email = ?) OR
				((recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)) AND requester_email = ?)
			)`,
			principal.User.UserID,
			counterpartyEmail,
			principal.User.UserID,
			principalEmail,
			counterpartyEmail,
		)
}

func (s *PostgresStore) GetFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
) (Friend, error) {
	var row friendRow
	err := s.readableFriendQuery(ctx, principal).
		Where("friend_id = ?", friendID).
		First(&row).
		Error
	if err != nil {
		return Friend{}, mapGormError(err)
	}
	return friendFromModel(&row), nil
}

func (s *PostgresStore) AcceptFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	alias string,
	acceptedAt time.Time,
) (Friend, error) {
	return s.updateReceivedFriend(ctx, principal, friendID, map[string]any{
		"recipient_user_id": principal.User.UserID,
		"recipient_alias":   alias,
		"status":            domain.FriendStatusAccepted,
		"accepted_at":       acceptedAt,
	})
}

func (s *PostgresStore) UpdateFriendAlias(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	alias string,
) (Friend, error) {
	friend, err := s.GetFriend(ctx, principal, friendID)
	if err != nil {
		return Friend{}, err
	}
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
	return s.updateVisibleActiveFriend(ctx, principal, friendID, map[string]any{
		column: alias,
	})
}

func (s *PostgresStore) RemoveFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	removedAt time.Time,
) (Friend, error) {
	return s.updateVisibleActiveFriend(ctx, principal, friendID, map[string]any{
		"status":     domain.FriendStatusRemoved,
		"removed_at": removedAt,
	})
}

func (s *PostgresStore) BlockFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	blockedAt time.Time,
) (Friend, error) {
	return s.updateVisibleFriend(ctx, principal, friendID, map[string]any{
		"status":     domain.FriendStatusBlocked,
		"blocked_at": blockedAt,
	})
}

func (s *PostgresStore) readableFriendQuery(
	ctx context.Context,
	principal UserPrincipal,
) *gorm.DB {
	principalEmail := normalizeEmail(principal.User.Email)
	return s.gormDB.WithContext(ctx).
		Model(&friendRow{}).
		Where(
			"requester_user_id = ? OR recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?)",
			principal.User.UserID,
			principal.User.UserID,
			principalEmail,
		)
}

func (s *PostgresStore) updateReceivedFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	values map[string]any,
) (Friend, error) {
	principalEmail := normalizeEmail(principal.User.Email)
	result := s.gormDB.WithContext(ctx).
		Model(&friendRow{}).
		Where(
			"friend_id = ? AND status = ? AND (recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?))",
			friendID,
			domain.FriendStatusPending,
			principal.User.UserID,
			principalEmail,
		).
		Updates(values)
	if result.Error != nil {
		return Friend{}, mapGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return Friend{}, ErrNotFound
	}
	return s.GetFriend(ctx, principal, friendID)
}

func (s *PostgresStore) updateVisibleFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	values map[string]any,
) (Friend, error) {
	principalEmail := normalizeEmail(principal.User.Email)
	result := s.gormDB.WithContext(ctx).
		Model(&friendRow{}).
		Where(
			"friend_id = ? AND (requester_user_id = ? OR recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?))",
			friendID,
			principal.User.UserID,
			principal.User.UserID,
			principalEmail,
		).
		Updates(values)
	if result.Error != nil {
		return Friend{}, mapGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return Friend{}, ErrNotFound
	}
	return s.GetFriend(ctx, principal, friendID)
}

func (s *PostgresStore) updateVisibleActiveFriend(
	ctx context.Context,
	principal UserPrincipal,
	friendID string,
	values map[string]any,
) (Friend, error) {
	principalEmail := normalizeEmail(principal.User.Email)
	result := s.gormDB.WithContext(ctx).
		Model(&friendRow{}).
		Where(
			"friend_id = ? AND status IN ? AND (requester_user_id = ? OR recipient_user_id = ? OR (recipient_user_id IS NULL AND recipient_email = ?))",
			friendID,
			[]string{domain.FriendStatusPending, domain.FriendStatusAccepted},
			principal.User.UserID,
			principal.User.UserID,
			principalEmail,
		).
		Updates(values)
	if result.Error != nil {
		return Friend{}, mapGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return Friend{}, ErrNotFound
	}
	return s.GetFriend(ctx, principal, friendID)
}

func friendRecipientUserID(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
