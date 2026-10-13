package auth_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

type browserIdentity struct {
	UserID      string `json:"user_id"`
	IdentityKey string `json:"identity_key"`
	HomeRegion  string `json:"home_region"`
	Region      string `json:"region"`
	Subject     string `json:"subject"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Timestamp   int64  `json:"timestamp"`
}

func browserProof(t *testing.T, identity browserIdentity, secret string) string {
	t.Helper()
	body, err := json.Marshal(identity)
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("pax-browser-identity-v1\n"))
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(
		body,
	) + "." + base64.RawURLEncoding.EncodeToString(
		mac.Sum(nil),
	)
}
func TestBrowserIdentityPreservesAccountAndRejectsInvalidProofs(t *testing.T) {
	secret := strings.Repeat("s", 40)
	store := storage.NewMemoryStore(time.Now)
	user, err := store.EnsureRegionalUser(
		context.Background(),
		"usr_original",
		"owner@example.com",
		"user",
	)
	require.NoError(t, err)
	svc := auth.NewService(
		store,
		store,
		auth.NewStaticAdminPolicy(nil),
		auth.Secrets{},
		auth.Config{RequireProvisionedUser: true, Region: "us", BrowserIdentitySecret: secret},
	)
	proof := browserIdentity{
		UserID:      user.UserID,
		IdentityKey: user.Email,
		HomeRegion:  "us",
		Region:      "us",
		Subject:     "user_123",
		Method:      "GET",
		Path:        "/api/v1/user/self/agents",
		Timestamp:   time.Now().Unix(),
	}
	meta := func(token string) auth.RequestMetadata {
		return auth.NewRequestMetadata(map[string]string{"X-Pax-Identity-Assertion": token}).
			WithTarget("GET", proof.Path)
	}
	principal, err := svc.Principal(t.Context(), meta(browserProof(t, proof, secret)))
	require.NoError(t, err)
	require.Equal(t, user.UserID, principal.User.UserID)
	require.False(t, principal.IsAdmin)
	cases := map[string]func(*browserIdentity){
		"wrong region":     func(p *browserIdentity) { p.Region = "hk" },
		"expired":          func(p *browserIdentity) { p.Timestamp -= 31 },
		"future":           func(p *browserIdentity) { p.Timestamp += 60 },
		"wrong user ID":    func(p *browserIdentity) { p.UserID = "usr_other" },
		"unprovisioned":    func(p *browserIdentity) { p.IdentityKey = "missing@example.com" },
		"different method": func(p *browserIdentity) { p.Method = "POST" },
		"different path":   func(p *browserIdentity) { p.Path = "/api/v1/user/self/api-keys" },
		"foreign user":     func(p *browserIdentity) { p.HomeRegion = "hk" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			mutate(&candidate)
			_, err := svc.Principal(t.Context(), meta(browserProof(t, candidate, secret)))
			require.Error(t, err)
		})
	}
	for _, token := range []string{"bad", "a.b.c", "!.!", browserProof(t, proof, strings.Repeat("x", 40))} {
		_, err := svc.Principal(t.Context(), meta(token))
		require.ErrorIs(t, err, domain.ErrUnauthorized)
	}
	_, err = svc.Principal(
		t.Context(),
		auth.NewRequestMetadata(
			map[string]string{
				"X-Pax-Identity-Assertion": browserProof(t, proof, secret),
				"X-Pax-Request-Path":       proof.Path,
			},
		),
	)
	require.ErrorIs(t, err, domain.ErrUnauthorized)
}
func TestForeignAdminCanReadAnalyticsButCannotUseRegionalResources(t *testing.T) {
	secret := strings.Repeat("s", 40)
	store := storage.NewMemoryStore(time.Now)
	_, err := store.EnsureRegionalUser(t.Context(), "usr_local_admin", "admin@example.com", "admin")
	require.NoError(t, err)
	svc := auth.NewService(
		store,
		store,
		auth.NewStaticAdminPolicy(map[string]bool{"admin@example.com": true}),
		auth.Secrets{},
		auth.Config{RequireProvisionedUser: true, Region: "hk", BrowserIdentitySecret: secret},
	)
	for _, path := range []string{"/api/v1/user/self/customer-analytics", "/api/v1/user/self/agents"} {
		proof := browserIdentity{
			UserID:      "usr_canonical_admin",
			IdentityKey: "admin@example.com",
			HomeRegion:  "us",
			Region:      "hk",
			Subject:     "user_123",
			Method:      "GET",
			Path:        path,
			Timestamp:   time.Now().Unix(),
		}
		_, err := svc.Principal(
			t.Context(),
			auth.NewRequestMetadata(map[string]string{"X-Pax-Identity-Assertion": browserProof(t, proof, secret)}).
				WithTarget("GET", path),
		)
		if strings.HasSuffix(path, "customer-analytics") {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, domain.ErrUnauthorized)
		}
	}
}
