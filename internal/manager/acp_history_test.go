package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/paxkit/reliablemq"

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
	if len(messages) != 8 {
		t.Fatalf(
			"messages = %+v, want 8 rows: 3 turns of thought/message plus one tool call boundary",
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
			text:        "The user wants me to remember the passphrase blue-mango and reply only OK. Let me save this to memory first.",
			partType:    domain.MessagePartText,
		},
		{
			messageType: "tool_call",
			partType:    domain.MessagePartRawJSON,
			rawContains: `"title":"memory add: memory"`,
		},
		{
			messageType: "agent_thought_chunk",
			text:        "The passphrase was already saved, so no duplicate was added. Now I just reply OK as instructed.",
			partType:    domain.MessagePartText,
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

func TestACPHistoryGivenNonTextUpdateBetweenTextChunksThenStartsANewGroup(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent_boundary",
		ownerUserID: "user_boundary",
		nodeID:      "node_boundary",
		sessionID:   "sess_boundary",
	}
	frames := []json.RawMessage{
		json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"sess_boundary","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"before tool. "}}},"jsonrpc":"2.0"}`,
		),
		json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"sess_boundary","update":{"sessionUpdate":"tool_call","toolCallId":"tool_1","title":"Read file","kind":"read"}},"jsonrpc":"2.0"}`,
		),
		json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"sess_boundary","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"after tool."}}},"jsonrpc":"2.0"}`,
		),
	}

	for i, frame := range frames {
		seq := int64(i + 1)
		err := projectACPTransportMessage(
			ctx,
			store,
			agent.agentID,
			agent.ownerUserID,
			agent.nodeID,
			domain.TransportStreamPaxdToManager,
			seq,
			agent.historyGroupID(seq, frame),
			frame,
		)
		require.NoError(t, err)
		agent.observeHistoryBoundary(frame)
	}

	messages, err := store.ListMessages(ctx, agent.agentID, agent.sessionID, 10)
	require.NoError(t, err)
	require.Len(t, messages, 3)
	assert.Equal(t, []string{
		"agent_thought_chunk",
		"tool_call",
		"agent_thought_chunk",
	}, []string{
		messages[0].MessageType,
		messages[1].MessageType,
		messages[2].MessageType,
	})

	beforeParts, err := store.ListMessageParts(ctx, messages[0].MessageID)
	require.NoError(t, err)
	afterParts, err := store.ListMessageParts(ctx, messages[2].MessageID)
	require.NoError(t, err)
	require.Len(t, beforeParts, 1)
	require.Len(t, afterParts, 1)
	assert.Equal(t, "before tool. ", beforeParts[0].Text)
	assert.Equal(t, "after tool.", afterParts[0].Text)
	assert.NotEqual(t, messages[0].MessageID, messages[2].MessageID)
}

func TestACPHistoryGivenRepeatedTextChunksThenEnsuresMessageOnce(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	sink := &countingACPHistoryTextSink{store: store}
	agent := &ACPTunnelAgent{
		agentID:     "agent_once",
		ownerUserID: "user_once",
		nodeID:      "node_once",
		sessionID:   "sess_once",
	}
	frames := []json.RawMessage{
		json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"sess_once","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"h"}}},"jsonrpc":"2.0"}`,
		),
		json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"sess_once","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"i"}}},"jsonrpc":"2.0"}`,
		),
	}

	for i, frame := range frames {
		seq := int64(i + 1)
		err := projectACPTransportMessageWithTextSink(
			ctx,
			store,
			sink,
			agent.agentID,
			agent.ownerUserID,
			agent.nodeID,
			domain.TransportStreamPaxdToManager,
			seq,
			agent.historyGroupID(seq, frame),
			"",
			frame,
		)
		require.NoError(t, err)
	}

	assert.Equal(t, 1, sink.ensureCalls)
	assert.Equal(t, 2, sink.appendCalls)
	messages, err := store.ListMessages(ctx, agent.agentID, agent.sessionID, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	parts, err := store.ListMessageParts(ctx, messages[0].MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, "hi", parts[0].Text)
}

