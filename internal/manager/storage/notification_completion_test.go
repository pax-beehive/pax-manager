package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func completionFixture(
	t *testing.T,
	backend string,
	now func() time.Time,
) (notificationTestStore, string, string) {
	t.Helper()
	store, owner, other := newOutboxFixture(t, backend, now)
	if s, ok := store.(*MemoryStore); ok {
		s.nodes["node"] = Node{NodeID: "node", OwnerUserID: owner}
		s.agents["agent"] = Agent{AgentID: "agent", NodeID: "node", OwnerUserID: owner}
		s.sessions[sessionKey("agent", "session")] = AgentSession{
			AgentID:   "agent",
			SessionID: "session",
			Transport: "manager",
		}
	} else {
		s := store.(*PostgresStore)
		s.db, _ = s.gormDB.DB()
		for _, ddl := range []string{
			`CREATE TABLE nodes(node_id TEXT PRIMARY KEY, owner_user_id TEXT, deleted_at TIMESTAMPTZ)`,
			`CREATE TABLE agents(agent_id TEXT PRIMARY KEY, node_id TEXT, owner_user_id TEXT, deleted_at TIMESTAMPTZ)`,
			`CREATE TABLE agent_sessions(id BIGSERIAL PRIMARY KEY, agent_id TEXT, session_id TEXT, transport TEXT, conversation_id TEXT, last_user_message_at TIMESTAMPTZ)`,
			`CREATE TABLE messages(id BIGSERIAL PRIMARY KEY,message_id TEXT UNIQUE NOT NULL,conversation_id TEXT,owner_user_id TEXT REFERENCES users(user_id),node_id TEXT,agent_id TEXT,session_id TEXT,source TEXT,direction TEXT,role TEXT,status TEXT,message_type TEXT,parent_message_id TEXT,turn_id TEXT,response_id TEXT,logical_key TEXT UNIQUE,raw_json JSONB,session_seq BIGINT,conversation_seq BIGINT,created_at TIMESTAMPTZ,updated_at TIMESTAMPTZ)`,
			`CREATE TABLE message_parts(id BIGSERIAL PRIMARY KEY,message_id TEXT,part_index INTEGER,part_type TEXT,text TEXT,payload_json JSONB,artifact_uri TEXT,created_at TIMESTAMPTZ,updated_at TIMESTAMPTZ,UNIQUE(message_id,part_index))`,
		} {
			require.NoError(t, s.gormDB.Exec(ddl).Error)
		}
		require.NoError(t, s.gormDB.Exec(`INSERT INTO nodes VALUES ('node',?,NULL)`, owner).Error)
		require.NoError(t, s.gormDB.Exec(`INSERT INTO agents VALUES ('agent','node',?,NULL)`, owner).Error)
		require.NoError(t, s.gormDB.Exec(`INSERT INTO agent_sessions(agent_id,session_id,transport) VALUES ('agent','session','manager')`).Error)
	}
	return store, owner, other
}

func TestTurnNotificationGivenNonSuccessOrUncorrelatedResponseThenDoesNotNotify(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, scenario := range []struct {
			name   string
			revise func(*Message)
		}{
			{"cancelled", func(m *Message) { m.RawJSON = json.RawMessage(`{"id":11,"result":{"stopReason":"cancelled"}}`) }},
			{"failed", func(m *Message) {
				m.RawJSON = json.RawMessage(`{"id":11,"error":{"code":-1},"result":{"stopReason":"end_turn"}}`)
			}},
			{"permission_response", func(m *Message) { m.RawJSON = json.RawMessage(`{"id":11,"result":{"outcome":"selected"}}`) }},
			{"unknown_request", func(m *Message) { m.RawJSON = json.RawMessage(`{"id":12,"result":{"stopReason":"end_turn"}}`) }},
			{"wrong_id_type", func(m *Message) { m.RawJSON = json.RawMessage(`{"id":"11","result":{"stopReason":"end_turn"}}`) }},
			{"missing_id", func(m *Message) { m.RawJSON = json.RawMessage(`{"result":{"stopReason":"end_turn"}}`) }},
			{"null_id", func(m *Message) { m.RawJSON = json.RawMessage(`{"id":null,"result":{"stopReason":"end_turn"}}`) }},
			{"notification", func(m *Message) {
				m.RawJSON = json.RawMessage(`{"id":11,"method":"session/update","result":{"stopReason":"end_turn"}}`)
			}},
			{"wrong_turn", func(m *Message) { m.TurnID = "other-turn" }},
			{"wrong_owner", func(m *Message) { m.OwnerUserID = "b" }},
			{"wrong_node", func(m *Message) { m.NodeID = "other-node" }},
			{"not_terminal", func(m *Message) { m.MessageType = "tool_call_update" }},
			{"user_frame", func(m *Message) { m.Direction = domain.MessageDirectionUserToAgent }},
			{"other_source", func(m *Message) { m.Source = "mailbox" }},
		} {
			t.Run(backend+"/"+scenario.name, func(t *testing.T) {
				now := time.Now().UTC()
				store, owner, _ := completionFixture(t, backend, func() time.Time { return now })
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, "skip"),
				)
				require.NoError(t, err)
				saveCompletionPrompt(t, store, owner)
				msg := completionMessage(
					owner,
					"terminal",
					"end_turn",
					json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
				)
				scenario.revise(&msg)
				require.NoError(t, store.(interface {
					UpsertMessage(context.Context, *Message) error
				}).UpsertMessage(t.Context(), &msg))
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
				require.NoError(t, err)
				assert.Empty(t, jobs)
			})
		}
	}
}

