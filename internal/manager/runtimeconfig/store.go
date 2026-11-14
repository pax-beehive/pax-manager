package runtimeconfig

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

// DefaultRefreshInterval is how often each instance polls the revision.
const DefaultRefreshInterval = 15 * time.Second

const loadTimeout = 5 * time.Second

type Options struct {
	// Repository is optional; without it only environment values and
	// defaults apply and the admin API reports writes as unavailable.
	Repository      domain.RuntimeConfigRepository
	Env             EnvLookup
	Region          string
	RefreshInterval time.Duration
	Now             func() time.Time
	Registry        []Entry
}

// Health is reported by /api/v1/health and the admin API.
type Health struct {
	LastRefreshAt   *time.Time `json:"last_refresh_at"`
	RefreshFailures int64      `json:"refresh_failures"`
	Degraded        bool       `json:"degraded"`
}

// Store serves lock-free snapshots and refreshes them in the background.
type Store struct {
	opts      Options
	startedAt time.Time
	snapshot  atomic.Pointer[Resolved]
	refreshMu sync.Mutex

	healthMu        sync.Mutex
	loaded          bool
	lastErr         error
	lastRefreshAt   time.Time
	refreshFailures int64
}

// New builds the initial snapshot from environment values and defaults. It
// never touches the database; call Start or Refresh to load overrides.
func New(opts Options) *Store {
	if opts.Registry == nil {
		opts.Registry = DefaultRegistry()
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = DefaultRefreshInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Store{opts: opts, startedAt: opts.Now()}
	initial, _ := resolve(opts.Registry, opts.Env, nil, 0)
	s.snapshot.Store(initial)
	if opts.Repository == nil {
		s.loaded = true
	}
	return s
}

var defaultStore = New(Options{})

// Snapshot returns the current snapshot. A nil store serves defaults only.
func (s *Store) Snapshot() *Resolved {
	if s == nil {
		return defaultStore.Snapshot()
	}
	return s.snapshot.Load()
}

func (s *Store) Feature(p domain.UserPrincipal, key string) bool {
	return s.Snapshot().Enabled(p, key)
}

func (s *Store) Search() SearchConfig { return s.Snapshot().Search() }

func (s *Store) Voice() VoiceConfig { return s.Snapshot().Voice() }

// Start attempts one bounded load, then polls the revision until ctx ends.
// A failed load never blocks startup. The returned function joins the poller.
func (s *Store) Start(ctx context.Context) func() {
	if s == nil || s.opts.Repository == nil {
		return func() {}
	}
	if err := s.Refresh(ctx); err != nil {
		logging.Error(ctx, "runtime config load failed; serving environment values and defaults",
			logging.Err(err))
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.opts.RefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
					logging.Warn(ctx, "runtime config refresh failed; keeping last snapshot",
						logging.Err(err))
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Refresh reloads overrides when the revision changed. On failure the
// last-known-good snapshot stays in place.
func (s *Store) Refresh(ctx context.Context) error {
	if s == nil || s.opts.Repository == nil {
		return nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	err := s.refreshLocked(ctx)
	s.recordRefresh(err)
	return err
}

func (s *Store) refreshLocked(ctx context.Context) error {
	revision, err := s.opts.Repository.RuntimeConfigRevision(ctx)
	if err != nil {
		return err
	}
	s.healthMu.Lock()
	loaded := s.loaded
	s.healthMu.Unlock()
	if loaded && revision == s.snapshot.Load().Revision {
		return nil
	}
	revision, rows, err := s.opts.Repository.LoadRuntimeConfig(ctx)
	if err != nil {
		return err
	}
	next, skipped := resolve(s.opts.Registry, s.opts.Env, rows, revision)
	for _, row := range skipped {
		logging.Error(ctx, "runtime config override skipped",
			slog.String("config_key", row.Key), slog.String("reason", row.Reason))
	}
	s.snapshot.Store(next)
	return nil
}

func (s *Store) recordRefresh(err error) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.lastErr = err
	if err != nil {
		s.refreshFailures++
		return
	}
	s.loaded = true
	s.lastRefreshAt = s.opts.Now().UTC()
}

func (s *Store) Health() Health {
	if s == nil {
		return Health{}
	}
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	health := Health{
		RefreshFailures: s.refreshFailures,
		Degraded:        !s.loaded || s.lastErr != nil,
	}
	if !s.lastRefreshAt.IsZero() {
		at := s.lastRefreshAt
		health.LastRefreshAt = &at
	}
	return health
}
