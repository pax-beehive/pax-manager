package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

type notificationTestStore interface {
	notifications.Registry
	notifications.Outbox
}

func newOutboxFixture(
	t *testing.T,
	backend string,
	now func() time.Time,
) (notificationTestStore, string, string) {
	t.Helper()
	if backend == "memory" {
		s := NewMemoryStore(now)
		a, err := s.EnsureUser(t.Context(), "a@example.invalid", "A", "user")
		require.NoError(t, err)
		b, err := s.EnsureUser(t.Context(), "b@example.invalid", "B", "user")
		require.NoError(t, err)
		return s, a.UserID, b.UserID
	}
	dialector := sqlite.Open(":memory:")
	if backend == "postgres" {
		dsn := os.Getenv("PAX_NOTIFICATIONS_TEST_DATABASE_URL")
		if dsn == "" {
			t.Skip("set isolated PostgreSQL URL")
		}
		dialector = postgres.Open(dsn)
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { assert.NoError(t, conn.Close()) })
	if backend == "postgres" {
		schema := fmt.Sprintf("push_outbox_%d", time.Now().UnixNano())
		require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
		require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
		t.Cleanup(func() { assert.NoError(t, db.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })
	}
	require.NoError(
		t,
		db.Exec("CREATE TABLE users (user_id TEXT PRIMARY KEY, email TEXT NOT NULL)").Error,
	)
	require.NoError(
		t,
		db.Exec(
			"INSERT INTO users VALUES ('a','a@example.invalid'),('b','b@example.invalid')",
		).Error,
	)
	ddl, err := os.ReadFile("../../../db/init.sql")
	require.NoError(t, err)
	sql := strings.Split(string(ddl), "-- Notification receiver registry.")[1]
	if backend == "sql" {
		sql = strings.ReplaceAll(sql, "TIMESTAMPTZ", "DATETIME")
	}
	require.NoError(t, db.Exec(sql).Error)
	return &PostgresStore{gormDB: db, now: now}, "a", "b"
}

func TestOutboxGivenQueuedTestThenLeaseRetryAndMetadataAreDurableAndOwnerScoped(t *testing.T) {
	for _, backend := range []string{"memory", "sql", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			store, owner, other := newOutboxFixture(t, backend, func() time.Time { return now })
			ctx := context.Background()
			input := notificationRegistration(t, "outbox")
			receiver, err := store.RegisterNotificationReceiver(ctx, owner, input)
			require.NoError(t, err)
			_, err = store.EnqueueNotificationTest(ctx, other, receiver.ID)
			assert.ErrorIs(t, err, notifications.ErrReceiverNotFound)
			queued, err := store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			require.NoError(t, err)
			assert.Equal(t, "pending", queued.Status)
			_, err = store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			assert.ErrorIs(t, err, notifications.ErrTestRateLimited)
			jobs, err := store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			assert.Equal(t, "notification.test", jobs[0].Payload.Type)
			assert.Equal(t, 1, jobs[0].Attempts)
			assert.NotEmpty(t, jobs[0].LeaseToken)
			empty, err := store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			assert.Empty(t, empty)
			stale := jobs[0]
			stale.LeaseToken = "wrong"
			require.NoError(
				t,
				store.CompleteNotificationDelivery(
					ctx,
					stale,
					notifications.AttemptResult{Status: 201},
				),
			)
			require.NoError(
				t,
				store.CompleteNotificationDelivery(
					ctx,
					jobs[0],
					notifications.AttemptResult{
						Status:        503,
						RetryAfter:    20 * time.Second,
						ErrorCategory: "provider_rejected",
					},
				),
			)
			empty, err = store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			assert.Empty(t, empty)
			now = now.Add(21 * time.Second)
			jobs, err = store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			assert.Equal(t, 2, jobs[0].Attempts)
			require.NoError(
				t,
				store.CompleteNotificationDelivery(
					ctx,
					jobs[0],
					notifications.AttemptResult{Status: 201},
				),
			)
			page, err := store.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{UserID: owner},
			)
			require.NoError(t, err)
			require.NotNil(t, page.Receivers[0].LatestDelivery)
			assert.Equal(t, "accepted", page.Receivers[0].LatestDelivery.Status)
			assert.Equal(t, 2, page.Receivers[0].LatestDelivery.Attempts)
			record, err := store.LoadNotificationReceiver(ctx, owner, receiver.ID)
			require.NoError(t, err)
			assert.Equal(t, input.Subscription.Endpoint, record.Subscription.Endpoint)
			_, err = store.LoadNotificationReceiver(ctx, other, receiver.ID)
			assert.ErrorIs(t, err, notifications.ErrReceiverNotFound)
			now = now.Add(31 * time.Second)
			_, err = store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			require.NoError(t, err)
			jobs, err = store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			require.NoError(
				t,
				store.CompleteNotificationDelivery(
					ctx,
					jobs[0],
					notifications.AttemptResult{Status: 410},
				),
			)
			page, err = store.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{UserID: owner},
			)
			require.NoError(t, err)
			assert.Equal(t, notifications.SubscriptionInvalidated, page.Receivers[0].Status)
			_, err = store.EnqueueNotificationTest(ctx, owner, receiver.ID)
			assert.ErrorIs(t, err, notifications.ErrReceiverDisabled)
		})
	}
}
