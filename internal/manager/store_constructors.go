package manager

import (
	"database/sql"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

type MemoryStore = storage.MemoryStore
type PostgresStore = storage.PostgresStore

func NewMemoryStore(now func() time.Time) *MemoryStore {
	return storage.NewMemoryStore(now)
}

func NewPostgresStore(db *sql.DB, now func() time.Time) *PostgresStore {
	return storage.NewPostgresStore(db, now)
}
