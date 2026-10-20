package notifications

import "time"

const MaxAttempts = 6

type DeliveryState string

const (
	StatePending  DeliveryState = "pending"
	StateAccepted DeliveryState = "accepted"
	StateFailed   DeliveryState = "failed"
	StateExpired  DeliveryState = "expired"
)

type Outcome string

const (
	OutcomeAccepted Outcome = "accepted"
	OutcomeRetry    Outcome = "retry"
	OutcomeGone     Outcome = "gone"
	OutcomeRejected Outcome = "rejected"
)

// ClassifyWebPushStatus applies only to Web Push, not native FCM or APNs APIs.
// Zero represents a transport failure with an unknown provider acceptance state.
func ClassifyWebPushStatus(status int) Outcome {
	switch {
	case status >= 200 && status < 300:
		return OutcomeAccepted
	case status == 404 || status == 410:
		return OutcomeGone
	case status == 0 || status == 408 || status == 425 || status == 429 ||
		(status >= 500 && status < 600):
		return OutcomeRetry
	default:
		return OutcomeRejected
	}
}

type Decision struct {
	State              DeliveryState
	NextAttemptAt      time.Time
	InvalidateReceiver bool
}

// DecideDelivery receives the count including the attempt that just finished.
func DecideDelivery(
	outcome Outcome,
	attempt int,
	now time.Time,
	expiresAt time.Time,
	retryAfter time.Duration,
) Decision {
	switch outcome {
	case OutcomeAccepted:
		return Decision{State: StateAccepted}
	case OutcomeGone:
		return Decision{State: StateFailed, InvalidateReceiver: true}
	case OutcomeRetry:
		if !now.Before(expiresAt) {
			return Decision{State: StateExpired}
		}
		if attempt < 1 || attempt >= MaxAttempts {
			return Decision{State: StateFailed}
		}
		delay := 10 * time.Second * time.Duration(1<<(attempt-1))
		if retryAfter > delay {
			delay = retryAfter
		}
		next := now.UTC().Add(delay)
		if !next.Before(expiresAt) {
			return Decision{State: StateExpired}
		}
		return Decision{State: StatePending, NextAttemptAt: next}
	default:
		return Decision{State: StateFailed}
	}
}
