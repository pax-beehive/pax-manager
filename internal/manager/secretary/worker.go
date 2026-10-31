package secretary

import (
	"context"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type WorkerConfig struct {
	BatchSize           int
	LeaseDuration       time.Duration
	SettleDuration      time.Duration
	ExecutionTimeout    time.Duration
	ActiveCheckInterval time.Duration
	PollInterval        time.Duration
	RetryInterval       time.Duration
	OnError             func(error)
}

type Worker struct {
	store    domain.SecretaryStore
	executor Executor
	cfg      WorkerConfig
}

func NewWorker(store domain.SecretaryStore, executor Executor, cfg WorkerConfig) *Worker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 10 * time.Minute
	}
	if cfg.ExecutionTimeout <= 0 || cfg.ExecutionTimeout >= cfg.LeaseDuration {
		cfg.ExecutionTimeout = cfg.LeaseDuration / 2
	}
	if cfg.SettleDuration <= 0 {
		cfg.SettleDuration = 30 * time.Second
	}
	if cfg.ActiveCheckInterval <= 0 {
		cfg.ActiveCheckInterval = time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = 10 * time.Second
	}
	return &Worker{store: store, executor: executor, cfg: cfg}
}

func (w *Worker) Run(ctx context.Context) error {
	initialized := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		if !initialized {
			err = w.store.BackfillSecretaryJobs(ctx)
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
	jobs, err := w.store.ClaimSecretaryJobs(
		ctx,
		w.cfg.BatchSize,
		w.cfg.LeaseDuration,
		w.cfg.SettleDuration,
	)
	if err != nil {
		return err
	}
	var failures []error
	for _, job := range jobs {
		runErr := w.processJob(ctx, job)
		if runErr == nil {
			continue
		}
		failures = append(failures, runErr)
		failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		if err = w.store.FailSecretaryJob(failureCtx, job, runErr); err != nil {
			failures = append(failures, err)
		}
		cancel()
	}
	return errors.Join(failures...)
}

func (w *Worker) processJob(ctx context.Context, job domain.SecretaryJob) error {
	if err := w.ensureActive(ctx, job); err != nil {
		return err
	}
	prompt, err := BuildPrompt(job)
	if err != nil {
		return err
	}
	if w.executor == nil {
		return errors.New("secretary agent executor is unavailable")
	}
	runCtx, cancel := context.WithTimeout(ctx, w.cfg.ExecutionTimeout)
	defer cancel()
	monitorErrors := make(chan error, 1)
	monitorDone := make(chan struct{})
	go w.monitorJob(runCtx, cancel, job, monitorErrors, monitorDone)
	defer func() { cancel(); <-monitorDone }()
	result, err := w.executor.Execute(runCtx, ExecutionRequest{
		OwnerID: job.OwnerUserID, AgentID: job.AgentID, RunID: job.RunID, Prompt: prompt,
		BeforePrompt: func(promptCtx context.Context, agentID, sessionID string) error {
			if activeErr := w.ensureActive(promptCtx, job); activeErr != nil {
				return activeErr
			}
			return w.store.RegisterSecretaryInternalSession(promptCtx, job, agentID, sessionID)
		},
	})
	select {
	case monitorErr := <-monitorErrors:
		return monitorErr
	default:
	}
	if err != nil {
		return err
	}
	if err = w.ensureActive(runCtx, job); err != nil {
		return err
	}
	request, err := BuildPublication(job, result.Text)
	if err != nil {
		return err
	}
	_, err = w.store.PublishSecretaryJob(runCtx, job, request)
	return err
}

func (w *Worker) ensureActive(ctx context.Context, job domain.SecretaryJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	active, err := w.store.SecretaryJobActive(ctx, job)
	if err != nil {
		return err
	}
	if !active {
		return domain.ErrSecretaryFenced
	}
	return nil
}

func (w *Worker) monitorJob(
	ctx context.Context,
	cancel context.CancelFunc,
	job domain.SecretaryJob,
	errorsOut chan<- error,
	done chan<- struct{},
) {
	defer close(done)
	ticker := time.NewTicker(w.cfg.ActiveCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.ensureActive(ctx, job); err != nil {
				errorsOut <- err
				cancel()
				return
			}
		}
	}
}
