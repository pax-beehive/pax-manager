package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type canonicalSecretaryWakeProbe struct {
	domain.Store
	ctx context.Context
	job domain.SecretaryJob
	err error
}

func (s *canonicalSecretaryWakeProbe) WakeSecretaryExecutionRecovery(
	ctx context.Context,
	job domain.SecretaryJob,
) error {
	s.ctx, s.job = ctx, job
	return s.err
}

func TestCanonicalSessionStoreGivenSecretaryTerminalWakeThenForwardsIdentityContextAndError(
	t *testing.T,
) {
	for name, failure := range map[string]error{"success": nil, "storage_failure": domain.ErrConflict, "canceled": context.Canceled} {
		t.Run(name, func(t *testing.T) {
			probe := &canonicalSecretaryWakeProbe{err: failure}
			wrapped := newCanonicalSessionStore(probe)
			wake, ok := wrapped.(domain.SecretaryExecutionRecoveryWakeStore)
			require.True(t, ok, "the production wrapper must preserve terminal recovery")
			job := domain.SecretaryJob{OwnerUserID: "owner", AgentID: "agent", SourceID: "source",
				BatchID: "batch", AttemptID: "attempt", SettingsRevision: 3, RetryGeneration: 10, RetryEpoch: 4}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == context.Canceled {
				cancel()
			}
			assert.ErrorIs(t, wake.WakeSecretaryExecutionRecovery(ctx, job), failure)
			assert.Same(t, ctx, probe.ctx)
			assert.Equal(t, job, probe.job)
		})
	}
}

func TestCanonicalSessionStoreGivenMissingSecretaryWakeThenReturnsConflict(t *testing.T) {
	wrapped := newCanonicalSessionStore(struct{ domain.Store }{})
	wake, ok := wrapped.(domain.SecretaryExecutionRecoveryWakeStore)
	require.True(t, ok)
	assert.ErrorIs(
		t,
		wake.WakeSecretaryExecutionRecovery(t.Context(), domain.SecretaryJob{}),
		domain.ErrConflict,
	)
}
