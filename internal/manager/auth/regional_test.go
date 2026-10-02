package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/auth/mocks"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestRegionalAuthGivenUnprovisionedIdentityThenNeitherLoginNorStaticRegistrationCreatesUser(
	t *testing.T,
) {
	store := storage.NewMemoryStore(time.Now)
	svc := auth.NewService(
		store,
		store,
		auth.NewStaticAdminPolicy(nil),
		auth.Secrets{},
		auth.Config{
			RequireProvisionedUser: true, AllowLocalUserHeader: true, LocalUserEmail: "owner@example.com", RegistrationToken: "static", RegistrationOwnerEmail: "owner@example.com",
		},
	)
	meta := auth.NewRequestMetadata(map[string]string{"X-Registration-Token": "static"})
	_, err := svc.Principal(t.Context(), meta)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = svc.RegistrationOwner(t.Context(), meta)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = store.GetUserByEmail(t.Context(), "owner@example.com")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	user, err := store.EnsureRegionalUser(t.Context(), "usr_global", "owner@example.com", "user")
	require.NoError(t, err)
	principal, err := svc.Principal(t.Context(), meta)
	require.NoError(t, err)
	assert.Equal(t, user, principal.User)
	owner, err := svc.RegistrationOwner(t.Context(), meta)
	require.NoError(t, err)
	assert.Equal(t, user, owner)
}
func TestRegionalAuthGivenStoreWithoutReadSupportThenFailsClosed(t *testing.T) {
	svc := auth.NewService(
		mocks.NewMockUserStore(t),
		nil,
		auth.NewStaticAdminPolicy(nil),
		auth.Secrets{},
		auth.Config{
			RequireProvisionedUser: true,
			AllowLocalUserHeader:   true,
			LocalUserEmail:         "owner@example.com",
		},
	)
	_, err := svc.Principal(t.Context(), auth.NewRequestMetadata(nil))
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}