func TestACPHistoryGivenTerminalOutputDeltasThenAggregatesByToolAndTerminal(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	})
	sink := &countingACPHistoryTextSink{store: store}
	frames := []json.RawMessage{
		json.RawMessage(
			`{"method":"session/update","params":{"update":{"_meta":{"terminal_output_delta":{"data":" M first.go\n","terminal_id":"call_1"}},"toolCallId":"call_1","sessionUpdate":"tool_call_update"},"sessionId":"sess_terminal"},"jsonrpc":"2.0"}`,
		),
		json.RawMessage(
			`{"method":"session/update","params":{"update":{"_meta":{"terminal_output_delta":{"data":" M second.go\n","terminal_id":"call_1"}},"toolCallId":"call_1","sessionUpdate":"tool_call_update"},"sessionId":"sess_terminal"},"jsonrpc":"2.0"}`,
		),
	}

	for i, frame := range frames {
		require.NoError(t, projectACPTransportMessageWithTextSink(
			ctx,
			store,
			sink,
			"agent_terminal",
			"user_terminal",
			"node_terminal",
			domain.TransportStreamPaxdToManager,
			int64(i+1),
			"",
			"",
			frame,
		))
	}

	assert.Equal(t, 1, sink.ensureCalls)
	assert.Equal(t, 2, sink.appendCalls)
	messages, err := store.ListMessages(ctx, "agent_terminal", "sess_terminal", 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "tool_call_update", messages[0].MessageType)
	assert.Contains(t, messages[0].LogicalKey, ":call_1:call_1:tool_call_update")
	assert.Contains(t, string(messages[0].RawJSON), `"data":" M first.go\n"`)

	parts, err := store.ListMessageParts(ctx, messages[0].MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, " M first.go\n M second.go\n", parts[0].Text)
}