func TestTurnNotificationGivenReceiverEligibilityAndLostSessionAccessThenSuppressesDelivery(
	t *testing.T,
) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Now().UTC()
			store, owner, _ := completionFixture(t, backend, func() time.Time { return now })
			for _, label := range []string{"enabled", "disabled", "revoked", "expired"} {
				input := notificationRegistration(t, label)
				if label == "disabled" {
					input.Enabled = false
				}
				if label == "expired" {
					expiry := now.Add(time.Second)
					input.ExpiresAt = &expiry
				}
				record, err := store.RegisterNotificationReceiver(t.Context(), owner, input)
				require.NoError(t, err)
				if label == "revoked" {
					require.NoError(
						t,
						store.RevokeNotificationReceiver(t.Context(), owner, record.ID),
					)
				}
			}
			now = now.Add(2 * time.Second)
			saveCompletionPrompt(t, store, owner)
			msg := completionMessage(
				owner,
				"terminal",
				"end_turn",
				json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
			)
			require.NoError(t, store.(interface {
				UpsertMessage(context.Context, *Message) error
			}).UpsertMessage(t.Context(), &msg))
			jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			authorizer := store.(notifications.DeliveryAuthorizer)
			allowed, err := authorizer.CanDeliverTurnNotification(t.Context(), jobs[0])
			require.NoError(t, err)
			assert.True(t, allowed)
			foreign := jobs[0]
			foreign.UserID = "foreign"
			allowed, err = authorizer.CanDeliverTurnNotification(t.Context(), foreign)
			require.NoError(t, err)
			assert.False(t, allowed)
			if memory, ok := store.(*MemoryStore); ok {
				delete(memory.sessions, sessionKey("agent", "session"))
			} else {
				require.NoError(t, store.(*PostgresStore).gormDB.Exec(`DELETE FROM agent_sessions WHERE session_id='session'`).Error)
			}
			allowed, err = authorizer.CanDeliverTurnNotification(t.Context(), jobs[0])
			require.NoError(t, err)
			assert.False(t, allowed)
		})
	}
}

func TestTurnNotificationGivenNoReceiversAtCompletionOrExpiredCompletionThenNeverBackfills(
	t *testing.T,
) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, expired := range []bool{false, true} {
			t.Run(backend+"/"+fmt.Sprint(expired), func(t *testing.T) {
				now := time.Now().UTC()
				store, owner, _ := completionFixture(t, backend, func() time.Time { return now })
				saveCompletionPrompt(t, store, owner)
				if expired {
					_, err := store.RegisterNotificationReceiver(
						t.Context(),
						owner,
						notificationRegistration(t, "early"),
					)
					require.NoError(t, err)
				}
				msg := completionMessage(
					owner,
					"terminal",
					"end_turn",
					json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
				)
				if expired {
					msg.CreatedAt = now.Add(-2 * time.Hour)
				}
				writer := store.(interface {
					UpsertMessage(context.Context, *Message) error
				})
				require.NoError(t, writer.UpsertMessage(t.Context(), &msg))
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, "late"),
				)
				require.NoError(t, err)
				msg.MessageID, msg.LogicalKey = "replay", "replay"
				require.NoError(t, writer.UpsertMessage(t.Context(), &msg))
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
				require.NoError(t, err)
				assert.Empty(t, jobs)
			})
		}
	}
}

