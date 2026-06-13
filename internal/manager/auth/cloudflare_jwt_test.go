package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCloudflareAccessVerifier(t *testing.T) {
	t.Run(
		"Given a valid Cloudflare Access JWT when verifying then it returns the normalized email",
		func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)

			verifier := NewCloudflareAccessVerifier(
				"https://team.cloudflareaccess.com",
				"aud-1",
				"https://jwks.local/certs",
			)
			verifier.now = func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) }
			verifier.client = &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(
							strings.NewReader(jwksForRSAKey("kid-1", &key.PublicKey)),
						),
						Header: http.Header{"Content-Type": []string{"application/json"}},
					}, nil
				}),
			}

			token := signTestJWT(t, key, map[string]any{
				"iss":   "https://team.cloudflareaccess.com",
				"aud":   []string{"aud-1"},
				"email": " User@Example.COM ",
				"nbf":   verifier.now().Add(-time.Minute).Unix(),
				"exp":   verifier.now().Add(time.Minute).Unix(),
			})

			identity, err := verifier.Verify(context.Background(), token)

			require.NoError(t, err)
			require.Equal(t, "user@example.com", identity.Email)
		},
	)

	t.Run(
		"Given a JWT with the wrong audience when verifying then it rejects the token",
		func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)

			verifier := NewCloudflareAccessVerifier(
				"https://team.cloudflareaccess.com",
				"aud-1",
				"https://jwks.local/certs",
			)
			verifier.now = func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) }
			token := signTestJWT(t, key, map[string]any{
				"iss":   "https://team.cloudflareaccess.com",
				"aud":   []string{"other-aud"},
				"email": "user@example.com",
				"exp":   verifier.now().Add(time.Minute).Unix(),
			})

			_, err = verifier.Verify(context.Background(), token)

			require.ErrorIs(t, err, ErrInvalidAccessJWT)
		},
	)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func signTestJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "kid-1"}
	encodedHeader := encodeJWTJSON(t, header)
	encodedClaims := encodeJWTJSON(t, claims)
	signed := encodedHeader + "." + encodedClaims
	sum := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func encodeJWTJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(data)
}

func jwksForRSAKey(kid string, key *rsa.PublicKey) string {
	e := big.NewInt(int64(key.E)).Bytes()
	doc := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"kid": kid,
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(e),
		}},
	}
	data, _ := json.Marshal(doc)
	return string(data)
}