func TestACPHistoryGivenUserFrameBetweenAgentChunksThenStartsNewAgentMessage(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent_interleave",
		ownerUserID: "user_interleave",
		nodeID:      "node_interleave",
		sessionID:   "sess_interleave",
		store:       store,
	}
	firstAgentChunk := json.RawMessage(
		`{"method":"session/update","params":{"sessionId":"sess_interleave","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"resp A first"}}},"jsonrpc":"2.0"}`,
	)
	secondAgentChunk := json.RawMessage(
		`{"method":"session/update","params":{"sessionId":"sess_interleave","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"resp B"}}},"jsonrpc":"2.0"}`,
	)
	userRequest := []byte(
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"sess_interleave","prompt":[{"type":"text","text":"request B"}]}}`,
	)

	require.NoError(t, projectACPTransportMessageWithTextSink(
		ctx,
		store,
		acpAgentHistoryTextSink{agent: agent},
		agent.agentID,
		agent.ownerUserID,
		agent.nodeID,
		domain.TransportStreamPaxdToManager,
		1,
		agent.historyGroupID(1, firstAgentChunk),
		agent.sessionID,
		firstAgentChunk,
	))
	require.NoError(t, projectACPUserPrompt(ctx, agent, userRequest))
	require.NoError(t, projectACPTransportMessageWithTextSink(
		ctx,
		store,
		acpAgentHistoryTextSink{agent: agent},
		agent.agentID,
		agent.ownerUserID,
		agent.nodeID,
		domain.TransportStreamPaxdToManager,
		2,
		agent.historyGroupID(2, secondAgentChunk),
		agent.sessionID,
		secondAgentChunk,
	))
	require.NoError(t, agent.flushHistoryText(ctx))

	messages, err := store.ListMessages(ctx, agent.agentID, agent.sessionID, 10)
	require.NoError(t, err)
	require.Len(t, messages, 3)
	agentTexts := make([]string, 0, 2)
	userTexts := make([]string, 0, 1)
	agentMessageIDs := make([]string, 0, 2)
	for _, msg := range messages {
		parts, err := store.ListMessageParts(ctx, msg.MessageID)
		require.NoError(t, err)
		require.Len(t, parts, 1)
		switch msg.Direction {
		case domain.MessageDirectionAgentToUser:
			agentTexts = append(agentTexts, parts[0].Text)
			agentMessageIDs = append(agentMessageIDs, msg.MessageID)
		case domain.MessageDirectionUserToAgent:
			userTexts = append(userTexts, parts[0].Text)
		}
	}
	require.Len(t, agentTexts, 2)
	require.Len(t, agentMessageIDs, 2)
	assert.Equal(t, []string{"resp A first", "resp B"}, agentTexts)
	assert.NotEqual(t, agentMessageIDs[0], agentMessageIDs[1])
	assert.Equal(t, []string{"request B"}, userTexts)
}

func TestACPHistoryCreatesMessagesInTransportSeqOrderWhenFramesArriveOutOfOrder(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent_order",
		ownerUserID: "user_order",
		nodeID:      "node_order",
		sessionID:   "sess_order",
	}
	producer, err := reliablemq.NewProducer(ctx, reliablemq.ProducerConfig{
		QueueID: "agent_order",
		Stream:  reliablemq.StreamACP,
	}, store)
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, producer.Close(closeCtx))
	})
	engine := reliablemq.NewEngine(
		reliablemq.Config{},
		store,
		producer,
		reliablemq.DispatcherFunc(func(dispatchCtx context.Context, frame reliablemq.Frame) error {
			if err := projectACPTransportMessageForSession(
				dispatchCtx,
				store,
				agent.agentID,
				agent.ownerUserID,
				agent.nodeID,
				domain.TransportStreamPaxdToManager,
				frame.Key.Seq,
				agent.historyGroupID(frame.Key.Seq, frame.Payload),
				agent.sessionID,
				frame.Payload,
			); err != nil {
				return err
			}
			agent.observeHistoryBoundary(frame.Payload)
			return nil
		}),
	)

	require.NoError(t, engine.Receive(ctx, reliablemq.Envelope{
		Type:    reliablemq.EnvelopeTypeData,
		QueueID: "agent_order",
		Stream:  reliablemq.StreamACP,
		Seq:     2,
		Payload: json.RawMessage(
			`{"id":4,"result":{"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2},"stopReason":"end_turn"},"jsonrpc":"2.0"}`,
		),
	}))
	messages, err := store.ListMessages(ctx, agent.agentID, agent.sessionID, 10)
	require.NoError(t, err)
	require.Empty(t, messages)

	require.NoError(t, engine.Receive(ctx, reliablemq.Envelope{
		Type:    reliablemq.EnvelopeTypeData,
		QueueID: "agent_order",
		Stream:  reliablemq.StreamACP,
		Seq:     1,
		Payload: json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"sess_order","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}},"jsonrpc":"2.0"}`,
		),
	}))

	messages, err = store.ListMessages(ctx, agent.agentID, agent.sessionID, 10)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "agent_message_chunk", messages[0].MessageType)
	assert.Equal(t, "end_turn", messages[1].MessageType)
	assert.Less(t, messages[0].ID, messages[1].ID)
}

