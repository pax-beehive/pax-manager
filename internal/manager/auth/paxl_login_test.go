package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaxlLoginGivenWorkerProofThenIdentityCodePurposeAndRegionAreBound(t *testing.T) {
	now := time.Now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	secret := strings.Repeat("s", 40)
	body := []byte(
		`{"identity_key":"owner@example.com","user_id":"usr_home","region":"hk","user_code":"ABC123","purpose":"user"}`,
	)
	sign := func(body []byte, ts string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte("pax-login-approval-v1\n" + ts + "\n"))
		_, _ = mac.Write(body)
		return hex.EncodeToString(mac.Sum(nil))
	}
	signature := sign(body, timestamp)
	proof, err := VerifyPaxlLoginApproval(body, timestamp, signature, "hk", secret, "ABC123", now)
	require.NoError(t, err)
	assert.Equal(t, "owner@example.com", proof.IdentityKey)
	for _, tc := range []struct {
		name, ts, sig, region, key, code string
		body                             []byte
	}{
		{"different code", timestamp, signature, "hk", secret, "OTHER1", body},
		{"different region", timestamp, signature, "us", secret, "ABC123", body},
		{"forged signature", timestamp, "00", "hk", secret, "ABC123", body},
		{"expired", strconv.FormatInt(now.Add(-time.Minute).Unix(), 10), signature, "hk", secret, "ABC123", body},
		{"invalid timestamp", "invalid", signature, "hk", secret, "ABC123", body},
		{"invalid key", timestamp, signature, "hk", "short", "ABC123", body},
		{"tampered body", timestamp, signature, "hk", secret, "ABC123", []byte(`{}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyPaxlLoginApproval(
				tc.body,
				tc.ts,
				tc.sig,
				tc.region,
				tc.key,
				tc.code,
				now,
			)
			assert.Error(t, err)
		})
	}
	for _, invalid := range []string{`{}`, `{"identity_key":"OWNER@example.com","user_id":"usr_home","region":"hk","user_code":"ABC123","purpose":"user"}`, `{"identity_key":"owner@example.com","user_id":"usr_home","region":"hk","user_code":"ABC123","purpose":"unknown"}`, string(body) + ` {}`, strings.Replace(string(body), `"user_id":"usr_home"`, `"user_id":"invalid"`, 1), `{"extra":true}`} {
		_, err := VerifyPaxlLoginApproval(
			[]byte(invalid),
			timestamp,
			sign([]byte(invalid), timestamp),
			"hk",
			secret,
			"ABC123",
			now,
		)
		assert.Error(t, err)
	}
}
