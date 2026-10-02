package storage

import (
	"context"
	"errors"

	"gorm.io/gorm/clause"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	dbmodel "github.com/pax-beehive/pax-manager/internal/manager/storage/dal/model"
)

// EnsureRegionalUser preserves the ID chosen by the global directory. Conflicts
// require operator reconciliation; an existing local account is never renamed.
func (s *PostgresStore) EnsureRegionalUser(
	ctx context.Context,
	userID, email, role string,
) (User, error) {
	email = normalizeEmail(email)
	if userID == "" || email == "" {
		return User{}, ErrUnauthorized
	}
	now := s.now().UTC()
	row := dbmodel.User{
		UserID:     userID,
		Email:      email,
		Role:       &role,
		CreatedAt:  &now,
		LastSeenAt: &now,
	}
	if err := s.gormDB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return User{}, err
	}
	user, err := s.GetUserByEmail(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		return User{}, ErrConflict
	}
	if err != nil {
		return User{}, err
	}
	if user.UserID != userID {
		return User{}, ErrConflict
	}
	return user, nil
}

func (s *MemoryStore) EnsureRegionalUser(
	_ context.Context,
	userID, email, role string,
) (User, error) {
	email = normalizeEmail(email)
	if userID == "" || email == "" {
		return User{}, ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.usersByEmail[email]; ok {
		if id != userID {
			return User{}, ErrConflict
		}
		return s.users[id], nil
	}
	if _, ok := s.users[userID]; ok {
		return User{}, ErrConflict
	}
	now := s.now().UTC()
	user := User{UserID: userID, Email: email, Role: role, CreatedAt: now, LastSeenAt: &now}
	s.users[userID] = user
	s.usersByEmail[email] = userID
	return user, nil
}
