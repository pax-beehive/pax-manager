package secretary

import (
	"context"
	"errors"
	"fmt"
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
	// AutoRestartInterval is how often stalled tasks are checked for a bounded
	// automatic restart.
	AutoRestartInterval time.Duration
	OnError             func(error)
	OnRestart           func(domain.SecretaryTaskRestart)
	// OwnerAllowed reports whether the job owner may use Secretary. Jobs of
	// other owners are left leased (not failed) and retried after the lease.
	OwnerAllowed func(ctx context.Context, ownerUserID string) bool
}

// maxSkippedClaims bounds how many claims one pass spends on gated owners,
// so they cannot starve allowed owners behind them in the queue.
const maxSkippedClaims = 32

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
	if cfg.AutoRestartInterval <= 0 {
		cfg.AutoRestartInterval = time.Minute
	}
	return &Worker{store: store, executor: executor, cfg: cfg}
}

func (w *Worker) Run(ctx context.Context) error {
	initialized := false
	var restartChecked time.Time
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
		if initialized && time.Since(restartChecked) >= w.cfg.AutoRestartInterval {
			restartChecked = time.Now()
			err = errors.Join(err, w.RestartStalled(ctx))
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

// RestartStalled gives tasks that can no longer progress on their own a bounded
// automatic restart. It is the same transition as an explicit owner retry: the
// previous attempt stays fenced in its old epoch, so a late result cannot commit.
func (w *Worker) RestartStalled(ctx context.Context) error {
	restarts, ok := w.store.(domain.SecretaryAutoRestartStore)
	if !ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	restarted, err := restarts.RestartStalledSecretaryTasks(ctx, 0)
	if w.cfg.OnRestart != nil {
		for _, task := range restarted {
			w.cfg.OnRestart(task)
		}
	}
	return err
}

func (w *Worker) ProcessOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	jobs, err := w.claimAllowed(ctx)
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

func (w *Worker) claimAllowed(ctx context.Context) ([]domain.SecretaryJob, error) {
	for skipped := 0; ; {
		jobs, err := w.store.ClaimSecretaryJobs(
			ctx,
			w.cfg.BatchSize,
			w.cfg.LeaseDuration,
			w.cfg.SettleDuration,
		)
		if err != nil || len(jobs) == 0 || w.cfg.OwnerAllowed == nil {
			return jobs, err
		}
		allowed := jobs[:0]
		for _, job := range jobs {
			if w.cfg.OwnerAllowed(ctx, job.OwnerUserID) {
				allowed = append(allowed, job)
			} else {
				skipped++
			}
		}
		if len(allowed) > 0 || skipped >= maxSkippedClaims || ctx.Err() != nil {
			return allowed, nil
		}
	}
}

func (w *Worker) processJob(ctx context.Context, job domain.SecretaryJob) error {
	if executions, ok := w.store.(domain.SecretaryExecutionStore); ok {
		if nodes, supported := w.store.(domain.SecretaryNodeStore); supported {
			return w.processNodeJob(ctx, job, executions, nodes)
		}
	}
	if err := w.ensureActive(ctx, job); err != nil {
		return err
	}
	if job.ResetRequired && len(job.Messages) == 0 {
		_, err := w.store.PublishSecretaryJob(ctx, job, BuildEmptyPublication(job))
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
		Job:     job,
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
	request, err := BuildPublicationWithReads(job, result.Text, result.Reads)
	if err != nil {
		return err
	}
	job.AllowedRelatedMessages, err = RequestedRelatedSourcesForPolicy(
		result.Text,
		job.OutputPolicyVersion,
	)
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
	reported := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := w.ensureActive(ctx, job)
			if err == nil || ctx.Err() != nil {
				continue
			}
			if errors.Is(err, domain.ErrSecretaryFenced) {
				errorsOut <- err
				cancel()
				return
			}
			// A failed check proves nothing about the execution. Canceling here
			// would turn one storage hiccup into an unknown outcome, so keep
			// running; publication repeats every fence check anyway.
			if !reported && w.cfg.OnError != nil {
				w.cfg.OnError(fmt.Errorf("check secretary execution fence: %w", err))
				reported = true
			}
		}
	}
}
