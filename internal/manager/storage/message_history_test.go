package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
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
