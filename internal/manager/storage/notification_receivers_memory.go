package storage

import (
	"context"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func (s *MemoryStore) RegisterNotificationReceiver(
	_ context.Context,
	owner string,
	input notifications.Registration,
) (notifications.ReceiverMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, exists := s.users[owner]
	if !exists {
		return notifications.ReceiverMetadata{}, notifications.ErrReceiverNotFound
	}
	if s.notificationReceivers == nil {
		s.notificationReceivers = make(map[string]notifications.RegistrationRecord)
	}
	id := notifications.ReceiverID(input.Subscription.Endpoint)
	old := s.notificationReceivers[id]
	record, err := notifications.RefreshRegistration(old, owner, input, s.now())
	if err != nil {
		return notifications.ReceiverMetadata{}, err
	}
	if old.ID == "" || old.RevokedAt != nil {
		count := 0
		for _, receiver := range s.notificationReceivers {
			if receiver.UserID == owner && receiver.RevokedAt == nil {
				count++
			}
		}
		if count >= notifications.MaxReceiversPerUser {
			return notifications.ReceiverMetadata{}, notifications.ErrReceiverLimit
		}
	}
	s.notificationReceivers[id] = cloneNotificationRecord(record)
	return record.Metadata(user.Email, s.now()), nil
}

func (s *MemoryStore) RevokeNotificationReceiver(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.notificationReceivers[id]
	if !exists || record.UserID != owner {
		return notifications.ErrReceiverNotFound
	}
	if record.RevokedAt == nil {
		now := s.now().UTC()
		record.RevokedAt, record.Enabled = &now, false
		record.Subscription = notifications.WebPushSubscription{}
		record.Revision++
		s.notificationReceivers[id] = record
	}
	return nil
}

func (s *MemoryStore) ListNotificationSubscriptions(
	_ context.Context,
	filter notifications.SubscriptionFilter,
) (notifications.SubscriptionPage, error) {
	page := notifications.SubscriptionPage{Receivers: []notifications.ReceiverMetadata{}}
	if err := filter.Validate(); err != nil {
		return page, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]notifications.ReceiverMetadata, 0)
	enabledUsers := make(map[string]bool)
	search := strings.ToLower(strings.TrimSpace(filter.Search))
	for _, record := range s.notificationReceivers {
		view := record.Metadata(s.users[record.UserID].Email, s.now())
		if filter.UserID != "" && view.UserID != filter.UserID ||
			filter.Status != "" && view.Status != filter.Status ||
			filter.Channel != "" && view.Channel != filter.Channel {
			continue
		}
		if search != "" &&
			!strings.Contains(
				strings.ToLower(view.Email+" "+view.DeviceLabel+" "+view.ID+" "+view.UserID),
				search,
			) {
			continue
		}
		page.Summary.Total++
		switch view.Status {
		case notifications.SubscriptionEnabled:
			page.Summary.Enabled++
			enabledUsers[view.UserID] = true
		case notifications.SubscriptionDisabled:
			page.Summary.Disabled++
		case notifications.SubscriptionRevoked:
			page.Summary.Revoked++
		case notifications.SubscriptionExpired:
			page.Summary.Expired++
		case notifications.SubscriptionInvalidated:
			page.Summary.Invalidated++
		}
		if view.ID > filter.After {
			rows = append(rows, view)
		}
	}
	page.Summary.EnabledUsers = int64(len(enabledUsers))
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	limit := notificationPageLimit(filter)
	if len(rows) > limit {
		page.NextCursor = rows[limit-1].ID
		rows = rows[:limit]
	}
	page.Receivers = rows
	return page, nil
}

func notificationPageLimit(filter notifications.SubscriptionFilter) int {
	if filter.Limit == 0 {
		return 50
	}
	return filter.Limit
}

func cloneNotificationRecord(
	record notifications.RegistrationRecord,
) notifications.RegistrationRecord {
	if record.ExpiresAt != nil {
		value := *record.ExpiresAt
		record.ExpiresAt = &value
	}
	return record
}
