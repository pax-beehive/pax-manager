package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type completionHistoryWriter interface {
	UpsertMessage(context.Context, *Message) error
	UpsertMessagePart(context.Context, *MessagePart) error
}

func savePreviewText(t *testing.T, store completionHistoryWriter, msg Message, text string) {
	t.Helper()
	require.NoError(t, store.UpsertMessage(t.Context(), &msg))
	require.NoError(t, store.UpsertMessagePart(t.Context(), &MessagePart{
		MessageID: msg.MessageID, PartIndex: 0, PartType: domain.MessagePartText, Text: text,
	}))
}

func TestTurnNotificationGivenHistoryThenPreviewsOnlyLatestReplyInCompletedTurn(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Now().UTC()
			store, owner, other := completionFixture(t, backend, func() time.Time { return now })
			_, err := store.RegisterNotificationReceiver(
				t.Context(),
				owner,
				notificationRegistration(t, "preview"),
			)
			require.NoError(t, err)
			saveCompletionPrompt(t, store, owner)
			writer := store.(completionHistoryWriter)
			base := Message{
				OwnerUserID: owner,
				NodeID:      "node",
				AgentID:     "agent",
				SessionID:   "session",
				TurnID:      "turn",
				Source:      domain.MessageSourceACPTunnel,
				Direction:   domain.MessageDirectionAgentToUser,
				Role:        "assistant",
				MessageType: "agent_message_chunk",
			}
			for i, text := range []string{"Earlier commentary.", "Final reply\n\twith a preview."} {
				msg := base
				msg.MessageID = fmt.Sprintf("reply-%d", i)
				savePreviewText(t, writer, msg, text)
			}
			for i, revise := range []func(*Message){
				func(m *Message) { m.OwnerUserID = other },
				func(m *Message) { m.NodeID = "another-node" },
				func(m *Message) { m.AgentID = "another-agent" },
				func(m *Message) { m.SessionID = "another-session" },
				func(m *Message) { m.TurnID = "another-turn" },
				func(m *Message) { m.Role = "user" },
				func(m *Message) { m.MessageType = "agent_thought_chunk" },
				func(m *Message) { m.MessageType = "tool_call_update" },
				func(m *Message) { m.Direction = domain.MessageDirectionUserToAgent },
				func(m *Message) { m.Source = "mailbox" },
			} {
				msg := base
				msg.MessageID = fmt.Sprintf("excluded-%d", i)
				revise(&msg)
				savePreviewText(t, writer, msg, "Excluded private text")
			}
			msg := completionMessage(
				owner,
				"terminal",
				"end_turn",
				json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
			)
			require.NoError(t, writer.UpsertMessage(t.Context(), &msg))
			jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, time.Minute)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			assert.Equal(t, "Final reply with a preview.", jobs[0].Payload.Body)
			assert.Equal(t, "/sessions/session?agentId=agent&nodeId=node", jobs[0].Payload.URL)
		})
	}
}

func TestTurnNotificationGivenNoReplyThenUsesGenericBody(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Now().UTC()
			store, owner, _ := completionFixture(t, backend, func() time.Time { return now })
			_, err := store.RegisterNotificationReceiver(
				t.Context(),
				owner,
				notificationRegistration(t, "empty"),
			)
			require.NoError(t, err)
			saveCompletionPrompt(t, store, owner)
			msg := completionMessage(
				owner,
				"terminal",
				"end_turn",
				json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
			)
			require.NoError(t, store.(completionHistoryWriter).UpsertMessage(t.Context(), &msg))
			jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, time.Minute)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			assert.Equal(t, "Your turn has finished.", jobs[0].Payload.Body)
		})
	}
}

func TestTurnNotificationGivenReplyVariantsThenKeepsPreviewBounded(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, scenario := range []struct{ name, kind, partType, text, want string }{
			{"alternate delta", "message_delta", "text", "Final response.", "Final response."},
			{"large unicode", "agent_message_chunk", "text", strings.Repeat("\u56de", 2048), strings.Repeat("\u56de", 199) + "\u2026"},
			{"empty reply", "agent_message_chunk", "text", "", "Your turn has finished."},
			{"non text part", "agent_message_chunk", "raw_json", "Do not preview", "Your turn has finished."},
		} {
			t.Run(backend+"/"+scenario.name, func(t *testing.T) {
				now := time.Now().UTC()
				store, owner, _ := completionFixture(t, backend, func() time.Time { return now })
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, "variant"),
				)
				require.NoError(t, err)
				saveCompletionPrompt(t, store, owner)
				writer := store.(completionHistoryWriter)
				reply := Message{
					MessageID:   "reply",
					OwnerUserID: owner,
					NodeID:      "node",
					AgentID:     "agent",
					SessionID:   "session",
					TurnID:      "turn",
					Source:      domain.MessageSourceACPTunnel,
					Direction:   domain.MessageDirectionAgentToUser,
					Role:        "assistant",
					MessageType: scenario.kind,
				}
				require.NoError(t, writer.UpsertMessage(t.Context(), &reply))
				require.NoError(
					t,
					writer.UpsertMessagePart(
						t.Context(),
						&MessagePart{
							MessageID: "reply",
							PartIndex: 0,
							PartType:  scenario.partType,
							Text:      scenario.text,
						},
					),
				)
				terminal := completionMessage(
					owner,
					"terminal",
					"end_turn",
					json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
				)
				require.NoError(t, writer.UpsertMessage(t.Context(), &terminal))
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, time.Minute)
				require.NoError(t, err)
				require.Len(t, jobs, 1)
				assert.Equal(t, scenario.want, jobs[0].Payload.Body)
			})
		}
	}
}

func TestTurnNotificationGivenPreviewReadFailureThenTerminalCanRetryAtomically(t *testing.T) {
	now := time.Now().UTC()
	store, owner, _ := completionFixture(t, "postgres", func() time.Time { return now })
	s := store.(*PostgresStore)
	_, err := store.RegisterNotificationReceiver(
		t.Context(),
		owner,
		notificationRegistration(t, "retry-preview"),
	)
	require.NoError(t, err)
	saveCompletionPrompt(t, store, owner)
	require.NoError(t, s.gormDB.Exec(`ALTER TABLE message_parts RENAME TO paused_parts`).Error)
	terminal := completionMessage(
		owner,
		"terminal",
		"end_turn",
		json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
	)
	require.Error(t, s.UpsertMessage(t.Context(), &terminal))
	var count int64
	require.NoError(
		t,
		s.gormDB.Table("messages").Where("message_id = ?", "terminal").Count(&count).Error,
	)
	assert.Zero(t, count)
	require.NoError(t, s.gormDB.Exec(`ALTER TABLE paused_parts RENAME TO message_parts`).Error)
	require.NoError(t, s.UpsertMessage(t.Context(), &terminal))
	jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, time.Minute)
	require.NoError(t, err)
	assert.Len(t, jobs, 1)
}
