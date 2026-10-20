package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

type notificationReceiverRow struct {
	ReceiverID    string `gorm:"primaryKey"`
	OwnerUserID   string
	DeviceLabel   string
	Enabled       bool
	ExpiresAt     *time.Time
	RevokedAt     *time.Time
	InvalidatedAt *time.Time
	RecordJSON    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (notificationReceiverRow) TableName() string { return "notification_receivers" }

func notificationRow(record notifications.RegistrationRecord) notificationReceiverRow {
	encoded, _ := json.Marshal(record)
	return notificationReceiverRow{
		ReceiverID: record.ID, OwnerUserID: record.UserID, DeviceLabel: record.DeviceLabel,
		Enabled: record.Enabled, ExpiresAt: record.ExpiresAt, RevokedAt: record.RevokedAt,
		InvalidatedAt: record.InvalidatedAt, RecordJSON: string(encoded),
		CreatedAt: record.CreatedAt, UpdatedAt: record.LastRegisteredAt,
	}
}

func (s *PostgresStore) RegisterNotificationReceiver(
	ctx context.Context,
	owner string,
	input notifications.Registration,
) (notifications.ReceiverMetadata, error) {
	var view notifications.ReceiverMetadata
	err := s.gormDB.WithContext(ctx).
		Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).
		Transaction(func(tx *gorm.DB) error {
			var user struct {
				UserID string
				Email  string
			}
			if err := tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", owner).Take(&user).Error; err != nil {
				return err
			}
			var row notificationReceiverRow
			err := tx.Where("receiver_id = ?", notifications.ReceiverID(input.Subscription.Endpoint)).
				Take(&row).
				Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var old notifications.RegistrationRecord
			if err == nil {
				if err := json.Unmarshal([]byte(row.RecordJSON), &old); err != nil {
					return err
				}
			}
			record, err := notifications.RefreshRegistration(old, owner, input, s.now())
			if err != nil {
				return err
			}
			if old.ID == "" || old.RevokedAt != nil {
				var count int64
				if err := tx.Model(&notificationReceiverRow{}).Where("owner_user_id = ? AND revoked_at IS NULL", owner).Count(&count).Error; err != nil {
					return err
				}
				if count >= notifications.MaxReceiversPerUser {
					return notifications.ErrReceiverLimit
				}
			}
			saved := notificationRow(record)
			if old.ID == "" {
				if err := tx.Create(&saved).Error; err != nil {
					if isUniqueViolation(err) ||
						strings.Contains(err.Error(), "UNIQUE constraint") {
						return notifications.ErrReceiverConflict
					}
					return err
				}
			} else if err := tx.Save(&saved).Error; err != nil {
				return err
			}
			view = record.Metadata(user.Email, s.now())
			return nil
		})
	return view, err
}

func (s *PostgresStore) RevokeNotificationReceiver(ctx context.Context, owner, id string) error {
	return s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedOwner struct{ UserID string }
		if err := tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", owner).Take(&lockedOwner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return notifications.ErrReceiverNotFound
			}
			return err
		}
		var row notificationReceiverRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("receiver_id = ? AND owner_user_id = ?", id, owner).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return notifications.ErrReceiverNotFound
			}
			return err
		}
		if row.RevokedAt != nil {
			return nil
		}
		var record notifications.RegistrationRecord
		if err := json.Unmarshal([]byte(row.RecordJSON), &record); err != nil {
			return err
		}
		now := s.now().UTC()
		record.RevokedAt, record.Enabled = &now, false
		record.Subscription = notifications.WebPushSubscription{}
		record.Revision++
		updated := notificationRow(record)
		updated.UpdatedAt = now
		return tx.Save(&updated).Error
	})
}

const notificationStatusSQL = `CASE WHEN r.revoked_at IS NOT NULL THEN 'revoked'
 WHEN r.invalidated_at IS NOT NULL THEN 'invalidated'
 WHEN r.expires_at IS NOT NULL AND r.expires_at <= ? THEN 'expired'
 WHEN r.enabled THEN 'enabled' ELSE 'disabled' END`

func notificationScope(
	db *gorm.DB,
	filter notifications.SubscriptionFilter,
	now time.Time,
) *gorm.DB {
	query := db.Table("notification_receivers r").
		Joins("JOIN users u ON u.user_id = r.owner_user_id")
	if filter.UserID != "" {
		query = query.Where("r.owner_user_id = ?", filter.UserID)
	}
	if filter.Status != "" {
		query = query.Where("("+notificationStatusSQL+") = ?", now, filter.Status)
	}
	if search := strings.ToLower(strings.TrimSpace(filter.Search)); search != "" {
		search = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(search)
		pattern := "%" + search + "%"
		query = query.Where(
			"(LOWER(u.email) LIKE ? ESCAPE '!' OR LOWER(r.device_label) LIKE ? ESCAPE '!' OR LOWER(r.receiver_id) LIKE ? ESCAPE '!' OR LOWER(r.owner_user_id) LIKE ? ESCAPE '!')",
			pattern,
			pattern,
			pattern,
			pattern,
		)
	}
	return query
}

func (s *PostgresStore) ListNotificationSubscriptions(
	ctx context.Context,
	filter notifications.SubscriptionFilter,
) (notifications.SubscriptionPage, error) {
	page := notifications.SubscriptionPage{Receivers: []notifications.ReceiverMetadata{}}
	if err := filter.Validate(); err != nil {
		return page, err
	}
	now := s.now().UTC()
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var counts []struct {
			Status string
			Total  int64
			Users  int64
		}
		if err := notificationScope(tx, filter, now).Select(notificationStatusSQL+" AS status, COUNT(*) AS total, COUNT(DISTINCT r.owner_user_id) AS users", now).Group("status").Scan(&counts).Error; err != nil {
			return err
		}
		for _, count := range counts {
			page.Summary.Total += count.Total
			switch notifications.SubscriptionStatus(count.Status) {
			case notifications.SubscriptionEnabled:
				page.Summary.Enabled = count.Total
				page.Summary.EnabledUsers = count.Users
			case notifications.SubscriptionDisabled:
				page.Summary.Disabled = count.Total
			case notifications.SubscriptionRevoked:
				page.Summary.Revoked = count.Total
			case notifications.SubscriptionExpired:
				page.Summary.Expired = count.Total
			case notifications.SubscriptionInvalidated:
				page.Summary.Invalidated = count.Total
			}
		}
		var rows []struct {
			RecordJSON string
			Email      string
		}
		query := notificationScope(
			tx,
			filter,
			now,
		).Select("r.record_json, u.email").
			Order("r.receiver_id ASC").
			Limit(notificationPageLimit(filter) + 1)
		if filter.After != "" {
			query = query.Where("r.receiver_id > ?", filter.After)
		}
		if err := query.Scan(&rows).Error; err != nil {
			return err
		}
		limit := notificationPageLimit(filter)
		for i, row := range rows {
			if i == limit {
				page.NextCursor = page.Receivers[i-1].ID
				break
			}
			var record notifications.RegistrationRecord
			if err := json.Unmarshal([]byte(row.RecordJSON), &record); err != nil {
				return err
			}
			page.Receivers = append(page.Receivers, record.Metadata(row.Email, now))
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return page, err
}
