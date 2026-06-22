package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	authmocks "github.com/pax-beehive/pax-manager/internal/manager/auth/mocks"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPrincipal(t *testing.T) {
	t.Run(
		"Given a Cloudflare Access JWT when resolving a principal then it verifies the token and ensures a normalized admin user",
		func(t *testing.T) {
			ctx := context.Background()
			users := authmocks.NewMockUserStore(t)
			tokens := authmocks.NewMockRegistrationTokenStore(t)
			admins := authmocks.NewMockAdminPolicy(t)

			admins.EXPECT().RoleForEmail("admin@example.com").Return("admin").Once()
			admins.EXPECT().IsAdmin("admin@example.com").Return(true).Once()
			users.EXPECT().
				EnsureUser(ctx, "admin@example.com", "", "admin").
				Return(domain.User{UserID: "usr_admin", Email: "admin@example.com", Role: "admin"}, nil).
				Once()

			svc := auth.NewService(users, tokens, admins, auth.Secrets{}, auth.Config{
				IdentityVerifier: fakeIdentityVerifier{email: " Admin@Example.COM "},
			})
			principal, err := svc.Principal(ctx, auth.NewRequestMetadata(map[string]string{
				"Cf-Access-Jwt-Assertion": "jwt",
			}))

			require.NoError(t, err)
			require.True(t, principal.IsAdmin)
			require.Equal(t, "usr_admin", principal.User.UserID)
		},
	)

	t.Run(
		"Given a Cloudflare Access JWT without a verifier when resolving a principal then it rejects the request",
		func(t *testing.T) {
			svc := auth.NewService(
				authmocks.NewMockUserStore(t),
				authmocks.NewMockRegistrationTokenStore(t),
				auth.NewStaticAdminPolicy(nil),
				auth.Secrets{},
				auth.Config{},
			)

			_, err := svc.Principal(context.Background(), auth.NewRequestMetadata(map[string]string{
				"Cf-Access-Jwt-Assertion": "jwt",
			}))

			require.ErrorIs(t, err, domain.ErrUnauthorized)
		},
	)

	t.Run(
		"Given only a local user header when local override is disabled then it rejects the request",
		func(t *testing.T) {
			svc := auth.NewService(
				authmocks.NewMockUserStore(t),
				authmocks.NewMockRegistrationTokenStore(t),
				auth.NewStaticAdminPolicy(nil),
				auth.Secrets{},
				auth.Config{AllowLocalUserHeader: false},
			)

			_, err := svc.Principal(context.Background(), auth.NewRequestMetadata(map[string]string{
				"X-User-Email": "spoof@example.com",
			}))

			require.ErrorIs(t, err, domain.ErrUnauthorized)
		},
	)

	t.Run(
		"Given a paxl bearer token when resolving a principal then it authenticates the stored user api key",
		func(t *testing.T) {
			ctx := context.Background()
			users := &fakeAPIKeyUserStore{
				wantHash: auth.HashSecret("paxu_test"),
				user: domain.User{
					UserID: "usr_cli",
					Email:  "cli@example.com",
					Role:   "user",
				},
			}
			admins := authmocks.NewMockAdminPolicy(t)
			admins.EXPECT().IsAdmin("cli@example.com").Return(false).Once()
			svc := auth.NewService(
				users,
				authmocks.NewMockRegistrationTokenStore(t),
				admins,
				auth.Secrets{},
				auth.Config{},
			)

			principal, err := svc.Principal(ctx, auth.NewRequestMetadata(map[string]string{
				"Authorization": "Bearer paxu_test",
			}))

			require.NoError(t, err)
			require.Equal(t, "usr_cli", principal.User.UserID)
			require.False(t, principal.IsAdmin)
		},
	)
}

type fakeIdentityVerifier struct {
	email string
	err   error
}

func (v fakeIdentityVerifier) Verify(ctx context.Context, token string) (auth.UserIdentity, error) {
	if v.err != nil {
		return auth.UserIdentity{}, v.err
	}
	return auth.UserIdentity{Email: v.email}, nil
}

type fakeAPIKeyUserStore struct {
	wantHash string
	user     domain.User
}

func (s *fakeAPIKeyUserStore) EnsureUser(
	ctx context.Context,
	email string,
	displayName string,
	role string,
) (domain.User, error) {
	return domain.User{}, errors.New("EnsureUser should not be called")
}

func (s *fakeAPIKeyUserStore) AuthenticateUserAPIKey(
	ctx context.Context,
	keyHash string,
) (domain.User, error) {
	if keyHash != s.wantHash {
		return domain.User{}, domain.ErrUnauthorized
	}
	return s.user, nil
}

