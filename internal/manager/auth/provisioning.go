package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type RegionalAssignment struct {
	UserID      string `json:"user_id"`
	IdentityKey string `json:"identity_key"`
	Region      string `json:"region"`
}

// VerifyProvisioning binds the exact body, destination region and a short time
// window. Replays are safe because provisioning itself is idempotent.
func VerifyProvisioning(
	body []byte,
	timestamp, signature, region, secret string,
	now time.Time,
) (RegionalAssignment, error) {
	var assignment RegionalAssignment
	if (region != "us" && region != "hk") || len(secret) < 32 || len(body) > 2048 {
		return assignment, domain.ErrUnauthorized
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds < now.Unix()-30 || seconds > now.Unix()+5 {
		return assignment, domain.ErrUnauthorized
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("pax-region-ensure-v1\n" + timestamp + "\n"))
	_, _ = mac.Write(body)
	provided, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(mac.Sum(nil), provided) {
		return assignment, domain.ErrUnauthorized
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&assignment); err != nil {
		return assignment, domain.ErrUnauthorized
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return assignment, domain.ErrUnauthorized
	}
	if assignment.Region != region || !strings.HasPrefix(assignment.UserID, "usr_") ||
		len(assignment.UserID) <= 4 ||
		len(assignment.UserID) > 128 {
		return assignment, domain.ErrUnauthorized
	}
	email := assignment.IdentityKey
	if email != domain.NormalizeEmail(email) || !strings.Contains(email, "@") || len(email) > 254 {
		return assignment, domain.ErrUnauthorized
	}
	return assignment, nil
}
