package notifications

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSubscription(t *testing.T) WebPushSubscription {
	t.Helper()
	privateKey := make([]byte, 32)
	privateKey[31] = 1
	key, err := ecdh.P256().NewPrivateKey(privateKey)
	require.NoError(t, err)
	return WebPushSubscription{
		Endpoint: "https://fcm.googleapis.com/wp/example-receiver",
		Keys: WebPushKeys{
			P256DH: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		},
	}
}

func testCompletion() TurnCompletion {
	return TurnCompletion{
		UserID: "usr_one", NodeID: "node_one", AgentID: "agent_one",
		SessionID: "session_one", TurnID: "turn_one",
		CompletedAt: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
	}
}

func TestWebPushSubscriptionGivenBrowserSubscriptionWhenValidated(t *testing.T) {
	subscription := testSubscription(t)
	assert.NoError(t, subscription.Validate())

	t.Run("padded URL base64 keys are accepted", func(t *testing.T) {
		subscription := testSubscription(t)
		for _, value := range []*string{&subscription.Keys.P256DH, &subscription.Keys.Auth} {
			decoded, err := base64.RawURLEncoding.DecodeString(*value)
			require.NoError(t, err)
			*value = base64.URLEncoding.EncodeToString(decoded)
		}
		assert.NoError(t, subscription.Validate())
	})

	cases := []struct {
		name   string
		change func(*WebPushSubscription)
	}{
		{"missing endpoint", func(s *WebPushSubscription) { s.Endpoint = "" }},
		{"malformed endpoint", func(s *WebPushSubscription) { s.Endpoint = "https://%zz/path" }},
		{
			"unencrypted endpoint",
			func(s *WebPushSubscription) { s.Endpoint = "http://push.example/token" },
		},
		{"relative endpoint", func(s *WebPushSubscription) { s.Endpoint = "/push/token" }},
		{"missing host", func(s *WebPushSubscription) { s.Endpoint = "https:///token" }},
		{
			"credentials",
			func(s *WebPushSubscription) { s.Endpoint = "https://user:pass@push.example/token" },
		},
		{"fragment", func(s *WebPushSubscription) { s.Endpoint += "#secret" }},
		{
			"nonstandard port",
			func(s *WebPushSubscription) { s.Endpoint = "https://push.example:8443/token" },
		},
		{"IP endpoint", func(s *WebPushSubscription) { s.Endpoint = "https://127.0.0.1/token" }},
		{"IPv6 endpoint", func(s *WebPushSubscription) { s.Endpoint = "https://[::1]/token" }},
		{"local host", func(s *WebPushSubscription) { s.Endpoint = "https://localhost/token" }},
		{
			"oversized endpoint",
			func(s *WebPushSubscription) { s.Endpoint += strings.Repeat("x", 4096) },
		},
		{"missing public key", func(s *WebPushSubscription) { s.Keys.P256DH = "" }},
		{"malformed public key", func(s *WebPushSubscription) { s.Keys.P256DH = "%%%" }},
		{"invalid curve point", func(s *WebPushSubscription) {
			s.Keys.P256DH = base64.RawURLEncoding.EncodeToString(make([]byte, 65))
		}},
		{"malformed auth secret", func(s *WebPushSubscription) { s.Keys.Auth = "%%%" }},
		{"short auth secret", func(s *WebPushSubscription) { s.Keys.Auth = "AA" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subscription := testSubscription(t)
			tc.change(&subscription)
			assert.ErrorIs(t, subscription.Validate(), ErrInvalidSubscription)
		})
	}
}

