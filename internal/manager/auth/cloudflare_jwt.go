package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var (
	ErrMissingAccessJWT = errors.New("missing cloudflare access jwt")
	ErrInvalidAccessJWT = errors.New("invalid cloudflare access jwt")
)

type UserIdentity struct {
	Email string
}

type UserIdentityVerifier interface {
	Verify(ctx context.Context, token string) (UserIdentity, error)
}

type CloudflareAccessVerifier struct {
	issuer   string
	audience string
	jwksURL  string
	client   *http.Client
	now      func() time.Time

	mu   sync.Mutex
	keys map[string]*rsa.PublicKey
}

func NewCloudflareAccessVerifier(
	issuer string,
	audience string,
	jwksURL string,
) *CloudflareAccessVerifier {
	return &CloudflareAccessVerifier{
		issuer:   strings.TrimRight(issuer, "/"),
		audience: audience,
		jwksURL:  jwksURL,
		client:   http.DefaultClient,
		now:      time.Now,
		keys:     make(map[string]*rsa.PublicKey),
	}
}

func (v *CloudflareAccessVerifier) Verify(ctx context.Context, token string) (UserIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return UserIdentity{}, ErrInvalidAccessJWT
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeJWTPart(parts[0], &header); err != nil {
		return UserIdentity{}, ErrInvalidAccessJWT
	}
	if header.Alg != "RS256" || header.Kid == "" {
		return UserIdentity{}, ErrInvalidAccessJWT
	}

	var claims struct {
		Iss   string          `json:"iss"`
		Aud   json.RawMessage `json:"aud"`
		Email string          `json:"email"`
		Exp   int64           `json:"exp"`
		Nbf   int64           `json:"nbf"`
	}
	if err := decodeJWTPart(parts[1], &claims); err != nil {
		return UserIdentity{}, ErrInvalidAccessJWT
	}
	if strings.TrimRight(claims.Iss, "/") != v.issuer ||
		!jwtAudienceContains(claims.Aud, v.audience) {
		return UserIdentity{}, ErrInvalidAccessJWT
	}
	now := v.now().Unix()
	if claims.Exp <= now || (claims.Nbf != 0 && claims.Nbf > now) {
		return UserIdentity{}, ErrInvalidAccessJWT
	}
	email := domain.NormalizeEmail(claims.Email)
	if email == "" {
		return UserIdentity{}, ErrInvalidAccessJWT
	}

	key, err := v.publicKey(ctx, header.Kid)
	if err != nil {
		return UserIdentity{}, err
	}
	signed := []byte(parts[0] + "." + parts[1])
	sum := sha256.Sum256(signed)
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return UserIdentity{}, ErrInvalidAccessJWT
	}
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return UserIdentity{}, ErrInvalidAccessJWT
	}
	return UserIdentity{Email: email}, nil
}

func (v *CloudflareAccessVerifier) publicKey(
	ctx context.Context,
	kid string,
) (*rsa.PublicKey, error) {
	v.mu.Lock()
	key := v.keys[kid]
	v.mu.Unlock()
	if key != nil {
		return key, nil
	}
	if err := v.refreshKeys(ctx); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	key = v.keys[kid]
	if key == nil {
		return nil, ErrInvalidAccessJWT
	}
	return key, nil
}

func (v *CloudflareAccessVerifier) refreshKeys(ctx context.Context) error {
	if v.jwksURL == "" {
		return ErrInvalidAccessJWT
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare jwks status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return err
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, raw := range doc.Keys {
		kid, key, err := parseRSAJWK(raw)
		if err != nil {
			continue
		}
		keys[kid] = key
	}
	v.mu.Lock()
	v.keys = keys
	v.mu.Unlock()
	return nil
}

func parseRSAJWK(raw json.RawMessage) (string, *rsa.PublicKey, error) {
	var jwk struct {
		Kid string   `json:"kid"`
		Kty string   `json:"kty"`
		N   string   `json:"n"`
		E   string   `json:"e"`
		X5c []string `json:"x5c"`
	}
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return "", nil, err
	}
	if jwk.Kid == "" {
		return "", nil, ErrInvalidAccessJWT
	}
	if len(jwk.X5c) > 0 {
		certBytes, err := base64.StdEncoding.DecodeString(jwk.X5c[0])
		if err != nil {
			return "", nil, err
		}
		cert, err := x509.ParseCertificate(certBytes)
		if err != nil {
			return "", nil, err
		}
		key, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return "", nil, ErrInvalidAccessJWT
		}
		return jwk.Kid, key, nil
	}
	if jwk.Kty != "RSA" || jwk.N == "" || jwk.E == "" {
		return "", nil, ErrInvalidAccessJWT
	}
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return "", nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return "", nil, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}
	return jwk.Kid, &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func decodeJWTPart(part string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func jwtAudienceContains(raw json.RawMessage, want string) bool {
	var values []string
	if err := json.Unmarshal(raw, &values); err == nil {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value == want
	}
	return false
}
