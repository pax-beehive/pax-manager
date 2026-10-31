package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryAPIFixture struct {
	domain.SecretaryStore
	status  domain.SecretaryStatus
	err     error
	updated int
	retried int
	owner   string
}

func (f *secretaryAPIFixture) GetSecretaryStatus(
	_ context.Context,
	owner string,
) (domain.SecretaryStatus, error) {
	f.owner = owner
	return f.status, f.err
}

func (f *secretaryAPIFixture) GetSecretarySettings(
	_ context.Context,
	owner string,
) (domain.SecretarySettings, error) {
	f.owner = owner
	return f.status.SecretarySettings, f.err
}

func (f *secretaryAPIFixture) UpdateSecretarySettings(
	_ context.Context,
	owner string,
	update domain.SecretarySettingsUpdate,
) (domain.SecretarySettings, error) {
	f.owner = owner
	f.updated++
	if f.err != nil {
		return domain.SecretarySettings{}, f.err
	}
	f.status.SecretarySettings = domain.SecretarySettings{
		OwnerUserID: owner,
		AgentID:     update.AgentID,
		Enabled:     update.Enabled,
		Revision:    update.ExpectedRevision + 1,
	}
	return f.status.SecretarySettings, nil
}

func (f *secretaryAPIFixture) RetrySecretaryJobs(_ context.Context, owner string) error {
	f.owner = owner
	f.retried++
	return f.err
}

type secretaryFleet struct {
	agent domain.Agent
	err   error
}

func (f secretaryFleet) GetAgent(
	context.Context,
	domain.UserPrincipal,
	string,
) (domain.Agent, error) {
	return f.agent, f.err
}

func TestSecretaryAPIGivenRuntimeStatesThenReportsOwnerScopedSafeStatus(t *testing.T) {
	for _, test := range []struct {
		name                           string
		enabled, available, processing bool
		failure, want                  string
	}{
		{"disabled", false, true, false, "", "disabled"},
		{"offline", true, false, false, "", "waiting_for_agent"},
		{"idle", true, true, false, "", "idle"},
		{"processing", true, true, true, "", "processing"},
		{"failed", true, true, false, "private source text and credentials", "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &secretaryAPIFixture{
				status: domain.SecretaryStatus{
					SecretarySettings: domain.SecretarySettings{
						AgentID: "agent",
						Enabled: test.enabled,
					},
					Processing: test.processing,
					LastError:  test.failure,
				},
			}
			service := NewSecretaryService(
				workspacePrincipal{owner: "owner"},
				store,
				secretaryFleet{agent: domain.Agent{AgentID: "agent", OwnerUserID: "owner"}},
				func(context.Context, string, string) bool { return test.available },
			)
			result, err := service.Get(t.Context(), auth.RequestMetadata{})
			require.NoError(t, err)
			assert.Equal(t, "owner", store.owner)
			assert.Equal(t, test.want, result.State)
			assert.Equal(t, test.available, result.AgentAvailable)
			assert.NotContains(t, result.LastError, "credentials")
		})
	}
}

func TestSecretaryAPIGivenOwnedOfflineAgentThenPersistsSelectionAndWaits(t *testing.T) {
	store := &secretaryAPIFixture{}
	service := NewSecretaryService(
		workspacePrincipal{owner: "owner"},
		store,
		secretaryFleet{agent: domain.Agent{AgentID: "owned", OwnerUserID: "owner"}},
		nil,
	)
	result, err := service.Update(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretarySettingsUpdate{AgentID: " owned ", Enabled: true, ExpectedRevision: 3},
	)
	require.NoError(t, err)
	assert.Equal(t, "owned", result.AgentID)
	assert.Equal(t, int64(4), result.Revision)
	assert.Equal(t, "waiting_for_agent", result.State)
	assert.Equal(t, 1, store.updated)
}

