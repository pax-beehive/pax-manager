package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestACPHistoryProjectsInboundResultBoundariesIntoExpectedMessageRows(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent-real",
		ownerUserID: "user-real",
		nodeID:      "node-real",
		sessionID:   "cdf174bc-65d6-4ccb-87f1-762fdc31b42b",
	}

	type observedFrame struct {
		stream string
		seq    int64
		raw    json.RawMessage
	}

	// Mirrors DB/log inspection output ordered newest first. Projection must be
	// reasoned about in chronological order before asserting merged text.
	reversed := []observedFrame{
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    13,
			raw:    json.RawMessage(`{"id":6,"result":{"usage":{"inputTokens":79974,"totalTokens":80224,"outputTokens":250,"thoughtTokens":0,"cachedReadTokens":78848},"stopReason":"end_turn"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    12,
			raw:    json.RawMessage(`{"method":"session/update","params":{"update":{"content":{"text":"（竖起耳朵，认真回话）主人让奴才记住的是 **blue-mango**，汪！","type":"text"},"sessionUpdate":"agent_message_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    11,
			raw:    json.RawMessage(`{"method":"session/update","params":{"update":{"content":{"text":"The user is testing if I remember the pass phrase.","type":"text"},"sessionUpdate":"agent_thought_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    10,
			raw:    json.RawMessage(`{"id":5,"result":{"usage":{"inputTokens":59489,"totalTokens":59695,"outputTokens":206,"thoughtTokens":0,"cachedReadTokens":58496},"stopReason":"end_turn"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    9,
			raw:    json.RawMessage(`{"method":"session/update","params":{"update":{"content":{"text":"\n\nOK","type":"text"},"sessionUpdate":"agent_message_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    8,
			raw:    json.RawMessage(`{"method":"session/update","params":{"update":{"content":{"text":"The passphrase was already saved, so no duplicate was added. Now I just reply OK as instructed.","type":"text"},"sessionUpdate":"agent_thought_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    7,
			raw:    json.RawMessage(`{"method":"session/update","params":{"update":{"kind":"other","title":"memory add: memory","content":[{"type":"content","content":{"text":"Memory add (memory)\nPreview: Passphrase: \"blue-mango\"","type":"text"}}],"locations":[],"toolCallId":"tc-7eefddd1fdea","sessionUpdate":"tool_call"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    6,
			raw:    json.RawMessage(`{"method":"session/update","params":{"update":{"content":{"text":"The user wants me to remember the passphrase blue-mango and reply only OK. Let me save this to memory first.","type":"text"},"sessionUpdate":"agent_thought_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    5,
			raw:    json.RawMessage(`{"id":4,"result":{"usage":{"inputTokens":19507,"totalTokens":19578,"outputTokens":71,"thoughtTokens":0,"cachedReadTokens":19456},"stopReason":"end_turn"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    4,
			raw:    json.RawMessage(`{"method":"session/update","params":{"update":{"content":{"text":"（摇尾巴）奴才给主人请安了，汪！","type":"text"},"sessionUpdate":"agent_message_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    3,
			raw:    json.RawMessage(`{"method":"session/update","params":{"update":{"content":{"text":"The user wants a playful dog greeting.","type":"text"},"sessionUpdate":"agent_thought_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`),
		},
	}

	for i := len(reversed) - 1; i >= 0; i-- {
		frame := reversed[i]
		if frame.stream != domain.TransportStreamPaxdToManager {
			t.Fatalf("test should only use inbound frames, got stream %q", frame.stream)
		}
		if err := projectACPTransportMessage(
			ctx,
			store,
			agent.agentID,
			agent.ownerUserID,
			agent.nodeID,
			frame.stream,
			frame.seq,
			agent.historyGroupID(frame.seq, frame.raw),
			frame.raw,
		); err != nil {
			t.Fatalf("project seq %d: %v", frame.seq, err)
		}
		agent.observeHistoryBoundary(frame.raw)
	}

	messages, err := store.ListMessages(ctx, agent.agentID, agent.sessionID, 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 7 {
		t.Fatalf("messages = %+v, want 7 rows: 3 turns of thought/message plus one tool call", messages)
	}

	type projectedRow struct {
		messageType string
		text        string
	}
	got := make([]projectedRow, 0, len(messages))
	for _, msg := range messages {
		if msg.Source != domain.MessageSourceACPTunnel ||
			msg.Direction != domain.MessageDirectionAgentToUser ||
			msg.Role != "assistant" ||
			msg.OwnerUserID != agent.ownerUserID ||
			msg.NodeID != agent.nodeID ||
			msg.SessionID != agent.sessionID ||
			msg.Status != "received" ||
			msg.TurnID != "" {
			t.Fatalf("message row = %+v", msg)
		}
		parts, err := store.ListMessageParts(ctx, msg.MessageID)
		if err != nil {
			t.Fatalf("list parts for %s: %v", msg.MessageID, err)
		}
		if len(parts) != 1 || parts[0].PartIndex != 0 || parts[0].PartType != domain.MessagePartText {
			t.Fatalf("parts for %s = %+v, want exactly one text part", msg.MessageID, parts)
		}
		got = append(got, projectedRow{messageType: msg.MessageType, text: parts[0].Text})
	}

	want := []projectedRow{
		{messageType: "agent_thought_chunk", text: "The user wants a playful dog greeting."},
		{messageType: "agent_message_chunk", text: "（摇尾巴）奴才给主人请安了，汪！"},
		{messageType: "agent_thought_chunk", text: "The user wants me to remember the passphrase blue-mango and reply only OK. Let me save this to memory first.The passphrase was already saved, so no duplicate was added. Now I just reply OK as instructed."},
		{messageType: "tool_call", text: "Memory add (memory)\nPreview: Passphrase: \"blue-mango\""},
		{messageType: "agent_message_chunk", text: "\n\nOK"},
		{messageType: "agent_thought_chunk", text: "The user is testing if I remember the pass phrase."},
		{messageType: "agent_message_chunk", text: "（竖起耳朵，认真回话）主人让奴才记住的是 **blue-mango**，汪！"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestACPHistoryStoresManagerSessionID(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	user, err := store.EnsureUser(ctx, "todd@example.com", "Todd", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	agent, err := store.RegisterAgent(ctx, user, domain.RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	session, err := store.CreateNodeAgentSession(ctx, domain.UserPrincipal{User: user}, domain.CreateSessionRequest{
		NodeID:    agent.NodeID,
		AgentID:   agent.AgentID,
		SessionID: "sess_manager_1",
		NativeID:  "harness-session-1",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	raw := json.RawMessage(`{"method":"session/update","params":{"sessionId":"harness-session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}},"jsonrpc":"2.0"}`)
	if err := projectACPTransportMessage(
		ctx,
		store,
		agent.AgentID,
		user.UserID,
		agent.NodeID,
		domain.TransportStreamPaxdToManager,
		1,
		"seq:1",
		raw,
	); err != nil {
		t.Fatalf("project message: %v", err)
	}

	messages, err := store.ListMessages(ctx, agent.AgentID, session.SessionID, 100)
	if err != nil {
		t.Fatalf("list manager session messages: %v", err)
	}
	if len(messages) != 1 || messages[0].SessionID != session.SessionID {
		t.Fatalf("messages = %+v, want manager session id %q", messages, session.SessionID)
	}
	nativeMessages, err := store.ListMessages(ctx, agent.AgentID, session.NativeID, 100)
	if err != nil {
		t.Fatalf("list native session messages: %v", err)
	}
	if len(nativeMessages) != 0 {
		t.Fatalf("native session messages = %+v, want none", nativeMessages)
	}
}
