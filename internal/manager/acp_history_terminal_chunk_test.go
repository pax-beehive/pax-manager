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

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestTerminalChunkTarget(t *testing.T) {
	const cap = 64 * 1024

	t.Run("given room in the active chunk then appends in place", func(t *testing.T) {
		index, size := terminalChunkTarget(0, 1000, 500, cap)
		assert.Equal(t, 0, index)
		assert.Equal(t, 1500, size)
	})

	t.Run("given the delta would overflow the cap then rolls to the next chunk", func(t *testing.T) {
		index, size := terminalChunkTarget(2, cap-10, 100, cap)
		assert.Equal(t, 3, index, "must advance part_index")
		assert.Equal(t, 100, size, "new chunk starts at the delta size")
	})

	t.Run("given an empty active chunk then never rolls before the first write", func(t *testing.T) {
		// A single oversized delta still lands in its own bounded part.
		index, size := terminalChunkTarget(0, 0, cap*2, cap)
		assert.Equal(t, 0, index)
		assert.Equal(t, cap*2, size)
	})

	t.Run("given exactly the cap then stays until the next delta overflows", func(t *testing.T) {
		index, size := terminalChunkTarget(1, 0, cap, cap)
		assert.Equal(t, 1, index)
		assert.Equal(t, cap, size)

		index, size = terminalChunkTarget(index, size, 1, cap)
		assert.Equal(t, 2, index, "the following delta overflows and rolls")
		assert.Equal(t, 1, size)
	})
}

// TestTerminalChunkingBoundsPartSize drives large terminal output through the
// immediate projection path and asserts it is stored as bounded, ordered chunks
// whose reassembly reproduces the exact byte stream — the core amplification
// fix and the terminal-history read contract.
func TestTerminalChunkingBoundsPartSize(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	})

	const deltaBytes = 20 * 1024
	const deltaCount = 5
	var expected strings.Builder
	for i := 0; i < deltaCount; i++ {
		data := strings.Repeat(string(rune('a'+i)), deltaBytes)
		expected.WriteString(data)
		frame := terminalDeltaFrame("sess_chunk", "call_chunk", data)
		require.NoError(t, projectACPTransportMessage(
			ctx,
			store,
			"agent_chunk",
			"user_chunk",
			"node_chunk",
			domain.TransportStreamPaxdToManager,
			int64(i+1),
			"",
			frame,
		))
	}

	messages, err := store.ListMessages(ctx, "agent_chunk", "sess_chunk", 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)

	parts, err := store.ListMessageParts(ctx, messages[0].MessageID)
	require.NoError(t, err)
	require.Greater(t, len(parts), 1, "large output must roll across multiple chunks")

	var reassembled strings.Builder
	for i, part := range parts {
		assert.Equal(t, i, part.PartIndex, "parts must be densely ordered by part_index")
		assert.Equal(t, domain.MessagePartText, part.PartType)
		assert.LessOrEqual(t, len(part.Text), defaultACPTerminalChunkMaxBytes,
			"no single terminal part may exceed the chunk cap")
		reassembled.WriteString(part.Text)
	}
	assert.Equal(t, expected.String(), reassembled.String(),
		"reassembling parts in order must reproduce the exact terminal byte stream")
}

// TestTerminalChunkingResumesFromStoreAfterRestart proves a fresh allocator
// (as after a process restart) recovers the active chunk from the store and
// keeps appending forward instead of clobbering an earlier chunk.
func TestTerminalChunkingResumesFromStoreAfterRestart(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	})
	messageID := "msg_resume"

	// Pre-existing chunks: part 0 full, part 1 partially filled.
	require.NoError(t, store.UpsertMessage(ctx, &domain.Message{
		MessageID: messageID, OwnerUserID: "u", NodeID: "n", AgentID: "a",
		SessionID: "s", Source: domain.MessageSourceACPTunnel,
		Direction: domain.MessageDirectionAgentToUser, Role: "assistant",
	}))
	require.NoError(t, store.AppendMessagePartText(ctx, messageID, 0,
		strings.Repeat("x", defaultACPTerminalChunkMaxBytes), nil))
	require.NoError(t, store.AppendMessagePartText(ctx, messageID, 1, "tail", nil))

	sink := immediateACPHistoryTextSink{store: store}
	// Small delta fits the partially-filled part 1.
	require.NoError(t, sink.AppendTerminalText(ctx, messageID, "-more"))

	parts, err := store.ListMessageParts(ctx, messageID)
	require.NoError(t, err)
	require.Len(t, parts, 2, "must continue the active chunk, not clobber part 0")
	assert.Equal(t, "tail-more", parts[1].Text)
	assert.Len(t, parts[0].Text, defaultACPTerminalChunkMaxBytes, "earlier full chunk is untouched")
}

// TestTerminalChunkingAgentPathRollsAndReassembles exercises the live,
// batcher-backed sink used in production.
func TestTerminalChunkingAgentPathRollsAndReassembles(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{
		agentID: "agent_live", ownerUserID: "user_live", nodeID: "node_live",
		sessionID: "sess_live", store: store,
	}
	sink := acpAgentHistoryTextSink{agent: agent}

	const deltaBytes = 30 * 1024
	const deltaCount = 4
	var expected strings.Builder
	for i := 0; i < deltaCount; i++ {
		data := strings.Repeat(string(rune('m'+i)), deltaBytes)
		expected.WriteString(data)
		require.NoError(t, projectACPTransportMessageWithTextSink(
			ctx, store, sink,
			agent.agentID, agent.ownerUserID, agent.nodeID,
			domain.TransportStreamPaxdToManager, int64(i+1), "", "",
			terminalDeltaFrame("sess_live", "call_live", data),
		))
	}
	require.NoError(t, agent.flushHistoryText(ctx))

	messages, err := store.ListMessages(ctx, agent.agentID, "sess_live", 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)

	parts, err := store.ListMessageParts(ctx, messages[0].MessageID)
	require.NoError(t, err)
	require.Greater(t, len(parts), 1)

	var reassembled strings.Builder
	for i, part := range parts {
		assert.Equal(t, i, part.PartIndex)
		assert.LessOrEqual(t, len(part.Text), defaultACPTerminalChunkMaxBytes)
		reassembled.WriteString(part.Text)
	}
	assert.Equal(t, expected.String(), reassembled.String())
}

func terminalDeltaFrame(sessionID, terminalID, data string) json.RawMessage {
	encoded, _ := json.Marshal(data)
	return json.RawMessage(fmt.Sprintf(
		`{"method":"session/update","params":{"update":{"_meta":{"terminal_output_delta":{"data":%s,"terminal_id":%q}},"toolCallId":%q,"sessionUpdate":"tool_call_update"},"sessionId":%q},"jsonrpc":"2.0"}`,
		encoded, terminalID, terminalID, sessionID,
	))
}
