package manager

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func parseSSEEvents(t *testing.T, body string) []conversationEvent {
	t.Helper()
	events := make([]conversationEvent, 0)
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var data string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" {
			continue
		}
		var ev conversationEvent
		require.NoError(t, json.Unmarshal([]byte(data), &ev))
		events = append(events, ev)
	}
	return events
}

func TestWriteSessionSeqCatchupReplaysDurableItemsAfterCursor(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	})
	for i := 1; i <= 4; i++ {
		id := "m" + string(rune('0'+i))
		require.NoError(t, store.UpsertMessage(ctx, &domain.Message{
			MessageID: id, AgentID: "a", SessionID: "s", TurnID: "t1",
			Source:    domain.MessageSourceACPTunnel,
			Direction: domain.MessageDirectionAgentToUser, Role: "assistant",
		}))
		require.NoError(t, store.UpsertMessagePart(ctx, &domain.MessagePart{
			MessageID: id, PartIndex: 0, PartType: domain.MessagePartText, Text: id,
		}))
	}

	// Catch up from cursor 2 → only seq 3 and 4, ascending, tagged with SSE ids.
	rec := httptest.NewRecorder()
	head, err := writeSessionSeqCatchup(ctx, rec, rec, store, "n", "a", "s", 2)
	require.NoError(t, err)
	assert.Equal(t, int64(4), head)

	body := rec.Body.String()
	assert.Contains(t, body, "id: 3\n")
	assert.Contains(t, body, "id: 4\n")

	events := parseSSEEvents(t, body)
	require.Len(t, events, 2)
	assert.Equal(t, "history_item", events[0].Type)
	assert.Equal(t, int64(3), events[0].Seq)
	assert.Equal(t, "m3", events[0].MessageID)
	require.NotNil(t, events[0].Item)
	require.Len(t, events[0].Item.Parts, 1)
	assert.Equal(t, "m3", events[0].Item.Parts[0].Text)
	assert.Equal(t, int64(4), events[1].Seq)
	assert.Equal(t, "m4", events[1].MessageID)

	// From head → nothing to replay, but head is still reported.
	rec2 := httptest.NewRecorder()
	head2, err := writeSessionSeqCatchup(ctx, rec2, rec2, store, "n", "a", "s", 4)
	require.NoError(t, err)
	assert.Equal(t, int64(4), head2)
	assert.Empty(t, parseSSEEvents(t, rec2.Body.String()))
}

func TestSessionObserverCursorPrefersQueryThenLastEventID(t *testing.T) {
	req := httptest.NewRequest("GET", "/events?after_seq=7", nil)
	assert.Equal(t, int64(7), sessionObserverCursor(req))

	req = httptest.NewRequest("GET", "/events", nil)
	req.Header.Set("Last-Event-ID", "12")
	assert.Equal(t, int64(12), sessionObserverCursor(req))

	req = httptest.NewRequest("GET", "/events", nil)
	assert.Equal(t, int64(0), sessionObserverCursor(req))
}
