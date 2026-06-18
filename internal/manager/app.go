package manager

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	managerconfig "github.com/pax-beehive/pax-manager/internal/manager/config"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
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

	logging.Info(ctx, "pax-manager listening", slog.String("addr", "http://localhost"+addr))
	logging.Info(ctx, "agent api ready", slog.String("url", "http://localhost"+addr+"/api/agent/*"))
	logging.Info(ctx, "user api ready", slog.String("url", "http://localhost"+addr+"/api/user/*"))

	return srv.engine(addr).Run()
}

func openStore(ctx context.Context, databaseURL string) (Store, func(), error) {
	if databaseURL == "" {
		logging.Warn(ctx, "DATABASE_URL is empty; using in-memory storage")
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
