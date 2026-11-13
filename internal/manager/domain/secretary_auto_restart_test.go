package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSecretaryAutoRestartDelayGivenRepeatedRestartsThenBacksOffWithinTheBudget(t *testing.T) {
	assert.Equal(t, 15*time.Minute, SecretaryAutoRestartDelay(0))
	assert.Equal(t, time.Hour, SecretaryAutoRestartDelay(1))
	assert.Equal(t, 4*time.Hour, SecretaryAutoRestartDelay(2))
	// The delay stops growing once the budget is spent; negative input is not a shortcut.
	assert.Equal(t, 16*time.Hour, SecretaryAutoRestartDelay(SecretaryAutoRestartMax))
	assert.Equal(t, 16*time.Hour, SecretaryAutoRestartDelay(SecretaryAutoRestartMax+5))
	assert.Equal(t, 15*time.Minute, SecretaryAutoRestartDelay(-1))
}