func TestTurnCompletionGivenValidEventWhenBuiltThenIdentityIsStableAndPayloadIsMinimal(
	t *testing.T,
) {
	event := testCompletion()
	notification, err := BuildTurnNotification(event)
	require.NoError(t, err)
	// Console derives this v1 ID to dismiss an exact read turn on the local device.
	assert.Equal(
		t,
		"ntf_c7d867a0e700d09be71a83529ee7c00cfcd98de06a3681edfe27794c3c0b7d7c",
		notification.ID,
	)
	assert.Equal(t, event.UserID, notification.UserID)
	assert.Equal(t, event.CompletedAt.Add(NotificationTTL), notification.ExpiresAt)
	assert.Equal(t, "turn.completed", notification.Payload.Type)
	assert.Equal(t, notification.ID, notification.Payload.Tag)
	assert.Equal(t, notification.ID, notification.Payload.NotificationID)
	assert.Equal(
		t,
		"/?session_id=session_one",
		notification.Payload.URL,
	)

	replayed := event
	replayed.CompletedAt = event.CompletedAt.Add(time.Second)
	replayedNotification, err := BuildTurnNotification(replayed)
	require.NoError(t, err)
	assert.Equal(t, notification.ID, replayedNotification.ID)

	encoded, err := json.Marshal(notification.Payload)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), event.UserID)
	assert.NotContains(t, string(encoded), "endpoint")
	assert.NotContains(t, string(encoded), "keys")
	assert.NotContains(t, string(encoded), "prompt")

	for _, field := range []string{"user", "agent", "session", "turn"} {
		t.Run("distinct "+field+" has distinct identity", func(t *testing.T) {
			other := event
			switch field {
			case "user":
				other.UserID += "_two"
			case "agent":
				other.AgentID += "_two"
			case "session":
				other.SessionID += "_two"
			case "turn":
				other.TurnID += "_two"
			}
			otherNotification, err := BuildTurnNotification(other)
			require.NoError(t, err)
			assert.NotEqual(t, notification.ID, otherNotification.ID)
		})
	}
}

func TestTurnNotificationGivenEscapedIdentityThenMatchesConsoleV1ReadReceipt(t *testing.T) {
	assert.Equal(
		t,
		"ntf_085f6bb6bb24524315402f7f042d19ef4a82e4b600c034f3099067feb40f6607",
		stableID(
			"ntf_",
			"user<&>",
			"agent\u2028line\u2029",
			"\u4f1a\u8bdd/\"\\",
			"turn\n\t\b\f\r\x00",
		),
	)
}

func TestTurnCompletionGivenSpecialIDsWhenBuiltThenDeepLinkCannotEscapeSession(t *testing.T) {
	event := testCompletion()
	event.SessionID = "session/with?query#fragment"
	event.AgentID = "agent&nodeId=other"
	notification, err := BuildTurnNotification(event)
	require.NoError(t, err)
	link, err := url.Parse(notification.Payload.URL)
	require.NoError(t, err)
	assert.Empty(t, link.Host)
	assert.Empty(t, link.Fragment)
	assert.Equal(t, "/", link.Path)
	assert.Equal(t, event.SessionID, link.Query().Get("session_id"))
	assert.Len(t, link.Query(), 1)
}

func TestTurnCompletionGivenReplyPreviewThenBodyIsBoundedAndIdentityUnchanged(t *testing.T) {
	base, err := BuildTurnNotification(testCompletion())
	require.NoError(t, err)
	for _, scenario := range []struct{ name, preview, want string }{
		{"empty", "", "Your turn has finished."},
		{"controls", " \n\x00\t ", "Your turn has finished."},
		{"single line", "Done.\n\tAll checks passed.\x00", "Done. All checks passed."},
		{"unicode", strings.Repeat("\U0001f600", 250), strings.Repeat("\U0001f600", 199) + "\u2026"},
		{"exact limit", strings.Repeat("a", 200), strings.Repeat("a", 200)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			event := testCompletion()
			event.ReplyPreview = scenario.preview
			got, err := BuildTurnNotification(event)
			require.NoError(t, err)
			assert.Equal(t, scenario.want, got.Payload.Body)
			assert.Equal(t, base.ID, got.ID)
			assert.Equal(t, base.Payload.URL, got.Payload.URL)
		})
	}
}

func TestTurnCompletionGivenInvalidIdentityWhenBuiltThenRejected(t *testing.T) {
	for _, field := range []string{"user", "node", "agent", "session", "turn", "time"} {
		t.Run(field, func(t *testing.T) {
			event := testCompletion()
			switch field {
			case "user":
				event.UserID = ""
			case "node":
				event.NodeID = ""
			case "agent":
				event.AgentID = ""
			case "session":
				event.SessionID = ""
			case "turn":
				event.TurnID = ""
			case "time":
				event.CompletedAt = time.Time{}
			}
			_, err := BuildTurnNotification(event)
			assert.ErrorIs(t, err, ErrInvalidCompletion)
		})
	}
}

