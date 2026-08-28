package storage

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryMessageHistoryAppendsTextDeltas(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	msg := Message{
		MessageID:   "acp:agent-1:paxd_to_manager:sess-1:turn-1:assistant",
		AgentID:     "agent-1",
		SessionID:   "sess-1",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		Role:        "assistant",
		MessageType: "message:delta",
		TurnID:      "turn-1",
		LogicalKey:  "acp:agent-1:paxd_to_manager:sess-1:turn-1:assistant",
		RawJSON:     json.RawMessage(`{"entityType":"message","eventType":"delta"}`),
	}
	if err := store.UpsertMessage(ctx, &msg); err != nil {
		t.Fatalf("upsert message: %v", err)
	}
	if err := store.AppendMessagePartText(ctx, msg.MessageID, 0, "hel", []byte(`{"content":"hel"}`)); err != nil {
		t.Fatalf("append first delta: %v", err)
	}
	if err := store.AppendMessagePartText(ctx, msg.MessageID, 0, "lo", []byte(`{"content":"lo"}`)); err != nil {
		t.Fatalf("append second delta: %v", err)
	}
	part := store.messageParts[messagePartKey{MessageID: msg.MessageID, Index: 0}]
	if part.Text != "hello" {
		t.Fatalf("part text = %q, want hello", part.Text)
	}
	if len(store.messageParts) != 1 {
		t.Fatalf("message parts = %d, want 1", len(store.messageParts))
	}
}

func TestUserPromptMessageAdvancesSessionOrderingTimestamp(t *testing.T) {
	ctx := context.Background()
	store, node, agent := sessionReportStoreFixture(t, ctx)
	promptAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
		SessionID: "codex:prompt-native", NativeID: "prompt-native",
	}}))
	sessions, err := store.ListAgentSessions(ctx, UserPrincipal{User: User{UserID: node.OwnerUserID}}, agent.AgentID)
	require.NoError(t, err)
	managerSessionID := ""
	for i := range sessions {
		if sessions[i].NativeID == "prompt-native" {
			managerSessionID = sessions[i].SessionID
			break
		}
	}
	require.NotEmpty(t, managerSessionID)
	message := Message{
		MessageID: "msg_prompt", AgentID: agent.AgentID, SessionID: managerSessionID,
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionUserToAgent,
		Role: "user", CreatedAt: promptAt,
	}
	require.NoError(t, store.UpsertMessage(ctx, &message))

	sessions, err = store.ListAgentSessions(ctx, UserPrincipal{User: User{UserID: node.OwnerUserID}}, agent.AgentID)
	require.NoError(t, err)
	var found *AgentSession
	for i := range sessions {
		if sessions[i].SessionID == managerSessionID {
			found = &sessions[i]
			break
		}
	}
	require.NotNil(t, found)
	require.NotNil(t, found.LastUserMessageAt)
	assert.Equal(t, promptAt, *found.LastUserMessageAt)
}

