package notifications

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWebPushGivenProviderStatusWhenClassifiedThenOnlyExpiredSubscriptionsAreInvalidated(
	t *testing.T,
) {
	cases := []struct {
		status  int
		outcome Outcome
	}{
		{200, OutcomeAccepted}, {201, OutcomeAccepted}, {204, OutcomeAccepted},
		{404, OutcomeGone}, {410, OutcomeGone},
		{0, OutcomeRetry}, {408, OutcomeRetry}, {425, OutcomeRetry}, {429, OutcomeRetry},
		{500, OutcomeRetry}, {503, OutcomeRetry}, {599, OutcomeRetry},
		{400, OutcomeRejected}, {401, OutcomeRejected}, {403, OutcomeRejected},
		{413, OutcomeRejected}, {301, OutcomeRejected}, {600, OutcomeRejected},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.outcome, ClassifyWebPushStatus(tc.status), "status %d", tc.status)
	}
}

func TestDeliveryGivenProviderOutcomeWhenDecidedThenRetryIsBoundedByAttemptsAndExpiry(
	t *testing.T,
) {
	now := testCompletion().CompletedAt
	cases := []struct {
		name       string
		outcome    Outcome
		attempt    int
		retryAfter time.Duration
		remaining  time.Duration
		state      DeliveryState
		nextDelay  time.Duration
		invalidate bool
	}{
		{
			"accepted means provider accepted",
			OutcomeAccepted,
			1,
			0,
			time.Hour,
			StateAccepted,
			0,
			false,
		},
		{"gone invalidates receiver", OutcomeGone, 1, 0, time.Hour, StateFailed, 0, true},
		{"bad credentials keep receiver", OutcomeRejected, 1, 0, time.Hour, StateFailed, 0, false},
		{
			"first temporary error",
			OutcomeRetry,
			1,
			0,
			time.Hour,
			StatePending,
			10 * time.Second,
			false,
		},
		{
			"second temporary error",
			OutcomeRetry,
			2,
			0,
			time.Hour,
			StatePending,
			20 * time.Second,
			false,
		},
		{
			"later temporary error",
			OutcomeRetry,
			5,
			0,
			time.Hour,
			StatePending,
			160 * time.Second,
			false,
		},
		{
			"respect provider delay",
			OutcomeRetry,
			1,
			time.Minute,
			time.Hour,
			StatePending,
			time.Minute,
			false,
		},
		{
			"do not shorten backoff",
			OutcomeRetry,
			2,
			time.Second,
			time.Hour,
			StatePending,
			20 * time.Second,
			false,
		},
		{
			"negative delay ignored",
			OutcomeRetry,
			1,
			-time.Second,
			time.Hour,
			StatePending,
			10 * time.Second,
			false,
		},
		{"attempt limit", OutcomeRetry, MaxAttempts, 0, time.Hour, StateFailed, 0, false},
		{"overflowing attempt", OutcomeRetry, 1000, 0, time.Hour, StateFailed, 0, false},
		{"invalid attempt", OutcomeRetry, 0, 0, time.Hour, StateFailed, 0, false},
		{"expired task", OutcomeRetry, 1, 0, 0, StateExpired, 0, false},
		{"retry reaches expiry", OutcomeRetry, 1, 0, 10 * time.Second, StateExpired, 0, false},
		{"retry after expiry", OutcomeRetry, 1, 2 * time.Hour, time.Hour, StateExpired, 0, false},
		{"unknown outcome", Outcome("unknown"), 1, 0, time.Hour, StateFailed, 0, false},
		{"gone after expiry still invalidates", OutcomeGone, 1, 0, 0, StateFailed, 0, true},
		{"accepted after expiry stays accepted", OutcomeAccepted, 1, 0, 0, StateAccepted, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := DecideDelivery(
				tc.outcome,
				tc.attempt,
				now,
				now.Add(tc.remaining),
				tc.retryAfter,
			)
			assert.Equal(t, tc.state, decision.State)
			assert.Equal(t, tc.invalidate, decision.InvalidateReceiver)
			if tc.nextDelay > 0 {
				assert.Equal(t, now.Add(tc.nextDelay), decision.NextAttemptAt)
			} else {
				assert.True(t, decision.NextAttemptAt.IsZero())
			}
		})
	}
}
