package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type UserStore interface {
	EnsureUser(
		ctx context.Context,
		email string,
		displayName string,
		role string,
	) (domain.User, error)
}

type RegistrationTokenStore interface {
	ResolveRegistrationToken(ctx context.Context, tokenHash string) (domain.User, error)
}

type UserAPIKeyStore interface {
	AuthenticateUserAPIKey(ctx context.Context, keyHash string) (domain.User, error)
}

type Service struct {
	users                  UserStore
	registrationTokens     RegistrationTokenStore
	admins                 AdminPolicy
	secrets                SecretIssuer
	identity               UserIdentityVerifier
	registrationToken      string
	registrationOwnerEmail string
	localUserEmail         string
	allowLocalUserHeader   bool
}

type Config struct {
	RegistrationToken      string
	RegistrationOwnerEmail string
	LocalUserEmail         string
	AllowLocalUserHeader   bool
	IdentityVerifier       UserIdentityVerifier
}

func NewService(
	users UserStore,
	registrationTokens RegistrationTokenStore,
	admins AdminPolicy,
	secrets SecretIssuer,
	cfg Config,
) *Service {
	return &Service{
		users:                  users,
		registrationTokens:     registrationTokens,
		admins:                 admins,
		secrets:                secrets,
		identity:               cfg.IdentityVerifier,
		registrationToken:      cfg.RegistrationToken,
		registrationOwnerEmail: cfg.RegistrationOwnerEmail,
		localUserEmail:         cfg.LocalUserEmail,
		allowLocalUserHeader:   cfg.AllowLocalUserHeader,
	}
}

func (s *Service) Principal(
	ctx context.Context,
	meta RequestMetadata,
) (domain.UserPrincipal, error) {
	if token := bearerToken(meta); token != "" {
		users, ok := s.users.(UserAPIKeyStore)
		if !ok {
			return domain.UserPrincipal{}, domain.ErrUnauthorized
		}
		user, err := users.AuthenticateUserAPIKey(ctx, s.secrets.Hash(token))
		if err != nil {
			return domain.UserPrincipal{}, err
		}
		return domain.UserPrincipal{
			User:    user,
			IsAdmin: s.admins.IsAdmin(user.Email),
		}, nil
	}
	email, err := s.userEmail(ctx, meta)
	if err != nil {
		return domain.UserPrincipal{}, err
	}
	role := s.admins.RoleForEmail(email)
	user, err := s.users.EnsureUser(ctx, email, "", role)
	if err != nil {
		return domain.UserPrincipal{}, err
	}
	return domain.UserPrincipal{User: user, IsAdmin: s.admins.IsAdmin(email)}, nil
}

func (s *Service) RegistrationOwner(
	ctx context.Context,
	meta RequestMetadata,
) (domain.User, error) {
	token := meta.Header("X-Registration-Token")
	if token != "" {
		owner, err := s.registrationTokens.ResolveRegistrationToken(ctx, s.secrets.Hash(token))
		if err == nil {
			return owner, nil
		}
		if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrUnauthorized) {
			return domain.User{}, err
		}
	}
	if s.registrationToken != "" && token == s.registrationToken {
		email := s.registrationOwnerEmail
		if email == "" {
			email = s.localUserEmail
		}
		return s.users.EnsureUser(ctx, email, "", s.admins.RoleForEmail(email))
	}
	return domain.User{}, domain.ErrUnauthorized
}

func (s *Service) userEmail(ctx context.Context, meta RequestMetadata) (string, error) {
	if token := meta.Header("Cf-Access-Jwt-Assertion"); token != "" {
		if s.identity == nil {
			return "", domain.ErrUnauthorized
		}
		identity, err := s.identity.Verify(ctx, token)
		if err != nil {
			return "", domain.ErrUnauthorized
		}
		return domain.NormalizeEmail(identity.Email), nil
	}
	if s.allowLocalUserHeader {
		if v := meta.Header("X-User-Email"); v != "" {
			return domain.NormalizeEmail(v), nil
		}
		if s.localUserEmail != "" {
			return domain.NormalizeEmail(s.localUserEmail), nil
		}
	}
	return "", domain.ErrUnauthorized
}

func bearerToken(meta RequestMetadata) string {
	token := strings.TrimSpace(meta.Header("Authorization"))
	if token == "" {
		return ""
	}
	return BearerToken(token)
}