func TestACPHistoryTextBatcherGivenBufferedChunksThenFlushesCombinedText(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	msg := domain.Message{
		MessageID:   "msg_batch",
		AgentID:     "agent_batch",
		SessionID:   "sess_batch",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		Role:        "assistant",
		Status:      "received",
		MessageType: "agent_message_chunk",
		LogicalKey:  "test:batch",
	}
	require.NoError(t, store.UpsertMessage(ctx, &msg))
	batcher := newACPHistoryTextBatcher(store, time.Hour, 1000)

	require.NoError(t, batcher.AppendText(ctx, msg.MessageID, 0, "h"))
	require.NoError(t, batcher.AppendText(ctx, msg.MessageID, 0, "i"))
	parts, err := store.ListMessageParts(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.Empty(t, parts)

	require.NoError(t, batcher.Flush(ctx))
	parts, err = store.ListMessageParts(ctx, msg.MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, "hi", parts[0].Text)
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

func TestACPHistoryGivenPaxInvocationPromptThenProjectsDisplayReplacement(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent-target",
		ownerUserID: "user-target",
		nodeID:      "node-target",
		sessionID:   "sess_target",
		store:       store,
	}

	payload := []byte(`{
		"jsonrpc":"2.0",
		"id":7,
		"method":"session/prompt",
		"params":{
			"sessionId":"sess_target",
			"prompt":[{"type":"text","text":"wrapped inquiry"}],
			"pax_invocation":{
				"invocation_id":"inv_1",
				"invocation_type":"agent_conversation",
				"phase":"inquiry",
				"side":"target",
				"sender":{"representative_agent_id":"rep_source","agent_id":"agent_source","session_id":"sess_source"},
				"receiver":{"representative_agent_id":"rep_target","agent_id":"agent_target","session_id":"sess_target"},
				"content":{"display_text":"Agent source asked for input.","original_text":"wrapped inquiry"}
			}
		}
	}`)

	err := projectACPUserPrompt(ctx, agent, payload)

	require.NoError(t, err)
	messages, err := store.ListMessages(ctx, agent.agentID, agent.sessionID, 100)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, domain.MessageTypePaxUser, messages[0].MessageType)
	assert.Equal(t, domain.MessageTypePaxInvocation, messages[1].MessageType)
	assert.Equal(t, messages[0].MessageID, messages[1].ParentMessageID)
	assert.Contains(
		t,
		string(messages[1].RawJSON),
		`"replaces_message_ids":["`+messages[0].MessageID+`"]`,
	)
	parts, err := store.ListMessageParts(ctx, messages[1].MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, "Agent source asked for input.", parts[0].Text)
}

