package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	cfg := loadConfig()

	store, closeStore, err := openStore(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer closeStore()

	srv := newServer(cfg, store)
	addr := ":" + cfg.Port

	log.Printf("pax-manager listening on http://localhost%s", addr)
	log.Printf("agent api: http://localhost%s/api/agent/*", addr)
	log.Printf("user api:  http://localhost%s/api/user/*", addr)

	if err := http.ListenAndServe(addr, srv.routes()); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func openStore(ctx context.Context, databaseURL string) (Store, func(), error) {
	if databaseURL == "" {
		log.Printf("DATABASE_URL is empty; using in-memory storage")
		return NewMemoryStore(time.Now), func() {}, nil
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, nil, err
	}

	store := NewPostgresStore(db, time.Now)
	if err := store.EnsureSchema(ctx); err != nil {
		db.Close()
		return nil, nil, err
	}

	return store, func() { _ = db.Close() }, nil
}

type Config struct {
	Port                   string
	DatabaseURL            string
	RegistrationToken      string
	RegistrationOwnerEmail string
	LocalUserID            string
	AllowLocalUserHeader   bool
	AdminEmails            map[string]bool
}

func loadConfig() Config {
	cfg := Config{
		Port:                   envDefault("PORT", "9879"),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		RegistrationToken:      os.Getenv("REGISTRATION_TOKEN"),
		RegistrationOwnerEmail: os.Getenv("REGISTRATION_TOKEN_OWNER_EMAIL"),
		LocalUserID:            envDefault("LOCAL_USER_ID", "local@example.local"),
		AllowLocalUserHeader:   parseBool(os.Getenv("ALLOW_LOCAL_USER_HEADER")),
		AdminEmails:            parseEmailSet(os.Getenv("ADMIN_EMAILS")),
	}
	return cfg
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseEmailSet(raw string) map[string]bool {
	out := mergeAdminEmails(nil)
	for _, part := range strings.Split(raw, ",") {
		email := strings.ToLower(strings.TrimSpace(part))
		if email != "" {
			out[email] = true
		}
	}
	return out
}

func mergeAdminEmails(extra map[string]bool) map[string]bool {
	out := defaultAdminEmails()
	for email, enabled := range extra {
		if enabled {
			out[normalizeEmail(email)] = true
		}
	}
	return out
}

func defaultAdminEmails() map[string]bool {
	return map[string]bool{
		"toddzheng024@gmail.com":      true,
		"gengcongkai456789@gmail.com": true,
		"zhangjiahang0725@gmail.com":  true,
	}
}

func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