func TestTurnNotificationGivenFanoutFailureThenRollsBackTerminalAndCompletionTogether(
	t *testing.T,
) {
	for _, failure := range []string{"ledger", "receivers", "deliveries", "invalid_receiver"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Now().UTC()
			store, owner, _ := completionFixture(t, "postgres", func() time.Time { return now })
			s := store.(*PostgresStore)
			_, err := store.RegisterNotificationReceiver(
				t.Context(),
				owner,
				notificationRegistration(t, "phone"),
			)
			require.NoError(t, err)
			saveCompletionPrompt(t, store, owner)
			table := map[string]string{"ledger": "notification_completions", "receivers": "notification_receivers", "deliveries": "notification_deliveries"}[failure]
			if table != "" {
				require.NoError(
					t,
					s.gormDB.Exec("ALTER TABLE "+table+" RENAME TO paused_"+table).Error,
				)
			} else {
				require.NoError(t, s.gormDB.Exec(`UPDATE notification_receivers SET record_json='"invalid"'::jsonb`).Error)
			}
			msg := completionMessage(
				owner,
				"terminal",
				"end_turn",
				json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
			)
			require.Error(t, s.UpsertMessage(t.Context(), &msg))
			var count int64
			require.NoError(
				t,
				s.gormDB.Table("messages").Where("message_id = ?", "terminal").Count(&count).Error,
			)
			assert.Zero(t, count)
			if table != "" {
				require.NoError(
					t,
					s.gormDB.Exec("ALTER TABLE paused_"+table+" RENAME TO "+table).Error,
				)
				require.NoError(t, s.UpsertMessage(t.Context(), &msg))
				jobs, err := s.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
				require.NoError(t, err)
				require.Len(t, jobs, 1)
			}
		})
	}
}

func TestNormalCompletionGivenIncompleteOrInvalidMetadataThenRejectsIt(t *testing.T) {
	valid := completionMessage(
		"owner",
		"terminal",
		"end_turn",
		json.RawMessage(`{"id":"prompt","result":{"stopReason":"end_turn"}}`),
	)
	for _, revise := range []func(*Message){func(m *Message) { m.RawJSON = []byte("[") }, func(m *Message) { m.Role = "tool" }, func(m *Message) { m.TurnID = "" }, func(m *Message) { m.SessionID = "" }, func(m *Message) { m.NodeID = "" }, func(m *Message) { m.AgentID = "" }, func(m *Message) { m.OwnerUserID = "" }} {
		msg := valid
		revise(&msg)
		assert.Empty(t, normalCompletionRPCID(msg))
	}
	assert.False(t, matchesCompletionPrompt([]byte("invalid"), "1"))
	_, err := notificationForMessage(valid, "")
	require.ErrorIs(t, err, notifications.ErrInvalidCompletion)
	valid.CreatedAt = time.Now()
	valid.TurnID = strings.Repeat("x", 513)
	_, err = notificationForMessage(valid, "")
	require.ErrorIs(t, err, notifications.ErrInvalidCompletion)
}