func TestACPHistoryGivenPendingInvocationWhenTerminalToolUpdateArrivesThenProjectsDisplayReplacement(
	t *testing.T,
) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	toolCallRaw := json.RawMessage(`{
		"jsonrpc":"2.0",
		"method":"session/update",
		"params":{
			"sessionId":"sess_source",
			"update":{"sessionUpdate":"tool_call","toolCallId":"tool_1","title":"pax conversation"}
		}
	}`)
	err := projectACPTransportMessage(
		ctx,
		store,
		"agent_source",
		"user_source",
		"node_source",
		domain.TransportStreamPaxdToManager,
		1,
		"",
		toolCallRaw,
	)
	require.NoError(t, err)
	messages, err := store.ListMessages(ctx, "agent_source", "sess_source", 100)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	toolCallMessageID := messages[0].MessageID

	prompt := domain.Message{
		MessageID:      "msg_prompt",
		ConversationID: "conv_1",
		OwnerUserID:    "user_source",
		NodeID:         "node_source",
		AgentID:        "agent_source",
		SessionID:      "sess_source",
		Source:         domain.MessageSourceACPTunnel,
		Direction:      domain.MessageDirectionUserToAgent,
		Role:           "user",
		Status:         "sent",
		MessageType:    domain.MessageTypePaxUser,
		LogicalKey:     "test:prompt",
	}
	require.NoError(t, store.UpsertMessage(ctx, &prompt))
	pendingRaw, err := json.Marshal(paxInvocationPendingRaw{
		InvocationID:    "inv_1",
		InvocationType:  "agent_conversation",
		Phase:           "inquiry",
		Side:            "source",
		ToolCallID:      "tool_1",
		PromptMessageID: prompt.MessageID,
		ReplacesMessageID: []string{
			toolCallMessageID,
			prompt.MessageID,
		},
		Sender: paxInvocationPromptEndpoint{
			RepresentativeAgentID: "rep_source",
			AgentID:               "agent_source",
			SessionID:             "sess_source",
		},
		Receiver: paxInvocationPromptEndpoint{
			RepresentativeAgentID: "rep_target",
			AgentID:               "agent_target",
			SessionID:             "sess_target",
		},
		Content: paxInvocationPromptContent{
			DisplayText:  "Asked target for input.",
			OriginalText: "Ask another agent.",
		},
	})
	require.NoError(t, err)
	pending := domain.Message{
		MessageID:       "msg_pending",
		ConversationID:  "conv_1",
		OwnerUserID:     "user_source",
		NodeID:          "node_source",
		AgentID:         "agent_source",
		SessionID:       "sess_source",
		Source:          domain.MessageSourceACPTunnel,
		Direction:       domain.MessageDirectionUserToAgent,
		Role:            "user",
		Status:          "pending",
		MessageType:     domain.MessageTypePaxInvocationPending,
		ParentMessageID: toolCallMessageID,
		LogicalKey:      "agent_conversation:inv_1:inquiry:source:pending",
		RawJSON:         pendingRaw,
	}
	require.NoError(t, store.UpsertMessage(ctx, &pending))

	toolProgressRaw := json.RawMessage(`{
		"jsonrpc":"2.0",
		"method":"session/update",
		"params":{
			"sessionId":"sess_source",
			"update":{"sessionUpdate":"tool_call_update","toolCallId":"tool_1","status":"running"}
		}
	}`)
	err = projectACPTransportMessage(
		ctx,
		store,
		"agent_source",
		"user_source",
		"node_source",
		domain.TransportStreamPaxdToManager,
		2,
		"",
		toolProgressRaw,
	)
	require.NoError(t, err)

	toolUpdateRaw := json.RawMessage(`{
		"jsonrpc":"2.0",
		"method":"session/update",
		"params":{
			"sessionId":"sess_source",
			"update":{"sessionUpdate":"tool_call_update","toolCallId":"tool_1","status":"completed"}
		}
	}`)
	err = projectACPTransportMessage(
		ctx,
		store,
		"agent_source",
		"user_source",
		"node_source",
		domain.TransportStreamPaxdToManager,
		3,
		"",
		toolUpdateRaw,
	)
	require.NoError(t, err)

	messages, err = store.ListMessages(ctx, "agent_source", "sess_source", 100)
	require.NoError(t, err)
	var toolUpdate domain.Message
	var toolProgress domain.Message
	var display domain.Message
	for _, message := range messages {
		switch message.MessageType {
		case "tool_call_update":
			if acpHistoryTerminalToolCallUpdate(message.RawJSON) {
				toolUpdate = message
			} else {
				toolProgress = message
			}
		case domain.MessageTypePaxInvocation:
			display = message
		}
	}
	require.NotEmpty(t, toolProgress.MessageID)
	require.NotEmpty(t, toolUpdate.MessageID)
	require.NotEmpty(t, display.MessageID)
	assert.Equal(t, toolUpdate.MessageID, display.ParentMessageID)
	assert.Contains(t, string(display.RawJSON), `"`+toolCallMessageID+`"`)
	assert.Contains(t, string(display.RawJSON), `"`+toolProgress.MessageID+`"`)
	assert.Contains(t, string(display.RawJSON), `"`+toolUpdate.MessageID+`"`)
	assert.Contains(t, string(display.RawJSON), `"msg_prompt"`)
	assert.Contains(t, string(display.RawJSON), `"msg_pending"`)
	parts, err := store.ListMessageParts(ctx, display.MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, "Asked target for input.", parts[0].Text)

	normalInput := make([]domain.MessageWithParts, 0, len(messages))
	for _, message := range messages {
		normalInput = append(normalInput, domain.MessageWithParts{Message: message})
	}
	normal := domain.NormalTranscriptMessages(normalInput)
	require.Len(t, normal, 1)
	assert.Equal(t, domain.MessageTypePaxInvocation, normal[0].MessageType)
}

