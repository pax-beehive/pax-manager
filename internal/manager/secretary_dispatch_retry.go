package manager

import (
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

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
