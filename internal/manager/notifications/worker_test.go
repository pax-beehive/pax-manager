package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type outboxStub struct {
	Outbox
	jobs        []LeasedDelivery
	receiver    RegistrationRecord
	result      AttemptResult
	completed   int
	err         error
	loadErr     error
	completeErr error
	onClaim     func()
}

func (s *outboxStub) ClaimNotificationDeliveries(
	context.Context,
	int,
	time.Duration,
) ([]LeasedDelivery, error) {
	if s.onClaim != nil {
		s.onClaim()
	}
	if len(s.jobs) == 0 {
		return nil, s.err
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	return []LeasedDelivery{job}, s.err
}

func (s *outboxStub) LoadNotificationReceiver(
	context.Context,
	string,
	string,
) (RegistrationRecord, error) {
	return s.receiver, s.loadErr
}

func (s *outboxStub) CompleteNotificationDelivery(
	_ context.Context,
	_ LeasedDelivery,
	result AttemptResult,
) error {
	s.result = result
	s.completed++
	return s.completeErr
}

type senderStub struct {
	calls  int
	result AttemptResult
}

type authorizedOutboxStub struct {
	*outboxStub
	allowed bool
	authErr error
}

func (s authorizedOutboxStub) CanDeliverTurnNotification(
	context.Context,
	LeasedDelivery,
) (bool, error) {
	return s.allowed, s.authErr
}

func TestWorkerGivenTurnTaskThenRequiresCurrentSessionAuthorization(t *testing.T) {
	now := time.Now().UTC()
	for _, scenario := range []string{"allowed", "denied", "unavailable", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			stub := &outboxStub{
				jobs: []LeasedDelivery{
					{
						Delivery: Delivery{
							ID:               "job",
							UserID:           "owner",
							ReceiverID:       "receiver",
							ReceiverRevision: 1,
							Channel:          ChannelWebPush,
							State:            StatePending,
							NextAttemptAt:    now,
							ExpiresAt:        now.Add(time.Hour),
						},
						Payload: Payload{Type: "turn.completed"},
					},
				},
				receiver: RegistrationRecord{
					Receiver: Receiver{
						ID:           "receiver",
						UserID:       "owner",
						Revision:     1,
						Channel:      ChannelWebPush,
						Enabled:      true,
						Subscription: testSubscription(t),
					},
					VAPIDKeyID: "key",
				},
			}
			var store Outbox = authorizedOutboxStub{outboxStub: stub, allowed: scenario == "allowed"}
			if scenario == "unavailable" {
				store = authorizedOutboxStub{outboxStub: stub, authErr: errors.New("unavailable")}
			}
			if scenario == "unsupported" {
				store = stub
			}
			sender := &senderStub{result: AttemptResult{Status: 201}}
			err := (Worker{Store: store, Sender: sender, KeyID: "key", Now: func() time.Time { return now }}).Drain(
				t.Context(),
			)
			if scenario == "unavailable" {
				require.Error(t, err)
				assert.Zero(t, stub.completed)
			} else {
				require.NoError(t, err)
				assert.Equal(t, 1, stub.completed)
			}
			assert.Equal(t, scenario == "allowed", sender.calls == 1)
			if scenario == "denied" || scenario == "unsupported" {
				assert.Equal(t, "session_access_changed", stub.result.ErrorCategory)
			}
		})
	}
}

func (s *senderStub) Send(context.Context, RegistrationRecord, Payload, time.Time) AttemptResult {
	s.calls++
	return s.result
}

func TestWorkerGivenLeasedTaskThenRechecksReceiverBeforeSending(t *testing.T) {
	now := time.Now().UTC()
	receiver := Receiver{
		ID:           "receiver",
		UserID:       "owner",
		Revision:     1,
		Channel:      ChannelWebPush,
		Enabled:      true,
		Subscription: testSubscription(t),
	}
	delivery := Delivery{
		ID:               "job",
		UserID:           "owner",
		ReceiverID:       "receiver",
		ReceiverRevision: 1,
		Channel:          ChannelWebPush,
		State:            StatePending,
		NextAttemptAt:    now,
		ExpiresAt:        now.Add(time.Hour),
	}
	for _, test := range []struct {
		name   string
		revise func(*RegistrationRecord)
		sent   bool
	}{
		{"enabled", func(*RegistrationRecord) {}, true},
		{"revoked", func(r *RegistrationRecord) { r.Enabled = false }, false},
		{"revised", func(r *RegistrationRecord) { r.Revision++ }, false},
		{"rotated_key", func(r *RegistrationRecord) { r.VAPIDKeyID = "other" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := RegistrationRecord{Receiver: receiver, VAPIDKeyID: "key"}
			test.revise(&record)
			store := &outboxStub{
				jobs:     []LeasedDelivery{{Delivery: delivery, Attempts: 1}},
				receiver: record,
			}
			sender := &senderStub{result: AttemptResult{Status: 201}}
			worker := Worker{
				Store:  store,
				Sender: sender,
				KeyID:  "key",
				Now:    func() time.Time { return now },
			}
			require.NoError(t, worker.Drain(t.Context()))
			assert.Equal(t, 1, store.completed)
			assert.Equal(t, test.sent, sender.calls == 1)
			assert.Equal(t, !test.sent, store.result.Suppressed)
		})
	}
}
func TestWorkerGivenUnavailableStoreThenReturnsErrorAndStopsOnCancellation(t *testing.T) {
	worker := Worker{
		Store:  &outboxStub{err: errors.New("offline")},
		Sender: &senderStub{},
		Now:    time.Now,
	}
	assert.Error(t, worker.Drain(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	worker.Run(ctx)
}

func TestWorkerGivenBatchOrStorageFailureThenBoundsWorkAndReportsFailure(t *testing.T) {
	now := time.Now().UTC()
	for _, failure := range []string{"load", "complete", "none"} {
		t.Run(failure, func(t *testing.T) {
			store := &outboxStub{jobs: make([]LeasedDelivery, 10)}
			if failure == "load" {
				store.loadErr = errors.New("offline")
			}
			if failure == "complete" {
				store.completeErr = errors.New("offline")
			}
			worker := Worker{
				Store:  store,
				Sender: &senderStub{},
				Now:    func() time.Time { return now },
			}
			err := worker.Drain(t.Context())
			if failure == "none" {
				require.NoError(t, err)
				assert.Equal(t, 8, store.completed)
			} else {
				require.Error(t, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	store := &outboxStub{err: errors.New("offline"), onClaim: cancel}
	reported := false
	Worker{
		Store:   store,
		Sender:  &senderStub{},
		Now:     time.Now,
		OnError: func() { reported = true },
	}.Run(
		ctx,
	)
	assert.True(t, reported)
}
