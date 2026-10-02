package auth_test

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

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
)

func TestProvisioningGivenSignedAssignmentThenEnforcesBodyRegionAndExpiry(t *testing.T) {
	now := time.Now()
	secret := strings.Repeat("s", 32)
	valid := `{"user_id":"usr_global","identity_key":"owner@example.com","region":"hk"}`
	sign := func(body, ts string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		_, err := mac.Write([]byte("pax-region-ensure-v1\n" + ts + "\n" + body))
		require.NoError(t, err)
		return hex.EncodeToString(mac.Sum(nil))
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	got, err := auth.VerifyProvisioning([]byte(valid), ts, sign(valid, ts), "hk", secret, now)
	require.NoError(t, err)
	assert.Equal(t, "usr_global", got.UserID)
	for name, body := range map[string]string{"json": "{", "trailing": valid + " {}", "role": strings.TrimSuffix(valid, "}") + `,"role":"admin"}`, "wrong region": strings.Replace(valid, "hk", "us", 1), "missing id": strings.Replace(valid, "usr_global", "", 1), "bad email": strings.Replace(valid, "owner@example.com", "Owner@example.com", 1), "no email": strings.Replace(valid, "owner@example.com", "invalid", 1)} {
		t.Run(name, func(t *testing.T) {
			_, err := auth.VerifyProvisioning([]byte(body), ts, sign(body, ts), "hk", secret, now)
			assert.Error(t, err)
		})
	}
	for _, timestamp := range []string{"nope", strconv.FormatInt(now.Unix()-31, 10), strconv.FormatInt(now.Unix()+6, 10)} {
		_, err := auth.VerifyProvisioning(
			[]byte(valid),
			timestamp,
			sign(valid, timestamp),
			"hk",
			secret,
			now,
		)
		assert.Error(t, err)
	}
	for _, sig := range []string{"", "zz", strings.Repeat("0", 64)} {
		_, err := auth.VerifyProvisioning([]byte(valid), ts, sig, "hk", secret, now)
		assert.Error(t, err)
	}
	for _, input := range [][2]string{{"", secret}, {"eu", secret}, {"hk", "short"}} {
		_, err := auth.VerifyProvisioning(
			[]byte(valid),
			ts,
			sign(valid, ts),
			input[0],
			input[1],
			now,
		)
		assert.Error(t, err)
	}
	_, err = auth.VerifyProvisioning([]byte(strings.Repeat("x", 2049)), ts, "", "hk", secret, now)
	assert.Error(t, err)
}