func TestTurnNotificationGivenConcurrentCompletionsThenOwnerForeignKeysDoNotDeadlock(t *testing.T) {
	now := time.Now().UTC()
	fixture, owner, _ := completionFixture(t, "postgres", func() time.Time { return now })
	old := fixture.(*PostgresStore)
	var schema string
	require.NoError(t, old.db.QueryRowContext(t.Context(), `SELECT current_schema()`).Scan(&schema))
	cfg, err := pgx.ParseConfig(os.Getenv("PAX_NOTIFICATIONS_TEST_DATABASE_URL"))
	require.NoError(t, err)
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	store := NewPostgresStore(db, func() time.Time { return now })
	_, err = store.RegisterNotificationReceiver(
		t.Context(),
		owner,
		notificationRegistration(t, "concurrent"),
	)
	require.NoError(t, err)
	require.NoError(
		t,
		store.gormDB.Exec(`INSERT INTO agents VALUES ('agent2','node',?,NULL)`, owner).Error,
	)
	require.NoError(
		t,
		store.gormDB.Exec(
			`INSERT INTO agent_sessions(agent_id,session_id,transport) VALUES ('agent2','session2','manager')`,
		).Error,
	)
	var terminals []Message
	for _, suffix := range []string{"", "2"} {
		prompt := completionMessage(
			owner,
			"prompt"+suffix,
			domain.MessageTypeUser,
			json.RawMessage(`{"id":11,"method":"session/prompt"}`),
		)
		prompt.AgentID, prompt.SessionID = "agent"+suffix, "session"+suffix
		prompt.Role, prompt.Direction = "user", domain.MessageDirectionUserToAgent
		require.NoError(t, store.UpsertMessage(t.Context(), &prompt))
		terminal := completionMessage(
			owner,
			"terminal"+suffix,
			"end_turn",
			json.RawMessage(`{"id":11,"result":{"stopReason":"end_turn"}}`),
		)
		terminal.AgentID, terminal.SessionID = prompt.AgentID, prompt.SessionID
		terminals = append(terminals, terminal)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, msg := range terminals {
		go func(m Message) { <-start; done <- store.UpsertMessage(ctx, &m) }(msg)
	}
	close(start)
	for range terminals {
		require.NoError(t, <-done)
	}
	jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	_, err = store.EnqueueNotificationTest(t.Context(), owner, jobs[0].ReceiverID)
	require.NoError(t, err, "completion alerts must not throttle manual test notifications")
}

func completionMessage(owner, id, kind string, raw json.RawMessage) Message {
	return Message{
		MessageID:   id,
		OwnerUserID: owner,
		NodeID:      "node",
		AgentID:     "agent",
		SessionID:   "session",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		Role:        "assistant",
		MessageType: kind,
		TurnID:      "turn",
		LogicalKey:  id,
		RawJSON:     raw,
	}
}

func saveCompletionPrompt(t *testing.T, store notificationTestStore, owner string) {
	t.Helper()
	msg := completionMessage(
		owner,
		"prompt",
		domain.MessageTypeUser,
		json.RawMessage(
			`{"jsonrpc":"2.0","id":11,"method":"session/prompt","params":{"sessionId":"session","prompt":[{"type":"text","text":"private prompt"}]}}`,
		),
	)
	msg.Role, msg.Direction = "user", domain.MessageDirectionUserToAgent
	require.NoError(t, store.(interface {
		UpsertMessage(ctx context.Context, msg *Message) error
	}).UpsertMessage(t.Context(), &msg))
}

func TestTurnNotificationGivenDurableNormalCompletionThenFansOutOnceWithoutABrowser(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			store, owner, other := completionFixture(t, backend, func() time.Time { return now })
			for _, suffix := range []string{"phone", "mac"} {
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, suffix),
				)
				require.NoError(t, err)
			}
			_, err := store.RegisterNotificationReceiver(
				t.Context(),
				other,
				notificationRegistration(t, "other"),
			)
			require.NoError(t, err)
			saveCompletionPrompt(t, store, owner)
			writer := store.(interface {
				UpsertMessage(ctx context.Context, msg *Message) error
			})
			msg := completionMessage(
				owner,
				"terminal",
				"end_turn",
				json.RawMessage(`{"jsonrpc":"2.0","id":11,"result":{"stopReason":"end_turn"}}`),
			)
			require.NoError(t, writer.UpsertMessage(t.Context(), &msg))
			jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
			require.NoError(t, err)
			require.Len(t, jobs, 2)
			for _, job := range jobs {
				assert.Equal(t, owner, job.UserID)
				assert.Equal(t, "turn.completed", job.Payload.Type)
				assert.Equal(t, "/?session_id=session", job.Payload.URL)
				assert.NotContains(t, job.Payload.Body, "private")
				require.NoError(
					t,
					store.CompleteNotificationDelivery(
						t.Context(),
						job,
						notifications.AttemptResult{Status: 201},
					),
				)
			}
			now = now.Add(time.Minute)
			_, err = store.RegisterNotificationReceiver(
				t.Context(),
				owner,
				notificationRegistration(t, "late"),
			)
			require.NoError(t, err)
			msg.MessageID, msg.LogicalKey = "replayed-terminal", "replayed-terminal"
			require.NoError(t, writer.UpsertMessage(t.Context(), &msg))
			jobs, err = store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
			require.NoError(t, err)
			assert.Empty(t, jobs)
		})
	}
}
