package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserIdentityVerifierChain(t *testing.T) {
	t.Run(
		"Given the primary verifier accepts when verifying then fallback is not called",
		func(t *testing.T) {
			fallbackCalls := 0
			verifier := NewUserIdentityVerifierChain(
				identityVerifierFunc(func(context.Context, string) (UserIdentity, error) {
					return UserIdentity{Email: "old@example.com"}, nil
				}),
				identityVerifierFunc(func(context.Context, string) (UserIdentity, error) {
					fallbackCalls++
					return UserIdentity{Email: "new@example.com"}, nil
				}),
			)

			identity, err := verifier.Verify(context.Background(), "token")

			require.NoError(t, err)
			require.Equal(t, "old@example.com", identity.Email)
			require.Zero(t, fallbackCalls)
		},
	)

	t.Run(
		"Given the primary verifier rejects the JWT when verifying then fallback is tried",
		func(t *testing.T) {
			verifier := NewUserIdentityVerifierChain(
				identityVerifierFunc(func(context.Context, string) (UserIdentity, error) {
					return UserIdentity{}, ErrInvalidAccessJWT
				}),
				identityVerifierFunc(func(context.Context, string) (UserIdentity, error) {
					return UserIdentity{Email: "new@example.com"}, nil
				}),
			)

			identity, err := verifier.Verify(context.Background(), "token")

			require.NoError(t, err)
			require.Equal(t, "new@example.com", identity.Email)
		},
	)

	t.Run(
		"Given the primary verifier has an operational failure when verifying then fallback is not called",
		func(t *testing.T) {
			upstreamErr := errors.New("jwks unavailable")
			fallbackCalls := 0
			verifier := NewUserIdentityVerifierChain(
				identityVerifierFunc(func(context.Context, string) (UserIdentity, error) {
					return UserIdentity{}, upstreamErr
				}),
				identityVerifierFunc(func(context.Context, string) (UserIdentity, error) {
					fallbackCalls++
					return UserIdentity{Email: "new@example.com"}, nil
				}),
			)

			_, err := verifier.Verify(context.Background(), "token")

			require.ErrorIs(t, err, upstreamErr)
			require.Zero(t, fallbackCalls)
		},
	)

	t.Run(
		"Given every verifier rejects the JWT when verifying then it returns invalid JWT",
		func(t *testing.T) {
			verifier := NewUserIdentityVerifierChain(
				identityVerifierFunc(func(context.Context, string) (UserIdentity, error) {
					return UserIdentity{}, ErrInvalidAccessJWT
				}),
				identityVerifierFunc(func(context.Context, string) (UserIdentity, error) {
					return UserIdentity{}, ErrInvalidAccessJWT
				}),
			)

			_, err := verifier.Verify(context.Background(), "token")

			require.ErrorIs(t, err, ErrInvalidAccessJWT)
		},
	)
}

type identityVerifierFunc func(context.Context, string) (UserIdentity, error)

func (fn identityVerifierFunc) Verify(
	ctx context.Context,
	token string,
) (UserIdentity, error) {
	return fn(ctx, token)
}