func TestSecretaryAPIGivenInvalidSelectionThenCannotEnableOrWriteAnotherOwnersAgent(t *testing.T) {
	for _, test := range []struct {
		name, selected, owner string
		revision              int64
		fleetErr              error
		want                  int
	}{
		{"missing", "", "owner", 0, nil, 400},
		{"negative revision", "agent", "owner", -1, nil, 400},
		{"oversized", strings.Repeat("a", 513), "owner", 0, nil, 400},
		{"foreign", "agent", "other", 0, nil, 404},
		{"deleted", "agent", "owner", 0, domain.ErrNotFound, 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &secretaryAPIFixture{}
			service := NewSecretaryService(
				workspacePrincipal{owner: "owner"},
				store,
				secretaryFleet{
					agent: domain.Agent{AgentID: "agent", OwnerUserID: test.owner},
					err:   test.fleetErr,
				},
				nil,
			)
			_, err := service.Update(
				t.Context(),
				auth.RequestMetadata{},
				domain.SecretarySettingsUpdate{
					AgentID:          test.selected,
					Enabled:          true,
					ExpectedRevision: test.revision,
				},
			)
			require.Error(t, err)
			if test.want == 404 {
				assert.ErrorIs(t, err, domain.ErrNotFound)
			} else {
				var apiErr apperr.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, test.want, apiErr.Status)
			}
			assert.Zero(t, store.updated)
		})
	}
}

func TestSecretaryAPIGivenRemovedAgentThenCanDisableAndClearSelection(t *testing.T) {
	store := &secretaryAPIFixture{}
	service := NewSecretaryService(
		workspacePrincipal{owner: "owner"},
		store,
		secretaryFleet{err: domain.ErrNotFound},
		nil,
	)
	result, err := service.Update(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretarySettingsUpdate{Enabled: false},
	)
	require.NoError(t, err)
	assert.False(t, result.Enabled)
	assert.Equal(t, "disabled", result.State)
	store.status.AgentID = "removed"
	result, err = service.Update(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretarySettingsUpdate{AgentID: "removed", Enabled: false},
	)
	require.NoError(t, err)
	assert.False(t, result.Enabled)
	_, err = service.Update(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretarySettingsUpdate{AgentID: "foreign", Enabled: false},
	)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSecretaryAPIGivenRunNowThenOnlyEnabledOwnerWorkIsQueued(t *testing.T) {
	store := &secretaryAPIFixture{}
	service := NewSecretaryService(workspacePrincipal{owner: "owner"}, store, secretaryFleet{}, nil)
	_, err := service.Run(t.Context(), auth.RequestMetadata{})
	var apiErr apperr.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Status)
	assert.Zero(t, store.retried)
	store.status.Enabled = true
	store.status.AgentID = "agent"
	_, err = service.Run(t.Context(), auth.RequestMetadata{})
	require.NoError(t, err)
	assert.Equal(t, 1, store.retried)
	assert.Equal(t, "owner", store.owner)
}

func TestSecretaryAPIGivenFailuresThenReturnsErrorsWithoutMutatingSettings(t *testing.T) {
	for _, principal := range []PrincipalResolver{nil, workspacePrincipal{}, workspacePrincipal{err: domain.ErrUnauthorized}} {
		service := NewSecretaryService(principal, &secretaryAPIFixture{}, secretaryFleet{}, nil)
		_, err := service.Get(t.Context(), auth.RequestMetadata{})
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
	}
	service := NewSecretaryService(workspacePrincipal{owner: "owner"}, nil, nil, nil)
	_, err := service.Get(t.Context(), auth.RequestMetadata{})
	var apiErr apperr.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.Status)
	store := &secretaryAPIFixture{err: errors.New("database unavailable")}
	service = NewSecretaryService(workspacePrincipal{owner: "owner"}, store, nil, nil)
	_, err = service.Get(t.Context(), auth.RequestMetadata{})
	assert.EqualError(t, err, "database unavailable")
	store.err = domain.ErrConflict
	_, err = service.Update(t.Context(), auth.RequestMetadata{}, domain.SecretarySettingsUpdate{})
	assert.ErrorIs(t, err, domain.ErrConflict)
}
