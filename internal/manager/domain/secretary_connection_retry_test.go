package domain

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSecretaryConnectionRetryGivenFailuresThenBacksOffWithBoundedJitter(t *testing.T) {
	for _, tc := range []struct {
		failures int
		base     time.Duration
	}{
		{math.MinInt, 5 * time.Second}, {0, 5 * time.Second}, {1, 5 * time.Second},
		{2, 10 * time.Second}, {3, 20 * time.Second}, {4, 40 * time.Second},
		{5, 80 * time.Second}, {6, 160 * time.Second}, {7, 5 * time.Minute},
		{8, 5 * time.Minute}, {math.MaxInt, 5 * time.Minute},
	} {
		delay := SecretaryConnectionRetryDelay(tc.failures, "attempt-one")
		assert.GreaterOrEqual(t, delay, tc.base*4/5)
		assert.LessOrEqual(t, delay, min(tc.base*6/5, 5*time.Minute))
	}
	assert.Equal(t, 8, SecretaryConnectionMaxRetries)
}

func TestSecretaryConnectionRetryGivenSameAttemptThenRetainsItsJitter(t *testing.T) {
	first := SecretaryConnectionRetryDelay(3, "attempt-one")
	assert.Equal(t, first, SecretaryConnectionRetryDelay(3, "attempt-one"))
	assert.NotEqual(t, first, SecretaryConnectionRetryDelay(3, "attempt-two"))
	assert.Positive(t, SecretaryConnectionRetryDelay(1, ""))
}
