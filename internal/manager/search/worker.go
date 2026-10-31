package search

import (
	"context"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type WorkerConfig struct {
	BatchSize     int
	LeaseDuration time.Duration
	PollInterval  time.Duration
	RetryInterval time.Duration
	OnError       func(error)
}

type Worker struct {
	queue   domain.SearchQueueStore
	backend domain.SearchBackend
	cfg     WorkerConfig
}

func NewWorker(
	queue domain.SearchQueueStore,
	backend domain.SearchBackend,
	cfg WorkerConfig,
) *Worker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 5 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = 5 * time.Second
	}
	return &Worker{queue: queue, backend: backend, cfg: cfg}
}

// Run initializes and retries in the background; callers start it in a goroutine.
func (w *Worker) Run(ctx context.Context) error {
	initialized := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		if !initialized {
			err = w.backend.EnsureIndex(ctx)
			if err == nil {
				err = w.queue.BackfillSearch(ctx)
			}
			initialized = err == nil
		}
		if err == nil {
			err = w.ProcessOnce(ctx)
		}
		delay := w.cfg.PollInterval
		if err != nil {
			if w.cfg.OnError != nil {
				w.cfg.OnError(err)
			}
			delay = w.cfg.RetryInterval
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *Worker) ProcessOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batch, err := w.queue.ClaimSearchBatch(ctx, w.cfg.BatchSize, w.cfg.LeaseDuration)
	if err != nil {
		return err
	}
	var failures []error
	for _, work := range batch {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		syncErr := w.backend.Index(ctx, work.Document)
		completionErr := w.queue.CompleteSearchWork(ctx, work, syncErr)
		if syncErr != nil {
			failures = append(failures, syncErr)
		}
		if completionErr != nil {
			failures = append(failures, completionErr)
		}
	}
	return errors.Join(failures...)
}
