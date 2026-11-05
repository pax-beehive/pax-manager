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

func TestSecretaryDispatchGivenUnavailableTunnelBeforeRPCThenMarksSafeToRetry(t *testing.T) {
	for _, failure := range []error{
		apperr.Error{Status: http.StatusNotFound, Message: "agent tunnel not connected"},
		apperr.Error{Status: http.StatusConflict, Message: "agent tunnel already in use"},
	} {
		err := secretaryTunnelClaimFailure(failure)
		assert.ErrorIs(t, err, domain.ErrSecretaryNotDispatched)
		assert.ErrorIs(t, err, failure)
	}
	for _, failure := range []error{nil, context.Canceled, context.DeadlineExceeded,
		errors.New("agent tunnel not connected"), domain.ErrNotFound,
		apperr.Error{Status: http.StatusUnauthorized, Message: "agent tunnel not connected"},
	} {
		assert.Equal(t, failure, secretaryTunnelClaimFailure(failure))
	}
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