func TestPostgresUserPromptUpsertTouchesSessionInSameStatement(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	script := &scriptedPostgresScript{queries: []scriptedRows{scriptedRow(scriptedMessageHistoryRow(1, "msg_prompt", now)...)}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	message := Message{
		MessageID: "msg_prompt", ConversationID: "conv_1", AgentID: "agent_1", SessionID: "sess_1",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionUserToAgent,
		Role: "user", CreatedAt: now,
	}

	require.NoError(t, store.UpsertMessage(context.Background(), &message))
	require.Len(t, script.queryTexts, 1)
	assert.Contains(t, script.queryTexts[0], "last_user_message_at")
	assert.Contains(t, script.queryTexts[0], "UPDATE agent_sessions")
}

func TestMemoryMailboxWritesMessageHistory(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	user, err := store.EnsureUser(ctx, "todd@example.com", "Todd", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	agent, err := store.RegisterAgent(ctx, user, RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	created, err := store.CreateMailboxMessage(ctx, UserPrincipal{User: user}, CreateMailboxRequest{
		AgentID: agent.AgentID,
		Message: "run tests",
	})
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	msg := store.messages[created.MessageID]
	if msg.MessageID != created.MessageID || msg.Source != domain.MessageSourceMailbox {
		t.Fatalf("history message = %+v", msg)
	}
	part := store.messageParts[messagePartKey{MessageID: created.MessageID, Index: 0}]
	if part.Text != "run tests" {
		t.Fatalf("history part = %+v", part)
	}
}

func TestMemorySecretAccessStoresManagerSessionID(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	user, err := store.EnsureUser(ctx, "todd@example.com", "Todd", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	agent, err := store.RegisterAgent(ctx, user, RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	session, err := store.CreateNodeAgentSession(
		ctx,
		UserPrincipal{User: user},
		CreateSessionRequest{
			NodeID:    agent.NodeID,
			AgentID:   agent.AgentID,
			SessionID: "sess_manager_1",
			NativeID:  "harness-session-1",
		},
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := store.RecordSecretAccess(ctx, SecretAccessEvent{
		SecretID:  "secret_1",
		NodeID:    agent.NodeID,
		AgentID:   agent.AgentID,
		SessionID: session.NativeID,
		Action:    "read_value",
		Result:    "allowed",
	}); err != nil {
		t.Fatalf("record secret access: %v", err)
	}
	if len(store.secretAccess) != 1 || store.secretAccess[0].SessionID != session.SessionID {
		t.Fatalf(
			"secret access = %+v, want manager session id %q",
			store.secretAccess,
			session.SessionID,
		)
	}
}

func TestMemoryMessageHistoryLogicalKeyUpsertAndSessionTranslation(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	user, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	agent, err := store.RegisterAgent(ctx, user, RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	session, err := store.CreateNodeAgentSession(
		ctx,
		UserPrincipal{User: user},
		CreateSessionRequest{
			NodeID:    agent.NodeID,
			AgentID:   agent.AgentID,
			SessionID: "sess_manager",
			NativeID:  "sess_native",
		},
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	first := Message{
		MessageID:  "msg_first",
		AgentID:    agent.AgentID,
		SessionID:  session.NativeID,
		Source:     domain.MessageSourceACPTunnel,
		Direction:  domain.MessageDirectionAgentToUser,
		LogicalKey: "turn:1:assistant",
		RawJSON:    json.RawMessage(`{"first":true}`),
	}
	if err := store.UpsertMessage(ctx, &first); err != nil {
		t.Fatalf("upsert first message: %v", err)
	}
	replacement := Message{
		MessageID:  "msg_replacement",
		AgentID:    agent.AgentID,
		SessionID:  session.SessionID,
		Source:     domain.MessageSourceACPTunnel,
		Direction:  domain.MessageDirectionAgentToUser,
		LogicalKey: "turn:1:assistant",
		RawJSON:    json.RawMessage(`{"replacement":true}`),
	}
	if err := store.UpsertMessage(ctx, &replacement); err != nil {
		t.Fatalf("upsert replacement message: %v", err)
	}
	if replacement.MessageID != first.MessageID || replacement.ID != first.ID {
		t.Fatalf("replacement = %+v, want original id/message", replacement)
	}

	messages, err := store.ListMessages(ctx, agent.AgentID, session.SessionID, 10)
	if err != nil {
		t.Fatalf("list messages by manager id: %v", err)
	}
	if len(messages) != 1 || messages[0].MessageID != first.MessageID {
		t.Fatalf("manager session messages = %+v", messages)
	}
	messages, err = store.ListMessages(ctx, agent.AgentID, session.NativeID, 10)
	if err != nil {
		t.Fatalf("list messages by native id: %v", err)
	}
	if len(messages) != 1 || messages[0].SessionID != session.SessionID {
		t.Fatalf("native session messages = %+v", messages)
	}
}

func TestMemoryListMessagesReturnsLatestLimitChronologically(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	for _, messageID := range []string{"msg_1", "msg_2", "msg_3", "msg_4"} {
		message := Message{
			MessageID:  messageID,
			AgentID:    "agent_1",
			SessionID:  "session_1",
			Source:     domain.MessageSourceACPTunnel,
			Direction:  domain.MessageDirectionAgentToUser,
			LogicalKey: messageID,
		}
		if err := store.UpsertMessage(ctx, &message); err != nil {
			t.Fatalf("upsert message %s: %v", messageID, err)
		}
	}

	messages, err := store.ListMessages(ctx, "agent_1", "session_1", 2)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 ||
		messages[0].MessageID != "msg_3" ||
		messages[1].MessageID != "msg_4" {
		t.Fatalf("messages = %+v, want latest two in chronological order", messages)
	}
}

func TestMemoryListMessageHistoryPageLoadsOlderMessages(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	for _, messageID := range []string{"msg_1", "msg_2", "msg_3", "msg_4"} {
		message := Message{
			MessageID:  messageID,
			AgentID:    "agent_1",
			SessionID:  "session_1",
			Source:     domain.MessageSourceACPTunnel,
			Direction:  domain.MessageDirectionAgentToUser,
			LogicalKey: messageID,
		}
		if err := store.UpsertMessage(ctx, &message); err != nil {
			t.Fatalf("upsert message %s: %v", messageID, err)
		}
	}

	latest, err := store.ListMessageHistoryPage(ctx, "agent_1", "session_1", 0, 2)
	if err != nil {
		t.Fatalf("list latest history page: %v", err)
	}
	if len(latest.Messages) != 2 ||
		latest.Messages[0].MessageID != "msg_3" ||
		latest.Messages[1].MessageID != "msg_4" ||
		!latest.HasMore ||
		latest.NextBeforeID != latest.Messages[0].ID {
		t.Fatalf("latest page = %+v", latest)
	}

	older, err := store.ListMessageHistoryPage(
		ctx,
		"agent_1",
		"session_1",
		latest.NextBeforeID,
		2,
	)
	if err != nil {
		t.Fatalf("list older history page: %v", err)
	}
	if len(older.Messages) != 2 ||
		older.Messages[0].MessageID != "msg_1" ||
		older.Messages[1].MessageID != "msg_2" ||
		older.HasMore ||
		older.NextBeforeID != 0 {
		t.Fatalf("older page = %+v", older)
	}
}

func TestPostgresListMessagesQueriesLatestAndReturnsChronologically(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{{
			columns: []string{
				"id", "message_id", "conversation_id", "owner_user_id", "node_id",
				"agent_id", "session_id", "source", "direction", "role", "status",
				"message_type", "parent_message_id", "turn_id", "response_id",
				"logical_key", "raw_json", "session_seq", "conversation_seq", "created_at", "updated_at",
			},
			values: [][]driver.Value{
				scriptedMessageHistoryRow(4, "msg_4", now),
				scriptedMessageHistoryRow(3, "msg_3", now),
			},
		}},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	messages, err := store.ListMessages(context.Background(), "agent_1", "", 2)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 ||
		messages[0].MessageID != "msg_3" ||
		messages[1].MessageID != "msg_4" {
		t.Fatalf("messages = %+v, want latest two in chronological order", messages)
	}
	if len(script.queryTexts) != 1 {
		t.Fatalf("queries = %d, want 1", len(script.queryTexts))
	}
	query := strings.Join(strings.Fields(script.queryTexts[0]), " ")
	if !strings.Contains(query, "ORDER BY id DESC LIMIT $2") {
		t.Fatalf("query = %q, want latest messages selected first", query)
	}
}

func TestPostgresListMessageHistoryPageUsesBeforeIDAndProbesForMore(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{{
			columns: []string{
				"id", "message_id", "conversation_id", "owner_user_id", "node_id",
				"agent_id", "session_id", "source", "direction", "role", "status",
				"message_type", "parent_message_id", "turn_id", "response_id",
				"logical_key", "raw_json", "session_seq", "conversation_seq", "created_at", "updated_at",
			},
			values: [][]driver.Value{
				scriptedMessageHistoryRow(3, "msg_3", now),
				scriptedMessageHistoryRow(2, "msg_2", now),
				scriptedMessageHistoryRow(1, "msg_1", now),
			},
		}},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	page, err := store.ListMessageHistoryPage(context.Background(), "agent_1", "", 4, 2)
	if err != nil {
		t.Fatalf("list history page: %v", err)
	}
	if len(page.Messages) != 2 ||
		page.Messages[0].MessageID != "msg_2" ||
		page.Messages[1].MessageID != "msg_3" ||
		!page.HasMore ||
		page.NextBeforeID != 2 {
		t.Fatalf("page = %+v", page)
	}
	query := strings.Join(strings.Fields(script.queryTexts[0]), " ")
	if !strings.Contains(query, "agent_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3") {
		t.Fatalf("query = %q, want before_id keyset pagination", query)
	}
}

func scriptedMessageHistoryRow(id int64, messageID string, now time.Time) []driver.Value {
	return []driver.Value{
		id,
		messageID,
		"",
		"",
		"",
		"agent_1",
		"session_1",
		string(domain.MessageSourceACPTunnel),
		string(domain.MessageDirectionAgentToUser),
		"assistant",
		"",
		"",
		"",
		"",
		"",
		messageID,
		[]byte(`{}`),
		int64(0),
		int64(0),
		now,
		now,
	}
}

func TestMessageHistoryValidationAndCloneIsolation(t *testing.T) {
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()
	if err := store.UpsertMessage(ctx, &Message{AgentID: "agent_1"}); err == nil {
		t.Fatal("upsert message without message_id succeeded")
	}
	if err := store.UpsertMessagePart(ctx, &MessagePart{
		MessageID:   "msg_1",
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		PayloadJSON: json.RawMessage(`{bad-json}`),
	}); err == nil {
		t.Fatal("upsert message part with invalid payload succeeded")
	}

	part := MessagePart{
		MessageID:   "msg_1",
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		PayloadJSON: json.RawMessage(`{"value":1}`),
	}
	if err := store.UpsertMessagePart(ctx, &part); err != nil {
		t.Fatalf("upsert part: %v", err)
	}
	listed, err := store.ListMessageParts(ctx, "msg_1")
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	listed[0].PayloadJSON[0] = '['
	again, err := store.ListMessageParts(ctx, "msg_1")
	if err != nil {
		t.Fatalf("list parts again: %v", err)
	}
	if string(again[0].PayloadJSON) != `{"value":1}` {
		t.Fatalf("stored payload mutated through list result: %s", again[0].PayloadJSON)
	}
}

func TestScanMessageAndMessagePartRows(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	updated := now.Add(time.Minute)
	msg, err := scanMessage(fakeRow{
		int64(7),
		"msg_1",
		"conv_1",
		"usr_owner",
		"node_1",
		"agent_1",
		"sess_1",
		domain.MessageSourceACPTunnel,
		domain.MessageDirectionAgentToUser,
		"assistant",
		"completed",
		"message",
		"parent_1",
		"turn_1",
		"resp_1",
		"logic_1",
		[]byte(`{"event":"completed"}`),
		int64(11),
		int64(3),
		now,
		updated,
	})
	if err != nil {
		t.Fatalf("scan message: %v", err)
	}
	if msg.ID != 7 ||
		msg.ConversationID != "conv_1" ||
		msg.Direction != domain.MessageDirectionAgentToUser ||
		msg.SessionSeq != 11 ||
		msg.ConversationSeq != 3 ||
		string(msg.RawJSON) != `{"event":"completed"}` {
		t.Fatalf("message = %+v", msg)
	}

	part, err := scanMessagePart(fakeRow{
		int64(8),
		"msg_1",
		1,
		domain.MessagePartArtifact,
		"stdout",
		[]byte(`{"text":"stdout"}`),
		"artifact://stdout",
		now,
		updated,
	})
	if err != nil {
		t.Fatalf("scan message part: %v", err)
	}
	if part.PartIndex != 1 ||
		part.ArtifactURI != "artifact://stdout" ||
		string(part.PayloadJSON) != `{"text":"stdout"}` {
		t.Fatalf("part = %+v", part)
	}
}

func TestHistoryFromMailboxMapsDirectionAndDefaultsPayload(t *testing.T) {
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	assistantMsg, assistantPart := historyFromMailbox(MailboxMessage{
		MessageID:       "msg_agent",
		OwnerUserID:     "usr_owner",
		NodeID:          "node_1",
		AgentID:         "agent_1",
		SessionID:       "sess_1",
		Message:         "done",
		MessageType:     "message",
		Status:          "completed",
		Direction:       "node_to_user",
		ParentMessageID: "parent_1",
		TurnID:          "turn_1",
		ResponseID:      "resp_1",
		Payload:         json.RawMessage(`{"content":"done"}`),
		CreatedAt:       now,
	})
	if assistantMsg.Direction != domain.MessageDirectionAgentToUser ||
		assistantMsg.Role != "assistant" ||
		assistantMsg.LogicalKey != "mailbox:msg_agent" ||
		assistantPart.Text != "done" ||
		string(assistantPart.PayloadJSON) != `{"content":"done"}` {
		t.Fatalf("assistant history = %+v %+v", assistantMsg, assistantPart)
	}

	userMsg, userPart := historyFromMailbox(MailboxMessage{
		MessageID:   "msg_user",
		OwnerUserID: "usr_owner",
		AgentID:     "agent_1",
		Message:     "run tests",
		CreatedAt:   now,
	})
	if userMsg.Direction != domain.MessageDirectionUserToAgent ||
		userMsg.Role != "user" ||
		string(userMsg.RawJSON) != `{}` ||
		string(userPart.PayloadJSON) != `{}` {
		t.Fatalf("user history = %+v %+v", userMsg, userPart)
	}
}
