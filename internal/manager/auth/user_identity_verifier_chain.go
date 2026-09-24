package auth

import (
	"context"
	"errors"
)

type UserIdentityVerifierChain struct {
	verifiers []UserIdentityVerifier
}

func NewUserIdentityVerifierChain(
	verifiers ...UserIdentityVerifier,
) *UserIdentityVerifierChain {
	return &UserIdentityVerifierChain{verifiers: verifiers}
}

func (v *UserIdentityVerifierChain) Verify(
	ctx context.Context,
	token string,
) (UserIdentity, error) {
	for _, verifier := range v.verifiers {
		identity, err := verifier.Verify(ctx, token)
		if err == nil {
			return identity, nil
		}
		if !errors.Is(err, ErrInvalidAccessJWT) {
			return UserIdentity{}, err
		}
	}
	return UserIdentity{}, ErrInvalidAccessJWT
}
