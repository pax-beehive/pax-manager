package manager

import (
	"context"
	"crypto/rand"
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
	if s.cfg.SearchURL != "" {
		index := s.cfg.SearchIndex
		if index == "" {
			index = "pax-search-v1"
		}
		client, err := search.NewClient(
			search.Config{URL: s.cfg.SearchURL, Index: index, APIKey: s.cfg.SearchAPIKey},
		)
		if err != nil {
			logging.Error(
				context.Background(),
				"workspace search configuration failed",
				slog.String("error", err.Error()),
			)
		} else {
			s.searchBackend = client
		}
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