func TestACPHistoryGivenPermissionRequestThenProjectsRawFrame(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	raw := json.RawMessage(`{
		"jsonrpc":"2.0",
		"id":0,
		"method":"session/request_permission",
		"params":{
			"sessionId":"sess_permission",
			"toolCall":{"toolCallId":"perm-check-1","kind":"execute","title":"curl -sI google.com"},
			"options":[{"kind":"allow_once","name":"Allow once","optionId":"allow_once"}]
		}
	}`)

	err := projectACPTransportMessage(
		ctx,
		store,
		"agent_1",
		"user_1",
		"node_1",
		domain.TransportStreamPaxdToManager,
		4,
		"",
		raw,
	)
	require.NoError(t, err)

	messages, err := store.ListMessages(ctx, "agent_1", "sess_permission", 100)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	msg := messages[0]
	assert.Equal(t, domain.MessageDirectionAgentToUser, msg.Direction)
	assert.Equal(t, "assistant", msg.Role)
	assert.Equal(t, "session/request_permission", msg.MessageType)
	assert.Equal(t, string(raw), string(msg.RawJSON))

	parts, err := store.ListMessageParts(ctx, msg.MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, domain.MessagePartRawJSON, parts[0].PartType)
	assert.Contains(t, string(parts[0].PayloadJSON), `"method":"session/request_permission"`)
	assert.Contains(t, string(parts[0].PayloadJSON), `"toolCallId":"perm-check-1"`)
}

func TestACPHistoryGivenPermissionResponseThenProjectsRawFrame(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent_1",
		ownerUserID: "user_1",
		nodeID:      "node_1",
		sessionID:   "sess_permission",
		store:       store,
	}
	payload := []byte(
		`{"jsonrpc":"2.0","id":0,"result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`,
	)

	err := projectACPUserPrompt(ctx, agent, payload)
	require.NoError(t, err)

	messages, err := store.ListMessages(ctx, "agent_1", "sess_permission", 100)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	msg := messages[0]
	assert.Equal(t, domain.MessageDirectionUserToAgent, msg.Direction)
	assert.Equal(t, "user", msg.Role)
	assert.Equal(t, "permission_response", msg.MessageType)
	assert.Equal(t, string(payload), string(msg.RawJSON))

	parts, err := store.ListMessageParts(ctx, msg.MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, domain.MessagePartRawJSON, parts[0].PartType)
	assert.Empty(t, parts[0].Text)
	assert.Contains(t, string(parts[0].PayloadJSON), `"id":0`)
	assert.Contains(t, string(parts[0].PayloadJSON), `"optionId":"allow_once"`)
}

