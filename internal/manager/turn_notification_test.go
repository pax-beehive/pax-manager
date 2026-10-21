package manager

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

type failingTerminalStore struct {
	Store
	fail bool
}

func (s *failingTerminalStore) UpsertMessage(ctx context.Context, msg *domain.Message) error {
	if s.fail && msg.MessageType == "end_turn" {
		return errors.New("terminal storage unavailable")
	}
	return s.Store.UpsertMessage(ctx, msg)
}

type turnNotificationSender struct{ payloads []notifications.Payload }

func (s *turnNotificationSender) Send(
	_ context.Context,
	_ notifications.RegistrationRecord,
	p notifications.Payload,
	_ time.Time,
) notifications.AttemptResult {
	s.payloads = append(s.payloads, p)
	return notifications.AttemptResult{Status: 201}
}

func TestTurnNotificationGivenDisconnectedBrowserAndStorageRetryThenSendsFromDurableInboundOnce(
	t *testing.T,
) {
	srv, _ := testServer(t, "notify@example.invalid")
	fixture := testNodeAgent(t, srv, "notify@example.invalid")
	createConversationTestSession(t, srv, fixture, "notify-session", "native-session")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	private := make([]byte, 32)
	private[31] = 1
	key, err := ecdh.P256().NewPrivateKey(private)
	require.NoError(t, err)
	_, err = srv.notificationRegistry.RegisterNotificationReceiver(
		t.Context(),
		principal.User.UserID,
		notifications.Registration{
			Subscription: notifications.WebPushSubscription{
				Endpoint: "https://fcm.googleapis.com/wp/inbound-notification",
				Keys: notifications.WebPushKeys{
					P256DH: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
					Auth:   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
				},
			},
			Enabled: true, Permission: "granted", VAPIDKeyID: "key",
		},
	)
	require.NoError(t, err)
	store := &failingTerminalStore{Store: srv.store}
	agent := &ACPTunnelAgent{
		agentID:     fixture.agentID,
		nodeID:      fixture.nodeID,
		ownerUserID: principal.User.UserID,
		store:       store,
	}
	prompt := json.RawMessage(
		`{"jsonrpc":"2.0","id":41,"method":"session/prompt","params":{"sessionId":"notify-session","prompt":[{"type":"text","text":"private prompt"}]}}`,
	)
	require.NoError(
		t,
		projectACPUserPromptForSessionTurn(
			t.Context(),
			agent,
			"notify-session",
			"turn-notify",
			prompt,
		),
	)
	frame := reliablemq.Frame{
		Key:     reliablemq.FrameKey{QueueID: "queue", Stream: reliablemq.StreamACP, Seq: 1},
		Payload: json.RawMessage(`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"end_turn"}}`),
		Metadata: reliablemq.Metadata{
			"manager_session_id": "notify-session",
			"turn_id":            "turn-notify",
		},
	}
	reply := frame
	reply.Payload = json.RawMessage(
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Final reply preview."}}}}`,
	)
	require.NoError(
		t,
		agent.dispatchReliableACPFrame(t.Context(), srv.agentACPFramePipeline(), reply),
	)
	frame.Key.Seq++
	store.fail = true
	require.ErrorContains(
		t,
		agent.dispatchReliableACPFrame(t.Context(), srv.agentACPFramePipeline(), frame),
		"terminal storage unavailable",
	)
	jobs, err := srv.notificationOutbox.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	store.fail = false
	require.NoError(
		t,
		agent.dispatchReliableACPFrame(t.Context(), srv.agentACPFramePipeline(), frame),
	)
	// A new connection/runtime projector replays the reliable terminal frame.
	srv.acpRuntime = newACPRuntimeProjector(srv.clock)
	agent = &ACPTunnelAgent{
		agentID:     fixture.agentID,
		nodeID:      fixture.nodeID,
		ownerUserID: principal.User.UserID,
		store:       store,
	}
	require.NoError(
		t,
		agent.dispatchReliableACPFrame(t.Context(), srv.agentACPFramePipeline(), frame),
	)
	sender := &turnNotificationSender{}
	worker := notifications.Worker{
		Store:  srv.notificationOutbox,
		Sender: sender,
		KeyID:  "key",
		Now:    srv.clock,
	}
	require.NoError(t, worker.Drain(t.Context()))
	require.Len(t, sender.payloads, 1)
	assert.Equal(t, "turn.completed", sender.payloads[0].Type)
	assert.Equal(t, "/?session_id=notify-session", sender.payloads[0].URL)
	assert.NotContains(t, sender.payloads[0].Body, "private prompt")
	assert.Equal(t, "Final reply preview.", sender.payloads[0].Body)
	// A delayed inbox replay retains its original receipt time and cannot target
	// a receiver that was registered after that terminal response arrived.
	require.NoError(
		t,
		projectACPUserPromptForSessionTurn(
			t.Context(),
			agent,
			"notify-session",
			"turn-old",
			prompt,
		),
	)
	frame.Metadata["turn_id"] = "turn-old"
	frame.Key.Seq++
	frame.CreatedAt = srv.clock().Add(-2 * time.Hour)
	require.NoError(
		t,
		agent.dispatchReliableACPFrame(t.Context(), srv.agentACPFramePipeline(), frame),
	)
	require.NoError(t, worker.Drain(t.Context()))
	assert.Len(t, sender.payloads, 1)
}
