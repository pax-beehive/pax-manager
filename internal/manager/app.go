package manager

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	managerconfig "github.com/pax-beehive/pax-manager/internal/manager/config"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

type Config = managerconfig.Config

func Run(ctx context.Context) error {
	cfg := loadConfig()

	store, closeStore, err := openStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore()

	srv := newServer(cfg, store)
	addr := ":" + cfg.Port

	log.Printf("pax-manager listening on http://localhost%s", addr)
	log.Printf("agent api: http://localhost%s/api/agent/*", addr)
	log.Printf("user api:  http://localhost%s/api/user/*", addr)

	return srv.engine(addr).Run()
}

func openStore(ctx context.Context, databaseURL string) (Store, func(), error) {
	if databaseURL == "" {
		log.Printf("DATABASE_URL is empty; using in-memory storage")
		return storage.NewMemoryStore(time.Now), func() {}, nil
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	store := storage.NewPostgresStore(db, time.Now)
	if err := store.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	return store, func() { _ = db.Close() }, nil
}

func loadConfig() Config {
	return managerconfig.Load()
}

func parseEmailSet(raw string) map[string]bool {
	return managerconfig.ParseEmailSet(raw)
}
