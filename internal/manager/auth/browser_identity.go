package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
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

// The directory signs a distinct purpose and exact request target. Cloudflare
// service credentials alone never represent a PAX user.
func verifyBrowserIdentity(
	token, region, secret string,
	meta RequestMetadata,
) (browserIdentity, error) {
	var identity browserIdentity
	if len(secret) < 32 || (region != "us" && region != "hk") || len(token) > 4096 {
		return identity, domain.ErrUnauthorized
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return identity, domain.ErrUnauthorized
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return identity, domain.ErrUnauthorized
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return identity, domain.ErrUnauthorized
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("pax-browser-identity-v1\n"))
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), signature) {
		return identity, domain.ErrUnauthorized
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&identity) != nil || decoder.Decode(new(any)) != io.EOF {
		return identity, domain.ErrUnauthorized
	}
	if !identity.valid(region, meta, time.Now().Unix()) {
		return browserIdentity{}, domain.ErrUnauthorized
	}
	return identity, nil
}

func (i browserIdentity) valid(region string, meta RequestMetadata, now int64) bool {
	return i.Region == region && (i.HomeRegion == "us" || i.HomeRegion == "hk") &&
		i.Timestamp >= now-30 && i.Timestamp <= now+5 &&
		i.Method != "" && i.Method == meta.method && i.Path != "" && i.Path == meta.path &&
		strings.HasPrefix(i.UserID, "usr_") && len(i.UserID) > 4 && len(i.UserID) <= 128 &&
		strings.HasPrefix(i.Subject, "user_") && len(i.Subject) > 5 && len(i.Subject) <= 128 &&
		i.IdentityKey == domain.NormalizeEmail(i.IdentityKey) &&
		strings.Contains(i.IdentityKey, "@") && len(i.IdentityKey) <= 254
}

func (s *Service) browserPrincipal(
	ctx context.Context,
	meta RequestMetadata,
	token string,
) (domain.UserPrincipal, error) {
	identity, err := verifyBrowserIdentity(token, s.region, s.browserIdentitySecret, meta)
	if err != nil {
		return domain.UserPrincipal{}, err
	}
	user, err := s.resolveUser(ctx, identity.IdentityKey)
	if err != nil {
		return domain.UserPrincipal{}, err
	}
	principal := domain.UserPrincipal{User: user, IsAdmin: s.admins.IsAdmin(user.Email)}
	if identity.HomeRegion == identity.Region {
		if user.UserID != identity.UserID {
			return domain.UserPrincipal{}, domain.ErrUnauthorized
		}
	} else if !principal.IsAdmin || !foreignAdminTarget(meta) {
		return domain.UserPrincipal{}, domain.ErrUnauthorized
	}
	return principal, nil
}

func foreignAdminTarget(meta RequestMetadata) bool {
	if meta.method == "GET" && meta.path == "/api/v1/user/self/customer-analytics" {
		return true
	}
	const prefix = "/api/v1/user/self/paxl/device-logins/"
	return meta.method == "POST" && strings.HasPrefix(meta.path, prefix) &&
		strings.HasSuffix(
			meta.path,
			"/approve",
		) && strings.Count(strings.TrimPrefix(meta.path, prefix), "/") == 1
}
