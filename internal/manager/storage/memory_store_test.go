package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreUserAndAPIKeyLifecycle(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()

	user, err := store.EnsureUser(ctx, " Owner@Example.COM ", "Owner", "")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	if user.Email != "owner@example.com" || user.Role != "user" {
		t.Fatalf("user = %+v", user)
	}

	now = now.Add(time.Minute)
	updated, err := store.EnsureUser(ctx, "owner@example.com", "Owner Two", "admin")
	if err != nil {
		t.Fatalf("ensure existing user: %v", err)
	}
	if updated.UserID != user.UserID || updated.DisplayName != "Owner Two" ||
		updated.Role != "admin" || updated.LastSeenAt == nil || !updated.LastSeenAt.Equal(now) {
		t.Fatalf("updated user = %+v", updated)
	}

	key, err := store.CreateUserAPIKey(
		ctx,
		UserPrincipal{User: updated},
		"laptop",
		"hashed_key",
		"paxu_abc",
	)
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	authenticated, err := store.AuthenticateUserAPIKey(ctx, "hashed_key")
	if err != nil {
		t.Fatalf("authenticate api key: %v", err)
	}
	if authenticated.UserID != updated.UserID {
		t.Fatalf("authenticated user = %+v", authenticated)
	}
	keys, err := store.ListUserAPIKeys(ctx, UserPrincipal{User: updated})
	if err != nil {
		t.Fatalf("list api keys: %v", err)
	}
	if len(keys) != 1 || keys[0].KeyID != key.KeyID {
		t.Fatalf("keys = %+v", keys)
	}
	if err := store.RevokeUserAPIKey(ctx, UserPrincipal{User: updated}, key.KeyID); err != nil {
		t.Fatalf("revoke api key: %v", err)
	}
	if _, err := store.AuthenticateUserAPIKey(ctx, "hashed_key"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("authenticate revoked err = %v, want unauthorized", err)
	}
}

func TestMemoryStoreUserLookupNormalizesEmail(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	user, err := store.EnsureUser(ctx, " Owner@Example.COM ", "Owner", "")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	byEmail, err := store.GetUserByEmail(ctx, "OWNER@example.com")
	if err != nil {
		t.Fatalf("get user by email: %v", err)
	}
	if byEmail.UserID != user.UserID {
		t.Fatalf("user by email = %+v, want %s", byEmail, user.UserID)
	}
	byID, err := store.GetUser(ctx, user.UserID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if byID.Email != "owner@example.com" {
		t.Fatalf("user by id = %+v", byID)
	}
}

func TestMemoryStoreRegistrationTokenLifecycle(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := context.Background()
	user, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	expiresAt := now.Add(time.Minute)
	if err := store.CreateRegistrationToken(ctx, user.UserID, "token_hash", &expiresAt); err != nil {
		t.Fatalf("create registration token: %v", err)
	}
	resolved, err := store.ResolveRegistrationToken(ctx, "token_hash")
	if err != nil {
		t.Fatalf("resolve registration token: %v", err)
	}
	if resolved.UserID != user.UserID {
		t.Fatalf("resolved user = %+v", resolved)
	}
	if _, err := store.ResolveRegistrationToken(ctx, "token_hash"); !errors.Is(
		err,
		ErrUnauthorized,
	) {
		t.Fatalf("reuse token err = %v, want unauthorized", err)
	}

	expiredAt := now.Add(-time.Minute)
	if err := store.CreateRegistrationToken(ctx, user.UserID, "expired_hash", &expiredAt); err != nil {
		t.Fatalf("create expired registration token: %v", err)
	}
	if _, err := store.ResolveRegistrationToken(ctx, "expired_hash"); !errors.Is(
		err,
		ErrUnauthorized,
	) {
		t.Fatalf("expired token err = %v, want unauthorized", err)
	}
}

func TestMemoryStoreRejectsUnknownRegistrationTokenOwner(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	})
	err := store.CreateRegistrationToken(context.Background(), "missing_user", "hash", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("create token err = %v, want not found", err)
	}
}
