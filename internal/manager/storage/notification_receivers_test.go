package storage

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
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

func notificationRegistration(t *testing.T, suffix string) notifications.Registration {
	t.Helper()
	secret := make([]byte, 32)
	secret[31] = 1
	key, err := ecdh.P256().NewPrivateKey(secret)
	require.NoError(t, err)
	return notifications.Registration{
		Subscription: notifications.WebPushSubscription{
			Endpoint: "https://fcm.googleapis.com/wp/" + suffix,
			Keys: notifications.WebPushKeys{
				P256DH: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
				Auth:   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
			},
		},
		DeviceLabel: "Pixel", Permission: "granted", Enabled: true, VAPIDKeyID: "dev-key",
	}
}

func TestNotificationRegistryGivenPersistentOrMemoryStoreThenOwnershipPagingAndRevocationAgree(
	t *testing.T,
) {
	for _, backend := range []string{"memory", "sql", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
			var registry notifications.Registry
			var one, two string
			if backend == "memory" {
				store := NewMemoryStore(func() time.Time { return now })
				u1, err := store.EnsureUser(t.Context(), "one@example.invalid", "One", "user")
				require.NoError(t, err)
				u2, err := store.EnsureUser(t.Context(), "two@example.invalid", "Two", "user")
				require.NoError(t, err)
				registry, one, two = store, u1.UserID, u2.UserID
			} else {
				dialector := sqlite.Open(":memory:")
				if backend == "postgres" {
					dsn := os.Getenv("PAX_NOTIFICATIONS_TEST_DATABASE_URL")
					if dsn == "" {
						t.Skip("set PAX_NOTIFICATIONS_TEST_DATABASE_URL to isolated PostgreSQL")
					}
					dialector = postgres.Open(dsn)
				}
				db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				sqlDB.SetMaxOpenConns(1)
				t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
				if backend == "postgres" {
					schemaName := fmt.Sprintf("notification_test_%d", time.Now().UnixNano())
					require.NoError(t, db.Exec("CREATE SCHEMA "+schemaName).Error)
					require.NoError(t, db.Exec("SET search_path TO "+schemaName).Error)
					t.Cleanup(func() { assert.NoError(t, db.Exec("DROP SCHEMA "+schemaName+" CASCADE").Error) })
				}
				require.NoError(t, db.Exec("CREATE TABLE users (user_id TEXT PRIMARY KEY, email TEXT NOT NULL)").Error)
				schema, err := os.ReadFile("../../../db/init.sql")
				require.NoError(t, err)
				ddl := strings.Split(string(schema), "-- Notification receiver registry.")
				require.Len(t, ddl, 2)
				ddlText := ddl[1]
				if backend == "sql" {
					ddlText = strings.ReplaceAll(ddlText, "TIMESTAMPTZ", "DATETIME")
				}
				require.NoError(t, db.Exec(ddlText).Error)
				require.NoError(t, db.Exec("INSERT INTO users VALUES ('one','one@example.invalid'),('two','two@example.invalid')").Error)
				registry, one, two = &PostgresStore{gormDB: db, now: func() time.Time { return now }}, "one", "two"
			}
			ctx := t.Context()
			_, err := registry.RegisterNotificationReceiver(
				ctx,
				"missing",
				notificationRegistration(t, "missing"),
			)
			assert.Error(t, err)
			input := notificationRegistration(t, "first")
			first, err := registry.RegisterNotificationReceiver(ctx, one, input)
			require.NoError(t, err)
			assert.Equal(t, "one@example.invalid", first.Email)
			again, err := registry.RegisterNotificationReceiver(ctx, one, input)
			require.NoError(t, err)
			assert.Equal(t, first.Revision, again.Revision)
			_, err = registry.RegisterNotificationReceiver(ctx, two, input)
			assert.ErrorIs(t, err, notifications.ErrReceiverConflict)
			second, err := registry.RegisterNotificationReceiver(
				ctx,
				two,
				notificationRegistration(t, "second"),
			)
			require.NoError(t, err)
			page, err := registry.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{Limit: 1},
			)
			require.NoError(t, err)
			require.Len(t, page.Receivers, 1)
			assert.Equal(t, int64(2), page.Summary.Total)
			assert.Equal(t, int64(2), page.Summary.EnabledUsers)
			require.NotEmpty(t, page.NextCursor)
			next, err := registry.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{Limit: 1, After: page.NextCursor},
			)
			require.NoError(t, err)
			require.Len(t, next.Receivers, 1)
			assert.NotEqual(t, page.Receivers[0].ID, next.Receivers[0].ID)
			assert.Empty(t, next.NextCursor)
			assert.Equal(t, int64(2), next.Summary.Total)
			assert.ErrorIs(
				t,
				registry.RevokeNotificationReceiver(ctx, one, second.ID),
				notifications.ErrReceiverNotFound,
			)
			require.NoError(t, registry.RevokeNotificationReceiver(ctx, one, first.ID))
			require.NoError(t, registry.RevokeNotificationReceiver(ctx, one, first.ID))
			revoked, err := registry.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{Status: notifications.SubscriptionRevoked},
			)
			require.NoError(t, err)
			require.Len(t, revoked.Receivers, 1)
			assert.Equal(t, first.Revision+1, revoked.Receivers[0].Revision)
			restored, err := registry.RegisterNotificationReceiver(ctx, one, input)
			require.NoError(t, err)
			assert.Equal(t, first.Revision+2, restored.Revision)
			input.Enabled = false
			_, err = registry.RegisterNotificationReceiver(ctx, one, input)
			require.NoError(t, err)
			for _, filter := range []notifications.SubscriptionFilter{{UserID: one}, {Search: "ONE@EXAMPLE"}, {Status: notifications.SubscriptionDisabled}, {Search: first.ID}} {
				filtered, err := registry.ListNotificationSubscriptions(ctx, filter)
				require.NoError(t, err)
				require.Len(t, filtered.Receivers, 1)
				assert.Equal(t, first.ID, filtered.Receivers[0].ID)
			}
			empty, err := registry.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{Search: "%_!"},
			)
			require.NoError(t, err)
			assert.Empty(t, empty.Receivers)
			_, err = registry.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{Status: "online"},
			)
			assert.ErrorIs(t, err, notifications.ErrInvalidFilter)
			input.Subscription.Endpoint = "invalid"
			_, err = registry.RegisterNotificationReceiver(ctx, one, input)
			assert.ErrorIs(t, err, notifications.ErrInvalidRegistration)
			for i := 1; i < notifications.MaxReceiversPerUser; i++ {
				_, err = registry.RegisterNotificationReceiver(
					ctx,
					one,
					notificationRegistration(t, fmt.Sprint("extra-", i)),
				)
				require.NoError(t, err)
			}
			_, err = registry.RegisterNotificationReceiver(
				ctx,
				one,
				notificationRegistration(t, "over-limit"),
			)
			assert.ErrorIs(t, err, notifications.ErrReceiverLimit)
			all, err := registry.ListNotificationSubscriptions(
				ctx,
				notifications.SubscriptionFilter{},
			)
			require.NoError(t, err)
			encoded, err := json.Marshal(all)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "fcm.googleapis.com")
			assert.NotContains(t, string(encoded), "p256dh")
		})
	}
}
