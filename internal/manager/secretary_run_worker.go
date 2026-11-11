package manager

import (
	"context"
	"log/slog"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

func (s *Service) startSecretaryRunWorker(ctx context.Context, store Store) func() {
	queue, ok := store.(domain.SecretaryRunQueueStore)
	if !ok {
		return func() {}
	}
	return startSecretaryMaintenanceWorker(
		ctx,
		queue.AdvanceSecretaryRuns,
		secretaryProgressWorkerConfig{
			onError: func(err error) {
				logging.Warn(
					ctx,
					"Secretary retry queue advance failed",
					slog.String("error", err.Error()),
				)
			},
		},
	)
}
