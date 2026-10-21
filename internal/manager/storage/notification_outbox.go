package storage

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

type notificationDeliveryRow struct {
	DeliveryID       string `gorm:"primaryKey"`
	NotificationID   string
	OwnerUserID      string
	ReceiverID       string
	ReceiverRevision int64
	State            string
	Attempts         int
	NextAttemptAt    time.Time
	ExpiresAt        time.Time
	CreatedAt        time.Time `gorm:"autoCreateTime:false"`
	UpdatedAt        time.Time `gorm:"autoUpdateTime:false"`
	AttemptedAt      *time.Time
	LeaseToken       string
	LeaseUntil       *time.Time
	ProviderStatus   int
	ErrorCategory    string
	PayloadJSON      string
}

func (notificationDeliveryRow) TableName() string { return "notification_deliveries" }
func (r notificationDeliveryRow) metadata() notifications.DeliveryMetadata {
	value := notifications.DeliveryMetadata{
		Status:         r.State,
		Attempts:       r.Attempts,
		ProviderStatus: r.ProviderStatus,
		ErrorCategory:  r.ErrorCategory,
	}
	if r.AttemptedAt != nil {
		value.AttemptedAt = *r.AttemptedAt
	}
	if r.State == string(notifications.StatePending) {
		value.NextAttemptAt = &r.NextAttemptAt
	}
	return value
}
func (r notificationDeliveryRow) leased() (notifications.LeasedDelivery, error) {
	job := notifications.LeasedDelivery{Delivery: notifications.Delivery{
		ID: r.DeliveryID, NotificationID: r.NotificationID, UserID: r.OwnerUserID, ReceiverID: r.ReceiverID,
		ReceiverRevision: r.ReceiverRevision, Channel: notifications.ChannelWebPush, State: notifications.DeliveryState(r.State), NextAttemptAt: r.NextAttemptAt, ExpiresAt: r.ExpiresAt,
	}, Attempts: r.Attempts, LeaseToken: r.LeaseToken}
	err := json.Unmarshal([]byte(r.PayloadJSON), &job.Payload)
	return job, err
}

func newNotificationTestRow(
	record notifications.RegistrationRecord,
	now time.Time,
) (notificationDeliveryRow, error) {
	notification := notifications.BuildTestNotification(record.UserID, rand.Text(), now)
	deliveries := notifications.PlanDeliveries(
		notification,
		[]notifications.Receiver{record.Receiver},
		now,
	)
	if len(deliveries) == 0 || record.RevokedAt != nil || record.InvalidatedAt != nil {
		return notificationDeliveryRow{}, notifications.ErrReceiverDisabled
	}
	delivery := deliveries[0]
	payload, _ := json.Marshal(notification.Payload)
	return notificationDeliveryRow{
		DeliveryID:       delivery.ID,
		NotificationID:   notification.ID,
		OwnerUserID:      record.UserID,
		ReceiverID:       record.ID,
		ReceiverRevision: record.Revision,
		State: string(
			notifications.StatePending,
		),
		NextAttemptAt: now.UTC(),
		ExpiresAt:     notification.ExpiresAt,
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
		PayloadJSON:   string(payload),
	}, nil
}

func applyNotificationAttempt(
	row notificationDeliveryRow,
	result notifications.AttemptResult,
	now time.Time,
) notificationDeliveryRow {
	decision := notifications.DecideDelivery(
		notifications.ClassifyWebPushStatus(result.Status),
		row.Attempts,
		now,
		row.ExpiresAt,
		result.RetryAfter,
	)
	row.State = string(decision.State)
	if result.Suppressed {
		row.State = string(notifications.StateSuppressed)
	}
	row.NextAttemptAt = decision.NextAttemptAt
	row.ProviderStatus, row.ErrorCategory = result.Status, result.ErrorCategory
	row.UpdatedAt, row.LeaseToken, row.LeaseUntil = now.UTC(), "", nil
	return row
}
func lockNotificationOwner(tx *gorm.DB, owner string) error {
	var user struct{ UserID string }
	err := tx.Table("users").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", owner).
		Take(&user).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notifications.ErrReceiverNotFound
	}
	return err
}
func notificationRecord(tx *gorm.DB, owner, id string) (notifications.RegistrationRecord, error) {
	var row notificationReceiverRow
	if err := tx.Where("receiver_id = ? AND owner_user_id = ?", id, owner).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return notifications.RegistrationRecord{}, notifications.ErrReceiverNotFound
		}
		return notifications.RegistrationRecord{}, err
	}
	var record notifications.RegistrationRecord
	err := json.Unmarshal([]byte(row.RecordJSON), &record)
	return record, err
}

