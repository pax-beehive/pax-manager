package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func TestOutboxGivenCrashRevokeOrRefreshThenStaleAttemptsCannotChangeNewReceiver(t *testing.T) {
	for _, backend := range []string{"memory", "sql", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			ctx := t.Context()
			store, owner, _ := newOutboxFixture(t, backend, func() time.Time { return now })
			_, err := store.LoadNotificationReceiver(ctx, owner, "missing")
			assert.ErrorIs(t, err, notifications.ErrReceiverNotFound)
			for _, limit := range []int{0, 101} {
				_, err = store.ClaimNotificationDeliveries(ctx, limit, 30*time.Second)
				assert.Error(t, err)
			}
			_, err = store.ClaimNotificationDeliveries(ctx, 1, time.Second)
			assert.Error(t, err)
			input := notificationRegistration(t, "recovery")
			receiver, err := store.RegisterNotificationReceiver(ctx, owner, input)
			require.NoError(t, err)
			_, err = store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			require.NoError(t, err)
			jobs, err := store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			first := jobs[0]
			now = now.Add(31 * time.Second)
			require.NoError(
				t,
				store.CompleteNotificationDelivery(
					ctx,
					first,
					notifications.AttemptResult{Status: 201},
				),
			)
			jobs, err = store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			assert.NotEqual(t, first.LeaseToken, jobs[0].LeaseToken)
			assert.Equal(t, 2, jobs[0].Attempts)
			input.VAPIDKeyID = "rotated"
			newer, err := store.RegisterNotificationReceiver(ctx, owner, input)
			require.NoError(t, err)
			assert.Greater(t, newer.Revision, first.ReceiverRevision)
			require.NoError(
				t,
				store.CompleteNotificationDelivery(
					ctx,
					jobs[0],
					notifications.AttemptResult{Status: 410},
				),
			)
			record, err := store.LoadNotificationReceiver(ctx, owner, receiver.ID)
			require.NoError(t, err)
			assert.Nil(t, record.InvalidatedAt)
			assert.True(t, record.Enabled)
			now = now.Add(31 * time.Second)
			_, err = store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			require.NoError(t, err)
			jobs, err = store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			require.NoError(t, store.RevokeNotificationReceiver(ctx, owner, receiver.ID))
			require.NoError(
				t,
				store.CompleteNotificationDelivery(
					ctx,
					jobs[0],
					notifications.AttemptResult{
						Suppressed:    true,
						ErrorCategory: "receiver_changed",
					},
				),
			)
			page, err := store.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{UserID: owner},
			)
			require.NoError(t, err)
			assert.Equal(t, notifications.SubscriptionRevoked, page.Receivers[0].Status)
			assert.Equal(t, "suppressed", page.Receivers[0].LatestDelivery.Status)
			_, err = store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			assert.ErrorIs(t, err, notifications.ErrReceiverDisabled)
			_, err = store.RegisterNotificationReceiver(ctx, owner, input)
			require.NoError(t, err)
			now = now.Add(31 * time.Second)
			_, err = store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			require.NoError(t, err)
			now = now.Add(6 * time.Minute)
			jobs, err = store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			assert.Empty(t, jobs)
			page, err = store.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{UserID: owner},
			)
			require.NoError(t, err)
			assert.Equal(t, "expired", page.Receivers[0].LatestDelivery.Status)
			_, err = store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			require.NoError(t, err)
			for attempt := 1; attempt <= notifications.MaxAttempts; attempt++ {
				jobs, err = store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
				require.NoError(t, err)
				require.Len(t, jobs, 1)
				assert.Equal(t, attempt, jobs[0].Attempts)
				now = now.Add(31 * time.Second)
			}
			jobs, err = store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			assert.Empty(t, jobs)
			page, err = store.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{UserID: owner},
			)
			require.NoError(t, err)
			assert.Equal(t, "failed", page.Receivers[0].LatestDelivery.Status)
		})
	}
}
