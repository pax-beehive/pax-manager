package domain

import (
	"context"
	"time"
)

// SecretaryAutoRestartMax bounds consecutive automatic restarts of a stalled
// task. A restart can spend more model calls on the owner's own agent, so the
// budget stays small. Publishing, a settings change, or any explicit owner
// retry starts a new budget.
const SecretaryAutoRestartMax = 3

const secretaryAutoRestartBaseDelay = 15 * time.Minute

// SecretaryAutoRestartBudget is how many consecutive automatic restarts a
// stalled task may receive. A batch that used up its dispatched attempts has
// already spent up to three model calls on the same input, so it gets one
// fresh start. States that spent at most one call, or none, get the full budget.
func SecretaryAutoRestartBudget(attemptsExhausted bool) int {
	if attemptsExhausted {
		return 1
	}
	return SecretaryAutoRestartMax
}

// SecretaryAutoRestartDelay is how long a task must stay stalled before its
// next automatic restart: 15 minutes, then 1 hour, then 4 hours.
func SecretaryAutoRestartDelay(restarts int) time.Duration {
	delay := secretaryAutoRestartBaseDelay
	for i := 0; i < restarts && i < SecretaryAutoRestartMax; i++ {
		delay *= 4
	}
	return delay
}

// SecretaryTaskRestart reports one automatic recovery for operational logging.
// It carries identifiers and states only, never source or summary content.
type SecretaryTaskRestart struct {
	OwnerUserID   string
	TaskID        string
	PreviousState string
	// Restarts is the consecutive automatic restart count after this recovery.
	Restarts int
	// Resumed is true when saved output was only released for publishing; no
	// new epoch was started and no restart was counted.
	Resumed bool
}

// SecretaryAutoRestartStore is optional so automatic recovery does not expand
// the general store contract.
type SecretaryAutoRestartStore interface {
	// RestartStalledSecretaryTasks recovers at most limit tasks that ordinary
	// claims can no longer reach, exactly as an explicit owner retry would.
	RestartStalledSecretaryTasks(ctx context.Context, limit int) ([]SecretaryTaskRestart, error)
}
