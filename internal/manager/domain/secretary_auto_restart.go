package domain

import (
	"context"
	"time"
)

// SecretaryAutoRestartMax bounds consecutive automatic restarts of a stalled
// task. A restart can spend another model call on the owner's own agent, so the
// budget stays small. Publishing or any explicit owner retry starts a new budget.
const SecretaryAutoRestartMax = 3

const secretaryAutoRestartBaseDelay = 15 * time.Minute

// SecretaryAutoRestartDelay is how long a task must stay stalled before its
// next automatic restart: 15 minutes, then 1 hour, then 4 hours.
func SecretaryAutoRestartDelay(restarts int) time.Duration {
	delay := secretaryAutoRestartBaseDelay
	for i := 0; i < restarts && i < SecretaryAutoRestartMax; i++ {
		delay *= 4
	}
	return delay
}

// SecretaryTaskRestart reports one automatic restart for operational logging.
// It carries identifiers and states only, never source or summary content.
type SecretaryTaskRestart struct {
	OwnerUserID   string
	TaskID        string
	PreviousState string
	Restarts      int
}

// SecretaryAutoRestartStore is optional so automatic recovery does not expand
// the general store contract.
type SecretaryAutoRestartStore interface {
	// RestartStalledSecretaryTasks restarts at most limit tasks whose current
	// execution is unknown or blocked, exactly as an explicit owner retry would.
	RestartStalledSecretaryTasks(ctx context.Context, limit int) ([]SecretaryTaskRestart, error)
}
