package manager

import (
	"context"
	"log/slog"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

type secretaryProgressWorkerConfig struct {
	stepTimeout time.Duration
	wait        func(context.Context, time.Duration) bool
	onError     func(error)
}

func (s *Service) startSecretaryProgressWorker(ctx context.Context, store Store) func() {
	projection, ok := store.(domain.SecretaryProgressProjectionStore)
	if !ok {
		return func() {}
	}
	return startSecretaryProgressProjectionWorker(ctx, projection, secretaryProgressWorkerConfig{
		onError: func(err error) {
			logging.Warn(
				ctx,
				"Secretary progress refresh failed",
				slog.String("error", err.Error()),
			)
		},
	})
}

func startSecretaryProgressProjectionWorker(
	parent context.Context,
	store domain.SecretaryProgressProjectionStore,
	cfg secretaryProgressWorkerConfig,
) func() {
	return startSecretaryMaintenanceWorker(parent, store.AdvanceSecretaryProgress, cfg)
}

func startSecretaryMaintenanceWorker(
	parent context.Context,
	advance func(context.Context, int) (bool, error),
	cfg secretaryProgressWorkerConfig,
) func() {
	if cfg.stepTimeout <= 0 {
		cfg.stepTimeout = 5 * time.Second
	}
	if cfg.wait == nil {
		cfg.wait = waitSecretaryProgressWorker
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSecretaryMaintenanceWorker(ctx, advance, cfg)
	}()
	return func() { cancel(); <-done }
}

func runSecretaryMaintenanceWorker(
	ctx context.Context,
	advance func(context.Context, int) (bool, error),
	cfg secretaryProgressWorkerConfig,
) {
	const pollInterval = 100 * time.Millisecond
	retryDelay := pollInterval
	for ctx.Err() == nil {
		stepCtx, cancel := context.WithTimeout(ctx, cfg.stepTimeout)
		more, err := advance(stepCtx, 128)
		cancel()
		if ctx.Err() != nil {
			return
		}
		delay := pollInterval
		if err != nil {
			if cfg.onError != nil {
				cfg.onError(err)
			}
			delay = retryDelay
			retryDelay = min(2*retryDelay, 10*time.Second)
		} else {
			retryDelay = pollInterval
			if !more {
				delay = time.Second
			}
		}
		// Remaining work never bypasses the bounded polling interval.
		if !cfg.wait(ctx, delay) {
			return
		}
	}
}

func waitSecretaryProgressWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
