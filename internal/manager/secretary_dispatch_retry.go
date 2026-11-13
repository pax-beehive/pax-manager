package manager

import (
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// secretarySessionCreationTimeout bounds session/new for an internal execution.
// Creating a session normally takes seconds, including adapter and MCP startup.
var secretarySessionCreationTimeout = 90 * time.Second

// secretaryNotDispatchedFailure classifies a failure that happened before any
// session/prompt frame was written. No native turn can exist yet, so the
// attempt uses the bounded connection retry instead of becoming an unknown
// outcome that needs manual recovery. Fencing keeps its own meaning, and an
// explicitly unknown outcome stays unknown.
func secretaryNotDispatchedFailure(err error) error {
	if err == nil || errors.Is(err, domain.ErrSecretaryFenced) ||
		errors.Is(err, domain.ErrSecretaryNotDispatched) {
		return err
	}
	return errors.Join(domain.ErrSecretaryNotDispatched, err)
}
