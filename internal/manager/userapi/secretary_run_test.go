package userapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryRunAPIFixture struct {
	domain.SecretaryStore
	owner, request string
	err            error
}

type secretaryNoRunStore struct{ domain.SecretaryStore }

func (f *secretaryRunAPIFixture) GetSecretaryStatus(
	context.Context,
	string,
) (domain.SecretaryStatus, error) {
	panic("run acknowledgement must not calculate status")
}

func (f *secretaryRunAPIFixture) RetrySecretaryJobs(context.Context, string) error {
	panic("run acknowledgement must not reconcile history")
}

func (f *secretaryRunAPIFixture) RequestSecretaryRun(
	_ context.Context,
	owner, id string,
) (domain.SecretaryRunReceipt, error) {
	f.owner, f.request = owner, id
	return domain.SecretaryRunReceipt{
		RequestID:  id,
		State:      "accepted",
		AcceptedAt: time.Unix(100, 0).UTC(),
	}, f.err
}

func TestSecretaryRunAPIGivenAcceptedCommandThenNeverReadsStatusOrReconcilesHistory(t *testing.T) {
	store := &secretaryRunAPIFixture{}
	service := NewSecretaryService(workspacePrincipal{owner: "owner"}, store, nil, nil)
	for _, id := range []string{"request-1", ""} {
		result, err := service.Run(t.Context(), auth.RequestMetadata{}, id)
		require.NoError(t, err)
		assert.Equal(t, "accepted", result.State)
		assert.Equal(t, "owner", store.owner)
		assert.True(t, domain.ValidSecretaryRunRequestID(result.RequestID))
		assert.Equal(t, store.request, result.RequestID)
		assert.Equal(t, time.Unix(100, 0).UTC(), result.AcceptedAt)
		if id != "" {
			assert.Equal(t, id, result.RequestID)
		}
	}
}

func TestSecretaryRunAPIGivenRejectedCommandThenNeverReportsAccepted(t *testing.T) {
	for _, scenario := range []string{"anonymous", "invalid", "disabled", "unavailable", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			store := &secretaryRunAPIFixture{}
			owner, id := "owner", "request-1"
			var capability domain.SecretaryStore = store
			want := http.StatusBadRequest
			switch scenario {
			case "anonymous":
				owner = ""
			case "invalid":
				id = strings.Repeat("x", 129)
			case "disabled":
				store.err = domain.ErrConflict
				want = http.StatusConflict
			case "unavailable":
				store.err = assert.AnError
			case "unsupported":
				capability = &secretaryNoRunStore{}
				want = http.StatusServiceUnavailable
			}
			service := NewSecretaryService(workspacePrincipal{owner: owner}, capability, nil, nil)
			_, err := service.Run(t.Context(), auth.RequestMetadata{}, id)
			if scenario == "anonymous" {
				assert.ErrorIs(t, err, domain.ErrUnauthorized)
				return
			}
			if scenario == "unavailable" {
				assert.ErrorIs(t, err, assert.AnError)
				return
			}
			var apiErr apperr.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, want, apiErr.Status)
		})
	}
}
