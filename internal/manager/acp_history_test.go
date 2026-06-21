package manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

//nolint:gocyclo // This regression fixture keeps the observed frame boundary cases together.
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
			raw: json.RawMessage(
				`{"id":6,"result":{"usage":{"inputTokens":79974,"totalTokens":80224,"outputTokens":250,"thoughtTokens":0,"cachedReadTokens":78848},"stopReason":"end_turn"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    12,
			raw: json.RawMessage(
				`{"method":"session/update","params":{"update":{"content":{"text":"（竖起耳朵，认真回话）主人让奴才记住的是 **blue-mango**，汪！","type":"text"},"sessionUpdate":"agent_message_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    11,
			raw: json.RawMessage(
				`{"method":"session/update","params":{"update":{"content":{"text":"The user is testing if I remember the pass phrase.","type":"text"},"sessionUpdate":"agent_thought_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    10,
			raw: json.RawMessage(
				`{"id":5,"result":{"usage":{"inputTokens":59489,"totalTokens":59695,"outputTokens":206,"thoughtTokens":0,"cachedReadTokens":58496},"stopReason":"end_turn"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    9,
			raw: json.RawMessage(
				`{"method":"session/update","params":{"update":{"content":{"text":"\n\nOK","type":"text"},"sessionUpdate":"agent_message_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    8,
			raw: json.RawMessage(
				`{"method":"session/update","params":{"update":{"content":{"text":"The passphrase was already saved, so no duplicate was added. Now I just reply OK as instructed.","type":"text"},"sessionUpdate":"agent_thought_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    7,
			raw: json.RawMessage(
				`{"method":"session/update","params":{"update":{"kind":"other","title":"memory add: memory","content":[{"type":"content","content":{"text":"Memory add (memory)\nPreview: Passphrase: \"blue-mango\"","type":"text"}}],"locations":[],"toolCallId":"tc-7eefddd1fdea","sessionUpdate":"tool_call"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    6,
			raw: json.RawMessage(
				`{"method":"session/update","params":{"update":{"content":{"text":"The user wants me to remember the passphrase blue-mango and reply only OK. Let me save this to memory first.","type":"text"},"sessionUpdate":"agent_thought_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    5,
			raw: json.RawMessage(
				`{"id":4,"result":{"usage":{"inputTokens":19507,"totalTokens":19578,"outputTokens":71,"thoughtTokens":0,"cachedReadTokens":19456},"stopReason":"end_turn"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    4,
			raw: json.RawMessage(
				`{"method":"session/update","params":{"update":{"content":{"text":"（摇尾巴）奴才给主人请安了，汪！","type":"text"},"sessionUpdate":"agent_message_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`,
			),
		},
		{
			stream: domain.TransportStreamPaxdToManager,
			seq:    3,
			raw: json.RawMessage(
				`{"method":"session/update","params":{"update":{"content":{"text":"The user wants a playful dog greeting.","type":"text"},"sessionUpdate":"agent_thought_chunk"},"sessionId":"cdf174bc-65d6-4ccb-87f1-762fdc31b42b"},"jsonrpc":"2.0"}`,
			),
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
		t.Fatalf(
			"messages = %+v, want 7 rows: 3 turns of thought/message plus one tool call",
			messages,
		)
	}

	type projectedRow struct {
		messageType string
		text        string
		partType    string
		rawContains string
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
		if len(parts) != 1 || parts[0].PartIndex != 0 {
			t.Fatalf("parts for %s = %+v, want exactly one part", msg.MessageID, parts)
		}
		got = append(got, projectedRow{
			messageType: msg.MessageType,
			text:        parts[0].Text,
			partType:    parts[0].PartType,
			rawContains: string(parts[0].PayloadJSON),
		})
	}

	want := []projectedRow{
		{
			messageType: "agent_thought_chunk",
			text:        "The user wants a playful dog greeting.",
			partType:    domain.MessagePartText,
		},
		{
			messageType: "agent_message_chunk",
			text:        "（摇尾巴）奴才给主人请安了，汪！",
			partType:    domain.MessagePartText,
		},
		{
			messageType: "agent_thought_chunk",
			text:        "The user wants me to remember the passphrase blue-mango and reply only OK. Let me save this to memory first.The passphrase was already saved, so no duplicate was added. Now I just reply OK as instructed.",
			partType:    domain.MessagePartText,
		},
		{
			messageType: "tool_call",
			partType:    domain.MessagePartRawJSON,
			rawContains: `"title":"memory add: memory"`,
		},
		{
			messageType: "agent_message_chunk",
			text:        "\n\nOK",
			partType:    domain.MessagePartText,
		},
		{
			messageType: "agent_thought_chunk",
			text:        "The user is testing if I remember the pass phrase.",
			partType:    domain.MessagePartText,
		},
		{
			messageType: "agent_message_chunk",
			text:        "（竖起耳朵，认真回话）主人让奴才记住的是 **blue-mango**，汪！",
			partType:    domain.MessagePartText,
		},
	}
	for i := range want {
		if got[i].messageType != want[i].messageType ||
			got[i].text != want[i].text ||
			got[i].partType != want[i].partType ||
			(want[i].rawContains != "" && !strings.Contains(got[i].rawContains, want[i].rawContains)) {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestACPHistoryProjectsUserPrompt(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent-real",
		ownerUserID: "user-real",
		nodeID:      "node-real",
		sessionID:   "sess_15e5718ff7125f2077253247f61742f173d39957ece9827d",
		store:       store,
	}

	err := projectACPUserPrompt(
		ctx,
		agent,
		[]byte(
			`{"jsonrpc":"2.0","id":7,"method":"session/prompt","params":{"sessionId":"sess_15e5718ff7125f2077253247f61742f173d39957ece9827d","prompt":[{"type":"text","text":"hello from user"}]}}`,
		),
	)
	if err != nil {
		t.Fatalf("project user prompt: %v", err)
	}

	messages, err := store.ListMessages(ctx, agent.agentID, agent.sessionID, 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one user prompt", messages)
	}
	msg := messages[0]
	if msg.Direction != domain.MessageDirectionUserToAgent ||
		msg.Role != "user" ||
		msg.MessageType != "user_message" ||
		msg.SessionID != agent.sessionID {
		t.Fatalf("message = %+v", msg)
	}
	parts, err := store.ListMessageParts(ctx, msg.MessageID)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(parts) != 1 || parts[0].Text != "hello from user" {
		t.Fatalf("parts = %+v", parts)
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
	session, err := store.CreateNodeAgentSession(
		ctx,
		domain.UserPrincipal{User: user},
		domain.CreateSessionRequest{
			NodeID:    agent.NodeID,
			AgentID:   agent.AgentID,
			SessionID: "sess_manager_1",
			NativeID:  "harness-session-1",
		},
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	raw := json.RawMessage(
		`{"method":"session/update","params":{"sessionId":"harness-session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}},"jsonrpc":"2.0"}`,
	)
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
	if len(nativeMessages) != 1 || nativeMessages[0].SessionID != session.SessionID {
		t.Fatalf(
			"native session messages = %+v, want mapped manager session id %q",
			nativeMessages,
			session.SessionID,
		)
	}
}

func TestACPHistoryProjectsNonTextSessionUpdateAsRawJSON(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	raw := json.RawMessage(
		`{"method":"session/update","params":{"sessionId":"sess-raw-1","update":{"sessionUpdate":"usage_update","usage":{"inputTokens":7,"outputTokens":3}}},"jsonrpc":"2.0"}`,
	)
	if err := projectACPTransportMessage(
		ctx,
		store,
		"agent_1",
		"user_1",
		"node_1",
		domain.TransportStreamPaxdToManager,
		7,
		"",
		raw,
	); err != nil {
		t.Fatalf("project raw update: %v", err)
	}

	messages, err := store.ListMessages(ctx, "agent_1", "sess-raw-1", 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one raw update message", messages)
	}
	msg := messages[0]
	if msg.MessageType != "usage_update" ||
		msg.RawJSON == nil ||
		string(msg.RawJSON) != string(raw) {
		t.Fatalf("message = %+v, want usage_update with raw json", msg)
	}
	parts, err := store.ListMessageParts(ctx, msg.MessageID)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(parts) != 1 ||
		parts[0].PartType != domain.MessagePartRawJSON ||
		string(parts[0].PayloadJSON) != string(raw) ||
		parts[0].Text != "" {
		t.Fatalf("parts = %+v, want one raw_json part", parts)
	}
}

func TestACPHistoryPreservesToolCallRawPayload(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	raw := json.RawMessage(
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_tool_1","update":{"content":[{"content":{"text":"{\n  \"linkedin_username\": \"test_user_debug\"\n}","type":"text"},"type":"content"}],"kind":"other","locations":[],"rawInput":{"linkedin_username":"test_user_debug"},"sessionUpdate":"tool_call","title":"mcp_linkedin_connect_with_person","toolCallId":"tc-7a75bb3b54d2"}}}`,
	)
	if err := projectACPTransportMessage(
		ctx,
		store,
		"agent_1",
		"user_1",
		"node_1",
		domain.TransportStreamPaxdToManager,
		9,
		"",
		raw,
	); err != nil {
		t.Fatalf("project tool call: %v", err)
	}

	messages, err := store.ListMessages(ctx, "agent_1", "sess_tool_1", 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].MessageType != "tool_call" {
		t.Fatalf("messages = %+v, want one tool_call", messages)
	}
	parts, err := store.ListMessageParts(ctx, messages[0].MessageID)
	if err != nil {
		t.Fatalf("list parts: %v", err)
	}
	if len(parts) != 1 ||
		parts[0].PartType != domain.MessagePartRawJSON ||
		!strings.Contains(
			string(parts[0].PayloadJSON),
			`"title":"mcp_linkedin_connect_with_person"`,
		) ||
		!strings.Contains(
			string(parts[0].PayloadJSON),
			`"rawInput":{"linkedin_username":"test_user_debug"}`,
		) ||
		parts[0].Text != "" {
		t.Fatalf("parts = %+v, want raw tool call payload", parts)
	}
	if string(messages[0].RawJSON) != string(raw) {
		t.Fatalf("message raw json = %s, want %s", messages[0].RawJSON, raw)
	}
}
