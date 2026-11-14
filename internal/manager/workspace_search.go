package manager

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/search"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func (s *Service) configureWorkspaceSearch(store Store) {
	sources, _ := store.(domain.SearchSourceStore)
	summaries, _ := store.(domain.SummaryStore)
	key := []byte(s.cfg.SearchReadReceiptSecret)
	if len(key) < 32 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			panic(err)
		}
	}
	// The backend reads search settings on every call, so admin changes
	// apply without a restart. Unconfigured search reports unavailable.
	if s.searchBackend == nil {
		s.searchBackend = newRuntimeSearchBackend(s)
	}
	s.workspaceSearch = userapi.NewWorkspaceSearchService(
		s.auth,
		sources,
		summaries,
		s.searchBackend,
		key,
	)
}

func (s *Service) startWorkspaceSearchWorker(ctx context.Context, store Store) func() {
	queue, ok := store.(domain.SearchQueueStore)
	if !ok || s.searchBackend == nil {
		return func() {}
	}
	worker := search.NewWorker(
		queue,
		s.searchBackend,
		search.WorkerConfig{OnError: func(err error) {
			if errors.Is(err, domain.ErrSearchUnavailable) {
				return
			}
			logging.Warn(
				ctx,
				"workspace search synchronization failed",
				slog.String("error", err.Error()),
			)
		}},
	)
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = worker.Run(workerCtx) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}
}
