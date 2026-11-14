package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerGivenRecipientWithoutFeatureThenSuppressesDelivery(t *testing.T) {
	now := time.Now().UTC()
	receiver := Receiver{
		ID: "receiver", UserID: "owner", Revision: 1, Channel: ChannelWebPush,
		Enabled: true, Subscription: testSubscription(t),
	}
	delivery := Delivery{
		ID: "job", UserID: "owner", ReceiverID: "receiver", ReceiverRevision: 1,
		Channel: ChannelWebPush, State: StatePending, NextAttemptAt: now, ExpiresAt: now.Add(time.Hour),
	}
	for _, allowed := range []bool{true, false} {
		store := &outboxStub{
			jobs:     []LeasedDelivery{{Delivery: delivery, Attempts: 1}},
			receiver: RegistrationRecord{Receiver: receiver, VAPIDKeyID: "key"},
		}
		sender := &senderStub{result: AttemptResult{Status: 201}}
		var asked string
		worker := Worker{
			Store: store, Sender: sender, KeyID: "key", Now: func() time.Time { return now },
			UserAllowed: func(_ context.Context, userID string) bool {
				asked = userID
				return allowed
			},
		}
		require.NoError(t, worker.Drain(t.Context()))
		assert.Equal(t, "owner", asked)
		assert.Equal(t, allowed, sender.calls == 1)
		if !allowed {
			assert.True(t, store.result.Suppressed)
			assert.Equal(t, "feature_disabled", store.result.ErrorCategory)
		}
	}
}
