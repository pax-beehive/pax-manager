package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage/dal/query"
)

type regionalTestStore interface {
	EnsureRegionalUser(context.Context, string, string, string) (domain.User, error)
}

func TestRegionalUserGivenAssignmentsThenRetriesPreserveIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(
		t,
		db.Exec(
			`CREATE TABLE users (user_id TEXT PRIMARY KEY, email TEXT UNIQUE NOT NULL, display_name TEXT NOT NULL DEFAULT '', role TEXT, created_at DATETIME, last_seen_at DATETIME)`,
		).Error,
	)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	stores := map[string]regionalTestStore{
		"memory": NewMemoryStore(time.Now),
		"sql":    &PostgresStore{gormDB: db, q: query.Use(db), now: time.Now},
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			user, err := store.EnsureRegionalUser(
				t.Context(),
				"usr_global",
				"Owner@Example.com",
				"user",
			)
			require.NoError(t, err)
			assert.Equal(t, "owner@example.com", user.Email)
			again, err := store.EnsureRegionalUser(
				t.Context(),
				"usr_global",
				"owner@example.com",
				"admin",
			)
			require.NoError(t, err)
			assert.Equal(
				t,
				user,
				again,
				"replay does not change identity, timestamps or permissions",
			)
			for _, input := range [][2]string{{"usr_other", "owner@example.com"}, {"usr_global", "other@example.com"}} {
				_, err = store.EnsureRegionalUser(t.Context(), input[0], input[1], "user")
				assert.ErrorIs(t, err, domain.ErrConflict)
			}
			for _, input := range [][2]string{{"", "owner@example.com"}, {"usr_x", ""}} {
				_, err = store.EnsureRegionalUser(t.Context(), input[0], input[1], "user")
				assert.ErrorIs(t, err, domain.ErrUnauthorized)
			}
		})
	}
	require.NoError(t, db.Exec("DROP TABLE users").Error)
	_, err = stores["sql"].EnsureRegionalUser(t.Context(), "usr_new", "new@example.com", "user")
	require.Error(t, err)
}
