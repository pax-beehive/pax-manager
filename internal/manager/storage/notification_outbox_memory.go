package storage

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func (s *MemoryStore) EnqueueNotificationTest(
	_ context.Context,
	owner, id string,
) (notifications.DeliveryMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.notificationReceivers[id]
	if !exists || record.UserID != owner {
		return notifications.DeliveryMetadata{}, notifications.ErrReceiverNotFound
	}
	now := s.now().UTC()
	row, err := newNotificationTestRow(record, now)
	if err != nil {
		return notifications.DeliveryMetadata{}, err
	}
	for _, old := range s.notificationDeliveries {
		var payload notifications.Payload
		_ = json.Unmarshal([]byte(old.PayloadJSON), &payload)
		if old.OwnerUserID == owner && old.CreatedAt.After(now.Add(-30*time.Second)) &&
			payload.Type == "notification.test" {
			return notifications.DeliveryMetadata{}, notifications.ErrTestRateLimited
		}
	}
	if s.notificationDeliveries == nil {
		s.notificationDeliveries = map[string]notificationDeliveryRow{}
	}
	s.notificationDeliveries[row.DeliveryID] = row
	return row.metadata(), nil
}

func (s *MemoryStore) LoadNotificationReceiver(
	_ context.Context,
	owner, id string,
) (notifications.RegistrationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.notificationReceivers[id]
	if !exists || record.UserID != owner {
		return notifications.RegistrationRecord{}, notifications.ErrReceiverNotFound
	}
	return cloneNotificationRecord(record), nil
}

func (s *MemoryStore) ClaimNotificationDeliveries(
	_ context.Context,
	limit int,
	lease time.Duration,
) ([]notifications.LeasedDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := []notifications.LeasedDelivery{}
	if limit < 1 || limit > 100 || lease < 10*time.Second {
		return jobs, errors.New("invalid notification lease")
	}
	now := s.now().UTC()
	var rows []notificationDeliveryRow
	for id, row := range s.notificationDeliveries {
		if row.State != string(notifications.StatePending) {
			continue
		}
		if !now.Before(row.ExpiresAt) {
			row.State = string(notifications.StateExpired)
			row.UpdatedAt = now
			row.LeaseToken = ""
			row.LeaseUntil = nil
			s.notificationDeliveries[id] = row
			continue
		}
		if row.NextAttemptAt.After(now) || (row.LeaseUntil != nil && row.LeaseUntil.After(now)) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].NextAttemptAt.Equal(rows[j].NextAttemptAt) {
			return rows[i].DeliveryID < rows[j].DeliveryID
		}
		return rows[i].NextAttemptAt.Before(rows[j].NextAttemptAt)
	})
	for _, row := range rows {
		if len(jobs) == limit {
			break
		}
		if row.Attempts >= notifications.MaxAttempts {
			row.State = string(notifications.StateFailed)
			row.ErrorCategory = "attempt_limit"
			row.UpdatedAt = now
			row.LeaseToken = ""
			row.LeaseUntil = nil
			s.notificationDeliveries[row.DeliveryID] = row
			continue
		}
		until := now.Add(lease)
		row.LeaseToken, row.LeaseUntil, row.AttemptedAt = rand.Text(), &until, &now
		row.Attempts++
		row.UpdatedAt = now
		job, err := row.leased()
		if err != nil {
			return nil, err
		}
		s.notificationDeliveries[row.DeliveryID] = row
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *MemoryStore) CompleteNotificationDelivery(
	_ context.Context,
	job notifications.LeasedDelivery,
	result notifications.AttemptResult,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, exists := s.notificationDeliveries[job.ID]
	now := s.now().UTC()
	if !exists || row.OwnerUserID != job.UserID ||
		row.State != string(notifications.StatePending) ||
		row.LeaseToken != job.LeaseToken ||
		row.LeaseUntil == nil ||
		!now.Before(*row.LeaseUntil) {
		return nil
	}
	if !result.Suppressed &&
		notifications.ClassifyWebPushStatus(result.Status) == notifications.OutcomeGone {
		record := s.notificationReceivers[row.ReceiverID]
		if record.Revision == row.ReceiverRevision && record.RevokedAt == nil {
			record.Enabled, record.InvalidatedAt = false, &now
			record.Revision++
			record.Subscription = notifications.WebPushSubscription{}
			s.notificationReceivers[row.ReceiverID] = record
		}
	}
	s.notificationDeliveries[row.DeliveryID] = applyNotificationAttempt(row, result, now)
	return nil
}
func (s *MemoryStore) latestNotificationAttempt(id string) *notifications.DeliveryMetadata {
	var selected *notificationDeliveryRow
	for _, row := range s.notificationDeliveries {
		if row.ReceiverID == id &&
			(selected == nil || row.UpdatedAt.After(selected.UpdatedAt) || (row.UpdatedAt.Equal(selected.UpdatedAt) && row.DeliveryID > selected.DeliveryID)) {
			copy := row
			selected = &copy
		}
	}
	if selected == nil {
		return nil
	}
	value := selected.metadata()
	return &value
}