func TestRegistrationOwner(t *testing.T) {
	t.Run(
		"Given a valid one-time registration token when resolving owner then it hashes the token before lookup",
		func(t *testing.T) {
			ctx := context.Background()
			users := authmocks.NewMockUserStore(t)
			tokens := authmocks.NewMockRegistrationTokenStore(t)
			admins := authmocks.NewMockAdminPolicy(t)
			secrets := authmocks.NewMockSecretIssuer(t)

			owner := domain.User{UserID: "usr_owner", Email: "owner@example.com"}
			secrets.EXPECT().Hash("reg_raw").Return("hashed_reg").Once()
			tokens.EXPECT().ResolveRegistrationToken(ctx, "hashed_reg").Return(owner, nil).Once()

			svc := auth.NewService(users, tokens, admins, secrets, auth.Config{})
			got, err := svc.RegistrationOwner(ctx, auth.NewRequestMetadata(map[string]string{
				"X-Registration-Token": "reg_raw",
			}))

			require.NoError(t, err)
			require.Equal(t, owner, got)
		},
	)

	t.Run(
		"Given the configured bootstrap token when no one-time token matches then it ensures the configured owner",
		func(t *testing.T) {
			ctx := context.Background()
			users := authmocks.NewMockUserStore(t)
			tokens := authmocks.NewMockRegistrationTokenStore(t)
			admins := authmocks.NewMockAdminPolicy(t)
			secrets := authmocks.NewMockSecretIssuer(t)

			secrets.EXPECT().Hash("bootstrap").Return("hashed_bootstrap").Once()
			tokens.EXPECT().
				ResolveRegistrationToken(ctx, "hashed_bootstrap").
				Return(domain.User{}, domain.ErrUnauthorized).
				Once()
			admins.EXPECT().RoleForEmail("owner@example.com").Return("user").Once()
			users.EXPECT().
				EnsureUser(ctx, "owner@example.com", "", "user").
				Return(domain.User{UserID: "usr_owner", Email: "owner@example.com", Role: "user"}, nil).
				Once()

			svc := auth.NewService(users, tokens, admins, secrets, auth.Config{
				RegistrationToken:      "bootstrap",
				RegistrationOwnerEmail: "owner@example.com",
			})
			owner, err := svc.RegistrationOwner(ctx, auth.NewRequestMetadata(map[string]string{
				"X-Registration-Token": "bootstrap",
			}))

			require.NoError(t, err)
			require.Equal(t, "usr_owner", owner.UserID)
		},
	)

	t.Run(
		"Given token storage fails unexpectedly when resolving owner then it returns that failure",
		func(t *testing.T) {
			ctx := context.Background()
			tokens := authmocks.NewMockRegistrationTokenStore(t)
			secrets := authmocks.NewMockSecretIssuer(t)
			storeErr := errors.New("store unavailable")

			secrets.EXPECT().Hash("reg_raw").Return("hashed_reg").Once()
			tokens.EXPECT().
				ResolveRegistrationToken(ctx, "hashed_reg").
				Return(domain.User{}, storeErr).
				Once()

			svc := auth.NewService(
				authmocks.NewMockUserStore(t),
				tokens,
				auth.NewStaticAdminPolicy(nil),
				secrets,
				auth.Config{},
			)
			_, err := svc.RegistrationOwner(ctx, auth.NewRequestMetadata(map[string]string{
				"X-Registration-Token": "reg_raw",
			}))

			require.ErrorIs(t, err, storeErr)
		},
	)
}

func TestStaticAdminPolicy(t *testing.T) {
	t.Run(
		"Given mixed-case configured admins when checking roles then matching is case-insensitive",
		func(t *testing.T) {
			policy := auth.NewStaticAdminPolicy(map[string]bool{"Admin@Example.COM": true})

			require.True(t, policy.IsAdmin("admin@example.com"))
			require.Equal(t, "admin", policy.RoleForEmail("ADMIN@example.com"))
			require.False(t, policy.IsAdmin("user@example.com"))
			require.Equal(t, "user", policy.RoleForEmail("user@example.com"))
		},
	)
}

func TestRequestMetadata(t *testing.T) {
	t.Run(
		"Given headers with mixed casing and whitespace when reading metadata then lookups are normalized",
		func(t *testing.T) {
			meta := auth.NewRequestMetadata(map[string]string{"X-Registration-Token": " token "})

			require.Equal(t, "token", meta.Header("x-registration-token"))
			require.Equal(t, "token", meta.Header("X-REGISTRATION-TOKEN"))
		},
	)
}

func TestBearerToken(t *testing.T) {
	t.Run(
		"Given an authorization header when extracting bearer token then casing is ignored",
		func(t *testing.T) {
			require.Equal(t, "abc", auth.BearerToken("bearer abc"))
			require.Equal(t, "abc", auth.BearerToken("Bearer abc"))
			require.Empty(t, auth.BearerToken("Basic abc"))
		},
	)
}
