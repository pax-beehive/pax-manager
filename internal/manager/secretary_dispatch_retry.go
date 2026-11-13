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

func secretarySessionCreationFailure(err error) error {
	// Session creation precedes session/prompt. Only a structured router
	// readiness failure enters connection backoff; other failures stay unchanged.
	if !errors.Is(err, domain.ErrSecretaryExecutionUnknown) &&
		isConversationACPErrorKind(err, "slot_unavailable") && isConversationACPRetryable(err) {
		return errors.Join(domain.ErrSecretaryNotDispatched, err)
	}
	return err
}
