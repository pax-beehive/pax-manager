package notifications

import (
	"context"
	"errors"
	"time"
)

var (
	ErrTestRateLimited  = errors.New("wait 30 seconds before sending another test notification")
	ErrReceiverDisabled = errors.New("enable this receiver before sending a test notification")
)

const StateSuppressed DeliveryState = "suppressed"

type LeasedDelivery struct {
	Delivery
	Payload    Payload
	Attempts   int
	LeaseToken string
}

type AttemptResult struct {
	Status        int
	ErrorCategory string
	RetryAfter    time.Duration
	Suppressed    bool
}

type Outbox interface {
	EnqueueNotificationTest(context.Context, string, string) (DeliveryMetadata, error)
	ClaimNotificationDeliveries(context.Context, int, time.Duration) ([]LeasedDelivery, error)
	LoadNotificationReceiver(context.Context, string, string) (RegistrationRecord, error)
	CompleteNotificationDelivery(context.Context, LeasedDelivery, AttemptResult) error
}

type Sender interface {
	Send(context.Context, RegistrationRecord, Payload, time.Time) AttemptResult
}

type DeliveryAuthorizer interface {
	CanDeliverTurnNotification(context.Context, LeasedDelivery) (bool, error)
}

type ApprovalDeliveryAuthorizer interface {
	CanDeliverApprovalNotification(context.Context, LeasedDelivery) (bool, error)
}

type Worker struct {
	Store   Outbox
	Sender  Sender
	KeyID   string
	Now     func() time.Time
	OnError func()
}

func (w Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := w.Drain(ctx); err != nil && w.OnError != nil {
			w.OnError()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w Worker) Drain(ctx context.Context) error {
	for range 8 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		jobs, err := w.Store.ClaimNotificationDeliveries(ctx, 1, 30*time.Second)
		if err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		job := jobs[0]
		result, err := w.attempt(ctx, job)
		if err != nil {
			return err
		}
		if err := w.Store.CompleteNotificationDelivery(ctx, job, result); err != nil {
			return err
		}
	}
	return nil
}

func (w Worker) attempt(ctx context.Context, job LeasedDelivery) (AttemptResult, error) {
	receiver, err := w.Store.LoadNotificationReceiver(ctx, job.UserID, job.ReceiverID)
	if err != nil {
		return AttemptResult{}, err
	}
	result := AttemptResult{Suppressed: true, ErrorCategory: "receiver_changed"}
	if !job.CanSendTo(receiver.Receiver, w.Now()) || receiver.RevokedAt != nil ||
		receiver.InvalidatedAt != nil ||
		receiver.VAPIDKeyID != w.KeyID {
		return result, nil
	}
	if job.Payload.Type == "turn.completed" {
		authorizer, ok := w.Store.(DeliveryAuthorizer)
		if !ok {
			return AttemptResult{Suppressed: true, ErrorCategory: "session_access_changed"}, nil
		}
		allowed, err := authorizer.CanDeliverTurnNotification(ctx, job)
		if err != nil {
			return AttemptResult{}, err
		}
		if !allowed {
			return AttemptResult{Suppressed: true, ErrorCategory: "session_access_changed"}, nil
		}
	}
	if job.Payload.Type == "approval.required" {
		authorizer, ok := w.Store.(ApprovalDeliveryAuthorizer)
		if !ok {
			return AttemptResult{Suppressed: true, ErrorCategory: "approval_not_actionable"}, nil
		}
		allowed, err := authorizer.CanDeliverApprovalNotification(ctx, job)
		if err != nil {
			return AttemptResult{}, err
		}
		if !allowed {
			return AttemptResult{Suppressed: true, ErrorCategory: "approval_not_actionable"}, nil
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return w.Sender.Send(sendCtx, receiver, job.Payload, job.ExpiresAt), nil
}

func BuildTestNotification(owner, nonce string, now time.Time) Notification {
	id := stableID("ntf_", owner, "test", nonce)
	return Notification{
		ID:        id,
		UserID:    owner,
		ExpiresAt: now.UTC().Add(5 * time.Minute),
		Payload: Payload{
			Version: 1, NotificationID: id, Type: "notification.test", Title: "Pax Workspace",
			Body: "Notifications are working on this device.", URL: "/settings/notifications", Tag: id,
			OccurredAt: now.UnixMilli(),
		},
	}
}
