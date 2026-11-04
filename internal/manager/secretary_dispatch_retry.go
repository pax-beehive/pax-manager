package manager

import (
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Only classify the runner-claim phase: no ACP request has been sent yet.
func secretaryTunnelClaimFailure(err error) error {
	if isAgentTunnelNotConnected(err) || isAgentTunnelAlreadyInUse(err) {
		return errors.Join(domain.ErrSecretaryNotDispatched, err)
	}
	return err
}
