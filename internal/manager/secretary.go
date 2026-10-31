package manager

import (
	"context"
	"log/slog"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func (s *Service) configureSecretary(store Store) {
	s.secretaryStore, _ = store.(domain.SecretaryStore)
	s.secretary = userapi.NewSecretaryService(
		s.auth,
		s.secretaryStore,
		s.store,
		s.secretaryAgentAvailable,
	)
}

func (s *Service) startSecretaryWorker(ctx context.Context) func() {
	if s.secretaryStore == nil {
		return func() {}
	}
	worker := secretary.NewWorker(s.secretaryStore, s.secretaryExecutor(), secretary.WorkerConfig{
		OnError: func(err error) {
			logging.Warn(ctx, "Secretary background task failed", slog.String("error", err.Error()))
		},
	})
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

func (s *Service) secretaryAgentAvailable(ctx context.Context, owner, agentID string) bool {
	principal := domain.UserPrincipal{User: domain.User{UserID: owner}}
	agent, err := s.store.GetAgent(ctx, principal, agentID)
	if err != nil || agent.OwnerUserID != owner || s.acpTunnels == nil {
		return false
	}
	s.acpTunnels.mu.RLock()
	defer s.acpTunnels.mu.RUnlock()
	for key, conn := range s.acpTunnels.agents {
		if key.agentID == agentID && conn != nil {
			return true
		}
	}
	return false
}
