package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPostgresLatestSessionMessageReadsOnlyHeadMetadata(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		scriptedRow("session"), scriptedRow("native"), scriptedRow("latest", int64(42), "turn"),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	message, err := store.LatestSessionMessage(t.Context(), "agent", "session")
	require.NoError(t, err)
	require.Equal(t, "latest", message.MessageID)
	require.Equal(t, int64(42), message.SessionSeq)
	require.Equal(t, "turn", message.TurnID)
	query := script.queryTexts[len(script.queryTexts)-1]
	require.Contains(t, query, "ORDER BY session_seq DESC LIMIT 1")
	require.Contains(t, query, "agent_id = $1 AND session_id IN")
	require.NotContains(t, query, "raw_json")
	require.NotContains(t, query, "message_parts")
}

func TestPostgresTurnSummaryFiltersBeforePagination(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		scriptedRow("session"), scriptedRow("native"), scriptedRow(int64(9)),
		{columns: []string{"message_id"}},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err := store.ListTurnSummaryPage(t.Context(), "agent", "session", "turn", 0, 9, 2)
	require.NoError(t, err)
	query := script.queryTexts[len(script.queryTexts)-1]
	require.Contains(t, query, "AND turn_id = $4")
	require.Contains(t, query, "AND session_seq < $5")
	require.Contains(t, query, "LIMIT $6")
}

func TestMessageSummaryCursorAndTerminalDetail(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	ctx := t.Context()
	for _, id := range []string{"first", "terminal", "last"} {
		msg := Message{
			MessageID:   id,
			AgentID:     "agent",
			SessionID:   "session",
			Source:      domain.MessageSourceACPTunnel,
			Direction:   domain.MessageDirectionAgentToUser,
			MessageType: "tool_call_update",
			RawJSON: json.RawMessage(
				`{"params":{"update":{"toolCallId":"call","title":"Read","content":[{"text":"large"}]}}}`,
			),
		}
		require.NoError(t, store.UpsertMessage(ctx, &msg))
	}
	page, err := store.ListMessageSummaryPage(ctx, "agent", "session", 0, 0, 2)
	require.NoError(t, err)
	require.True(t, page.HasOlder)
	require.Equal(t, int64(3), page.HeadSeq)
	require.NotContains(t, string(page.Messages[0].RawJSON), "large")
	older, err := store.ListMessageSummaryPage(ctx, "agent", "session", 0, page.NextBeforeSeq, 2)
	require.NoError(t, err)
	require.Len(t, older.Messages, 1)
	require.Equal(t, "first", older.Messages[0].MessageID)
	for i, text := range []string{strings.Repeat("a", 20000), "second"} {
		require.NoError(
			t,
			store.UpsertMessagePart(ctx, &MessagePart{MessageID: "terminal", PartIndex: i,
				PartType: domain.MessagePartText, Text: text}),
		)
	}
	detail, err := store.GetMessageDetailPage(
		ctx,
		"agent",
		"session",
		"terminal",
		"output",
		19995,
		10,
	)
	require.NoError(t, err)
	require.Equal(t, "aaaaasecon", detail.Text)
	require.True(t, detail.HasMore)
	oldRevision := detail.Revision
	now = now.Add(time.Second)
	require.NoError(t, store.AppendMessagePartText(ctx, "terminal", 1, "new", nil))
	detail, err = store.GetMessageDetailPage(ctx, "agent", "session", "terminal", "output", 0, 10)
	require.NoError(t, err)
	require.NotEqual(t, oldRevision, detail.Revision)
	_, err = store.GetMessageDetailPage(ctx, "wrong-agent", "session", "terminal", "output", 0, 10)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestPostgresMessageDetailSlicesBeforeReturning(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	script := &scriptedPostgresScript{queries: []scriptedRows{
		scriptedRow("session"), scriptedRow("native"), scriptedRow(now, "text", "123456"),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	page, err := store.GetMessageDetailPage(
		t.Context(),
		"agent",
		"session",
		"message",
		"output",
		10,
		5,
	)
	require.NoError(t, err)
	require.Equal(t, "12345", page.Text)
	require.True(t, page.HasMore)
	require.Equal(t, 15, page.NextOffset)
	query := script.queryTexts[len(script.queryTexts)-1]
	require.Contains(
		t,
		query,
		"substring(COALESCE(body, payload::text) FROM $4::integer FOR $5::integer)",
	)
	require.Contains(t, query, "agent_id=$1 AND message_id=$2 AND session_id IN")
}

func TestMessageSummaryPartsPreservesIndependentPayload(t *testing.T) {
	store := NewMemoryStore(time.Now)
	ctx := t.Context()
	raw := json.RawMessage(`{"metadata":"message"}`)
	message := Message{MessageID: "mixed", AgentID: "agent", SessionID: "session",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser, RawJSON: raw}
	require.NoError(t, store.UpsertMessage(ctx, &message))
	for i, payload := range []json.RawMessage{raw, json.RawMessage(`{"attachment_id":"attachment"}`)} {
		require.NoError(t, store.UpsertMessagePart(ctx, &MessagePart{MessageID: message.MessageID,
			PartIndex: i, PartType: domain.MessagePartText, Text: "body", PayloadJSON: payload}))
	}
	parts, err := store.ListMessageSummaryParts(ctx, []string{message.MessageID})
	require.NoError(t, err)
	require.Empty(t, parts[message.MessageID][0].PayloadJSON)
	require.JSONEq(
		t,
		`{"attachment_id":"attachment"}`,
		string(parts[message.MessageID][1].PayloadJSON),
	)
	stored, err := store.ListMessageParts(ctx, message.MessageID)
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(stored[0].PayloadJSON))
}

func TestPostgresSummaryContextStaysWithinPageTurnsAndSession(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{
		scriptedRow("session"), scriptedRow("native"), {},
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	page := domain.MessageHistoryPage{HeadSeq: 160, NextBeforeSeq: 60, HasOlder: true,
		Messages: []Message{{MessageID: "tool", TurnID: "turn", SessionSeq: 60}},
	}
	got, err := store.withSummaryTurnContext(t.Context(), "agent", "session", page)
	require.NoError(t, err)
	require.Equal(t, page.NextBeforeSeq, got.NextBeforeSeq)
	query := script.queryTexts[len(script.queryTexts)-1]
	require.Contains(t, query, "agent_id = $1 AND session_id IN ($2,$3)")
	require.Contains(t, query, "turn_id IN ($4)")
	require.Contains(t, query, "message_type IS NULL OR NOT")
	require.Contains(t, query, "session_seq <= $5")
	args := script.queryArgs[len(script.queryArgs)-1]
	require.Equal(t, "turn", args[3].Value)
	require.Equal(t, int64(160), args[4].Value)
}
