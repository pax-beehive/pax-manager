package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"time"
)

const SecretaryConnectionMaxRetries = 8

// SecretaryConnectionRetryDelay uses the random attempt identity for stable jitter.
// The selected deadline is persisted by the store, independently of worker polling.
func SecretaryConnectionRetryDelay(failures int, attemptID string) time.Duration {
	step := min(max(failures, 1), 7) - 1
	base := min((5*time.Second)<<step, 5*time.Minute)
	lower := base * 4 / 5
	upper := min(base*6/5, 5*time.Minute)
	hash := sha256.Sum256([]byte(attemptID))
	jitter := binary.BigEndian.Uint64(hash[:8]) % (uint64(upper-lower) + 1)
	return lower + time.Duration(jitter)
}
