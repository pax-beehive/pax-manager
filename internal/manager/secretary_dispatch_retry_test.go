package manager

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryDispatchGivenAnyFailureBeforePromptThenMarksSafeToRetry(t *testing.T) {
	for name, failure := range map[string]error{
		"tunnel offline": apperr.Error{Status: http.StatusNotFound, Message: "agent tunnel not connected"},
		"tunnel busy":    apperr.Error{Status: http.StatusConflict, Message: "agent tunnel already in use"},
		"storage":        errors.New("storage unavailable"),
		"canceled":       context.Canceled,
		"deadline":       context.DeadlineExceeded,
		"missing agent":  domain.ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			err := secretaryNotDispatchedFailure(failure)
			assert.ErrorIs(t, err, domain.ErrSecretaryNotDispatched)
			assert.ErrorIs(t, err, failure)
		})
	}
}

func TestSecretaryDispatchGivenFencedOrUnknownFailureThenKeepsItsOriginalMeaning(t *testing.T) {
	require.NoError(t, secretaryNotDispatchedFailure(nil))
	fenced := errors.Join(domain.ErrSecretaryFenced, errors.New("settings changed"))
	assert.Equal(t, fenced, secretaryNotDispatchedFailure(fenced))
	already := errors.Join(domain.ErrSecretaryNotDispatched, errors.New("offline"))
	assert.Equal(t, already, secretaryNotDispatchedFailure(already))
	// Storage and the worker treat an explicit unknown outcome as unknown even
	// when it is also marked undispatched.
	unknown := secretaryNotDispatchedFailure(domain.ErrSecretaryExecutionUnknown)
	assert.ErrorIs(t, unknown, domain.ErrSecretaryExecutionUnknown)
}

func TestSecretaryAgentGivenNoLiveTunnelThenFailsBeforeCreatingNativeSession(t *testing.T) {
	srv, fixture, job, _, _ := secretaryToolsFixture(t)
	job.BatchID, job.AttemptID, job.RunID = "batch", "attempt", "attempt"
	job.Attempt = &domain.SecretaryAttempt{ID: "attempt", BatchID: "batch", State: "running"}
	_, err := srv.executeSecretaryAgent(t.Context(), secretary.ExecutionRequest{
		OwnerID: job.OwnerUserID, AgentID: job.AgentID, RunID: job.RunID,
		Job: job, Prompt: "Maintain summaries",
	})
	require.ErrorIs(t, err, domain.ErrSecretaryNotDispatched)
	assert.NotErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	_, err = srv.store.GetSession(
		t.Context(), principal, secretary.ExecutionSessionID(job.OwnerUserID, job.RunID),
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