func (s *PostgresStore) EnqueueNotificationTest(
	ctx context.Context,
	owner, id string,
) (notifications.DeliveryMetadata, error) {
	var metadata notifications.DeliveryMetadata
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockNotificationOwner(tx, owner); err != nil {
			return err
		}
		record, err := notificationRecord(tx, owner, id)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		row, err := newNotificationTestRow(record, now)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&notificationDeliveryRow{}).Where("owner_user_id = ? AND created_at > ?", owner, now.Add(-30*time.Second)).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return notifications.ErrTestRateLimited
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		metadata = row.metadata()
		return nil
	})
	return metadata, err
}

func (s *PostgresStore) LoadNotificationReceiver(
	ctx context.Context,
	owner, id string,
) (notifications.RegistrationRecord, error) {
	return notificationRecord(s.gormDB.WithContext(ctx), owner, id)
}

func (s *PostgresStore) ClaimNotificationDeliveries(
	ctx context.Context,
	limit int,
	lease time.Duration,
) ([]notifications.LeasedDelivery, error) {
	jobs := []notifications.LeasedDelivery{}
	if limit < 1 || limit > 100 || lease < 10*time.Second {
		return jobs, errors.New("invalid notification lease")
	}
	now := s.now().UTC()
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&notificationDeliveryRow{}).Where("state = ? AND expires_at <= ?", notifications.StatePending, now).Updates(map[string]any{"state": notifications.StateExpired, "updated_at": now, "lease_token": "", "lease_until": nil}).Error; err != nil {
			return err
		}
		var rows []notificationDeliveryRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state = ? AND next_attempt_at <= ? AND (lease_until IS NULL OR lease_until <= ?)", notifications.StatePending, now, now).Order("next_attempt_at, delivery_id").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.Attempts >= notifications.MaxAttempts {
				if err := tx.Model(&row).Updates(map[string]any{"state": notifications.StateFailed, "error_category": "attempt_limit", "updated_at": now, "lease_token": "", "lease_until": nil}).Error; err != nil {
					return err
				}
				continue
			}
			until := now.Add(lease)
			row.LeaseToken, row.LeaseUntil, row.AttemptedAt = rand.Text(), &until, &now
			row.Attempts++
			row.UpdatedAt = now
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
			job, err := row.leased()
			if err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return nil
	})
	return jobs, err
}

func (s *PostgresStore) CompleteNotificationDelivery(
	ctx context.Context,
	job notifications.LeasedDelivery,
	result notifications.AttemptResult,
) error {
	return s.gormDB.WithContext(ctx).
		Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).
		Transaction(func(tx *gorm.DB) error {
			if err := lockNotificationOwner(tx, job.UserID); err != nil {
				return err
			}
			var row notificationDeliveryRow
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("delivery_id = ? AND owner_user_id = ? AND state = ? AND lease_token = ? AND lease_until > ?", job.ID, job.UserID, notifications.StatePending, job.LeaseToken, s.now()).
				Take(&row).
				Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			now := s.now().UTC()
			if !result.Suppressed &&
				notifications.ClassifyWebPushStatus(result.Status) == notifications.OutcomeGone {
				record, err := notificationRecord(tx, row.OwnerUserID, row.ReceiverID)
				if err != nil {
					return err
				}
				if record.Revision == row.ReceiverRevision && record.RevokedAt == nil {
					record.Enabled, record.InvalidatedAt = false, &now
					record.Revision++
					record.Subscription = notifications.WebPushSubscription{}
					if err := tx.Save(ptrNotificationRow(record)).Error; err != nil {
						return err
					}
				}
			}
			row = applyNotificationAttempt(row, result, now)
			return tx.Save(&row).Error
		})
}
func ptrNotificationRow(record notifications.RegistrationRecord) *notificationReceiverRow {
	row := notificationRow(record)
	return &row
}

func latestNotificationAttempts(
	tx *gorm.DB,
	ids []string,
) (map[string]notifications.DeliveryMetadata, error) {
	latest := map[string]notifications.DeliveryMetadata{}
	if len(ids) == 0 {
		return latest, nil
	}
	var rows []notificationDeliveryRow
	err := tx.Table("notification_deliveries d").
		Where("d.receiver_id IN ?", ids).
		Where(`NOT EXISTS (SELECT 1 FROM notification_deliveries newer WHERE newer.receiver_id = d.receiver_id AND (newer.updated_at > d.updated_at OR (newer.updated_at = d.updated_at AND newer.delivery_id > d.delivery_id)))`).
		Find(&rows).
		Error
	for _, row := range rows {
		latest[row.ReceiverID] = row.metadata()
	}
	return latest, err
}