func TestACPHistoryGivenSessionNewRequestThenProjectsRawFrame(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent_1",
		ownerUserID: "user_1",
		nodeID:      "node_1",
		sessionID:   "sess_conversation",
		store:       store,
	}
	payload := []byte(
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[{"name":"pax-conversation","command":"paxd","args":["mcp","conversation","serve"],"env":[{"name":"PAX_AGENT_ID","value":"agent_1"}]}]}}`,
	)

	err := projectACPUserPrompt(ctx, agent, payload)
	require.NoError(t, err)

	messages, err := store.ListMessages(ctx, "agent_1", "sess_conversation", 100)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	msg := messages[0]
	assert.Equal(t, domain.MessageDirectionUserToAgent, msg.Direction)
	assert.Equal(t, "user", msg.Role)
	assert.Equal(t, "acp_session_new", msg.MessageType)
	assert.Equal(t, string(payload), string(msg.RawJSON))

	parts, err := store.ListMessageParts(ctx, msg.MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, domain.MessagePartRawJSON, parts[0].PartType)
	assert.Empty(t, parts[0].Text)
	assert.Contains(t, string(parts[0].PayloadJSON), `"method":"session/new"`)
	assert.Contains(t, string(parts[0].PayloadJSON), `"mcpServers"`)
	assert.Contains(t, string(parts[0].PayloadJSON), `"pax-conversation"`)
}

func TestACPHistoryGivenInitializeRequestThenProjectsRawFrame(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent_1",
		ownerUserID: "user_1",
		nodeID:      "node_1",
		sessionID:   "sess_conversation",
		store:       store,
	}
	payload := []byte(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
	)

	err := projectACPUserPrompt(ctx, agent, payload)
	require.NoError(t, err)

	messages, err := store.ListMessages(ctx, "agent_1", "sess_conversation", 100)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "acp_initialize", messages[0].MessageType)
	assert.Equal(t, string(payload), string(messages[0].RawJSON))
}

func TestACPHistoryKeepsDistinctUserPromptIDs(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID:     "agent-real",
		ownerUserID: "user-real",
		nodeID:      "node-real",
		sessionID:   "sess_repeat",
		store:       store,
	}

	for i, prompt := range []string{"first turn", "second turn"} {
		payload := []byte(
			`{"jsonrpc":"2.0","id":` +
				fmt.Sprint(i+1) +
				`,"method":"session/prompt","params":{"sessionId":"sess_repeat","prompt":[{"type":"text","text":"` +
				prompt +
				`"}]}}`,
		)
		if err := projectACPUserPrompt(ctx, agent, payload); err != nil {
			t.Fatalf("project user prompt %q: %v", prompt, err)
		}
	}

	messages, err := store.ListMessages(ctx, agent.agentID, agent.sessionID, 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %+v, want two user prompts with distinct request ids", messages)
	}
	gotText := map[string]bool{}
	for _, msg := range messages {
		parts, err := store.ListMessageParts(ctx, msg.MessageID)
		if err != nil {
			t.Fatalf("list parts: %v", err)
		}
		if len(parts) != 1 {
			t.Fatalf("parts for %s = %+v", msg.MessageID, parts)
		}
		gotText[parts[0].Text] = true
	}
	if !gotText["first turn"] || !gotText["second turn"] {
		t.Fatalf("message texts = %+v, want both prompts", gotText)
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

func TestACPHistoryProjectsEndTurnResponseWithFallbackSessionAsRawJSON(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	})
	raw := json.RawMessage(
		`{"id":7,"result":{"usage":{"inputTokens":11,"outputTokens":5,"totalTokens":16},"stopReason":"end_turn"},"jsonrpc":"2.0"}`,
	)

	err := projectACPTransportMessageForSession(
		ctx,
		store,
		"agent_1",
		"user_1",
		"node_1",
		domain.TransportStreamPaxdToManager,
		8,
		"",
		"sess-end-turn-1",
		raw,
	)
	require.NoError(t, err)

	messages, err := store.ListMessages(ctx, "agent_1", "sess-end-turn-1", 100)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "end_turn", messages[0].MessageType)
	assert.Equal(t, "sess-end-turn-1", messages[0].SessionID)
	assert.JSONEq(t, string(raw), string(messages[0].RawJSON))

	parts, err := store.ListMessageParts(ctx, messages[0].MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, domain.MessagePartRawJSON, parts[0].PartType)
	assert.JSONEq(t, string(raw), string(parts[0].PayloadJSON))
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

type countingACPHistoryTextSink struct {
	store       domain.Store
	seen        map[string]struct{}
	ensureCalls int
	appendCalls int
	flushCalls  int
}

func (s *countingACPHistoryTextSink) EnsureMessage(ctx context.Context, msg *domain.Message) error {
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	if _, ok := s.seen[msg.MessageID]; ok {
		return nil
	}
	s.ensureCalls++
	s.seen[msg.MessageID] = struct{}{}
	return s.store.UpsertMessage(ctx, msg)
}

func (s *countingACPHistoryTextSink) AppendText(
	ctx context.Context,
	messageID string,
	partIndex int,
	delta string,
) error {
	s.appendCalls++
	return s.store.AppendMessagePartText(ctx, messageID, partIndex, delta, nil)
}

func (s *countingACPHistoryTextSink) Flush(ctx context.Context) error {
	_ = ctx
	s.flushCalls++
	return nil
}
