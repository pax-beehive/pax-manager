package storage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func setSecretaryNotificationSession(
	t *testing.T,
	store notificationTestStore,
	mode string,
) string {
	t.Helper()
	source, id := "", "session"
	if mode == "marker" {
		source = domain.SecretaryInternalSource
	}
	if mode == "prefix" {
		id = domain.SecretaryInternalSessionPrefix + "notification-test"
	}
	if memory, ok := store.(*MemoryStore); ok {
		session := memory.sessions[sessionKey("agent", "session")]
		delete(memory.sessions, sessionKey("agent", "session"))
		session.Source, session.SessionID = source, id
		memory.sessions[sessionKey("agent", id)] = session
	} else {
		_, err := store.(*PostgresStore).db.ExecContext(t.Context(), `UPDATE agent_sessions SET source=$1,session_id=$2`, source, id)
		require.NoError(t, err)
	}
	return id
}

func TestSecretaryNotificationsGivenQueuedCompletionThenInternalMarkerFencesDelivery(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, mode := range []string{"marker", "prefix"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				now := time.Now().UTC()
				store, owner, _ := completionFixture(t, backend, func() time.Time { return now })
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, "queued"),
				)
				require.NoError(t, err)
				saveCompletionPrompt(t, store, owner)
				terminal := completionMessage(
					owner,
					"terminal",
					"end_turn",
					json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
				)
				require.NoError(
					t,
					store.(completionHistoryWriter).UpsertMessage(t.Context(), &terminal),
				)
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, time.Minute)
				require.NoError(t, err)
				require.Len(t, jobs, 1)
				setSecretaryNotificationSession(t, store, mode)
				allowed, err := store.(notifications.DeliveryAuthorizer).CanDeliverTurnNotification(
					t.Context(),
					jobs[0],
				)
				require.NoError(t, err)
				assert.False(t, allowed)
			})
		}
	}
}

func TestSecretaryNotificationsGivenInternalExecutionThenSkipsOnlyNormalCompletionPush(
	t *testing.T,
) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, mode := range []string{"interactive", "marker", "prefix"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				now := time.Now().UTC()
				store, owner, _ := completionFixture(t, backend, func() time.Time { return now })
				id := setSecretaryNotificationSession(t, store, mode)
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, "secretary"),
				)
				require.NoError(t, err)
				prompt := completionMessage(
					owner,
					"prompt",
					domain.MessageTypeUser,
					json.RawMessage(`{"id":11,"method":"session/prompt"}`),
				)
				prompt.SessionID, prompt.Role, prompt.Direction = id, "user", domain.MessageDirectionUserToAgent
				writer := store.(completionHistoryWriter)
				require.NoError(t, writer.UpsertMessage(t.Context(), &prompt))
				terminal := completionMessage(
					owner,
					"terminal",
					"end_turn",
					json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
				)
				terminal.SessionID = id
				require.NoError(t, writer.UpsertMessage(t.Context(), &terminal))
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, time.Minute)
				require.NoError(t, err)
				if mode == "interactive" {
					require.Len(t, jobs, 1)
					allowed, authErr := store.(notifications.DeliveryAuthorizer).CanDeliverTurnNotification(
						t.Context(),
						jobs[0],
					)
					require.NoError(t, authErr)
					assert.True(t, allowed)
				} else {
					assert.Empty(t, jobs)
				}
			})
		}
	}
}

func TestSecretaryNotificationsGivenInternalToolApprovalThenStillNotifiesActionableRequest(
	t *testing.T,
) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, mode := range []string{"marker", "prefix"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				now := time.Now().UTC()
				store, owner := approvalNotificationFixture(
					t,
					backend,
					func() time.Time { return now },
				)
				id := setSecretaryNotificationSession(t, store, mode)
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, "approval"),
				)
				require.NoError(t, err)
				approval, err := store.CreateApproval(
					t.Context(),
					Node{NodeID: "node", OwnerUserID: owner},
					CreateApprovalRequest{
						AgentID: "agent", SessionID: id, Title: "Confirm tool operation", Operation: "session/request_permission", ResourceType: "acp_permission",
					},
				)
				require.NoError(t, err)
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, time.Minute)
				require.NoError(t, err)
				require.Len(t, jobs, 1)
				assert.Equal(t, "approval.required", jobs[0].Payload.Type)
				assert.Equal(t, approval.ApprovalID, jobs[0].Payload.ApprovalID)
				allowed, err := store.CanDeliverApprovalNotification(t.Context(), jobs[0])
				require.NoError(t, err)
				assert.True(t, allowed)
			})
		}
	}
}