func TestFanoutGivenMixedReceiversWhenPlannedThenOnlyCurrentOwnersActiveReceiversRemain(
	t *testing.T,
) {
	event := testCompletion()
	notification, err := BuildTurnNotification(event)
	require.NoError(t, err)
	now := event.CompletedAt.Add(time.Minute)
	receiver := Receiver{
		ID: "receiver_one", UserID: event.UserID, Revision: 1,
		Channel: ChannelWebPush, Enabled: true, Subscription: testSubscription(t),
	}
	second := receiver
	second.ID = "receiver_two"
	otherOwner := receiver
	otherOwner.ID, otherOwner.UserID = "receiver_other", "usr_other"
	disabled := receiver
	disabled.ID, disabled.Enabled = "receiver_disabled", false
	expired := receiver
	expired.ID, expired.ExpiresAt = "receiver_expired", &now
	invalid := receiver
	invalid.ID, invalid.Subscription.Endpoint = "receiver_invalid", "http://invalid"
	unsupported := receiver
	unsupported.ID, unsupported.Channel = "receiver_future", "apns"
	unversioned := receiver
	unversioned.ID, unversioned.Revision = "receiver_unversioned", 0
	anonymous := receiver
	anonymous.ID = ""

	deliveries := PlanDeliveries(notification, []Receiver{
		receiver, second, receiver, otherOwner, disabled, expired,
		invalid, unsupported, unversioned, anonymous,
	}, now)
	require.Len(t, deliveries, 2)
	assert.Equal(t, receiver.ID, deliveries[0].ReceiverID)
	assert.Equal(t, receiver.Revision, deliveries[0].ReceiverRevision)
	assert.Equal(t, second.ID, deliveries[1].ReceiverID)
	assert.NotEqual(t, deliveries[0].ID, deliveries[1].ID)
	assert.Equal(t, notification.ID, deliveries[0].NotificationID)
	assert.Equal(t, StatePending, deliveries[0].State)
	assert.Equal(t, now, deliveries[0].NextAttemptAt)
	assert.Equal(t, notification.ExpiresAt, deliveries[0].ExpiresAt)

	receiver.Revision++
	replanned := PlanDeliveries(notification, []Receiver{receiver}, now)
	require.Len(t, replanned, 1)
	assert.Equal(t, deliveries[0].ID, replanned[0].ID)
	assert.Equal(t, receiver.Revision, replanned[0].ReceiverRevision)
	assert.Empty(t, PlanDeliveries(notification, []Receiver{receiver}, notification.ExpiresAt))
	assert.Empty(t, PlanDeliveries(Notification{}, []Receiver{receiver}, now))
}

func TestDeliveryGivenReceiverChangesWhenAuthorizedThenStaleJobsAreSuppressed(t *testing.T) {
	event := testCompletion()
	notification, err := BuildTurnNotification(event)
	require.NoError(t, err)
	now := event.CompletedAt
	receiver := Receiver{
		ID: "receiver_one", UserID: event.UserID, Revision: 1,
		Channel: ChannelWebPush, Enabled: true, Subscription: testSubscription(t),
	}
	deliveries := PlanDeliveries(notification, []Receiver{receiver}, now)
	require.Len(t, deliveries, 1)
	delivery := deliveries[0]
	assert.True(t, delivery.CanSendTo(receiver, now))

	cases := []struct {
		name   string
		change func(*Receiver)
	}{
		{"different owner", func(r *Receiver) { r.UserID = "usr_other" }},
		{"different receiver", func(r *Receiver) { r.ID = "receiver_other" }},
		{"new revision", func(r *Receiver) { r.Revision++ }},
		{"disabled receiver", func(r *Receiver) { r.Enabled = false }},
		{"expired receiver", func(r *Receiver) { r.ExpiresAt = &now }},
		{"different channel", func(r *Receiver) { r.Channel = "apns" }},
		{"invalid subscription", func(r *Receiver) { r.Subscription.Endpoint = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := receiver
			tc.change(&changed)
			assert.False(t, delivery.CanSendTo(changed, now))
		})
	}
	assert.False(t, delivery.CanSendTo(receiver, delivery.ExpiresAt))
	delivery.NextAttemptAt = now.Add(time.Minute)
	assert.False(t, delivery.CanSendTo(receiver, now))
	delivery.NextAttemptAt = now
	delivery.State = StateAccepted
	assert.False(t, delivery.CanSendTo(receiver, now))
}
