package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func searchMemoryFixture() *MemoryStore {
	s := NewMemoryStore(time.Now)
	s.agents["agent"] = Agent{AgentID: "agent", OwnerUserID: "owner", NodeID: "node"}
	s.nodes["node"] = Node{NodeID: "node"}
	s.sessions[sessionKey("agent", "session")] = AgentSession{
		ID:          12,
		AgentID:     "agent",
		SessionID:   "session",
		SessionName: "Title",
	}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("message_%d", i)
		s.messages[id] = Message{
			MessageID:   id,
			AgentID:     "agent",
			SessionID:   "session",
			Role:        "user",
			MessageType: domain.MessageTypeUser,
			SessionSeq:  int64(i),
		}
		s.messageParts[messagePartKey{MessageID: id, Index: 0}] = MessagePart{
			MessageID: id,
			PartType:  domain.MessagePartText,
			Text:      id,
		}
	}
	return s
}

func TestSearchMemoryGivenMultipartMessageThenSharesCanonicalBodyAndOwner(t *testing.T) {
	s := searchMemoryFixture()
	s.messageParts[messagePartKey{MessageID: "message_1", Index: 1}] = MessagePart{
		MessageID: "message_1",
		PartIndex: 1,
		PartType:  domain.MessagePartText,
		Text:      "tail",
	}
	doc, err := s.ReadSearchDocument(t.Context(), "owner", "message", "message_1")
	require.NoError(t, err)
	assert.Equal(t, "message_1tail", doc.Body)
	assert.Equal(t, "owner", doc.OwnerUserID)
	assert.Equal(t, domain.SearchContentHash("message_1tail"), doc.ContentHash)
}

func TestSearchMemoryGivenHiddenOrForeignSourceThenRejectsRead(t *testing.T) {
	for _, tc := range []struct {
		name            string
		change          func(*MemoryStore)
		owner, kind, id string
	}{
		{name: "foreign", owner: "foreign", kind: "message", id: "message_1"},
		{name: "empty owner", owner: "", kind: "message", id: "message_1"},
		{name: "missing", owner: "owner", kind: "message", id: "missing"},
		{name: "unknown kind", owner: "owner", kind: "unknown", id: "message_1"},
		{name: "deleted agent", owner: "owner", kind: "message", id: "message_1", change: func(s *MemoryStore) { delete(s.agents, "agent") }},
		{name: "deleted session", owner: "owner", kind: "message", id: "message_1", change: func(s *MemoryStore) { delete(s.sessions, sessionKey("agent", "session")) }},
		{name: "deleted node", owner: "owner", kind: "message", id: "message_1", change: func(s *MemoryStore) { delete(s.nodes, "node") }},
		{name: "archived", owner: "owner", kind: "message", id: "message_1", change: func(s *MemoryStore) {
			session := s.sessions[sessionKey("agent", "session")]
			now := time.Now()
			session.ArchivedAt = &now
			s.sessions[sessionKey("agent", "session")] = session
		}},
		{name: "replaced", owner: "owner", kind: "message", id: "message_1", change: func(s *MemoryStore) {
			s.messages["display"] = Message{MessageID: "display", AgentID: "agent", SessionID: "session", MessageType: domain.MessageTypePaxInvocation, ParentMessageID: "message_1"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := searchMemoryFixture()
			if tc.change != nil {
				tc.change(s)
			}
			_, err := s.ReadSearchDocument(t.Context(), tc.owner, tc.kind, tc.id)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestSearchMemoryGivenTitleThenReturnsIndependentSessionDocument(t *testing.T) {
	s := searchMemoryFixture()
	for _, title := range []string{"Title", "Custom", ""} {
		session := s.sessions[sessionKey("agent", "session")]
		session.CustomSessionName = title
		if title == "" {
			session.SessionName = ""
		}
		s.sessions[sessionKey("agent", "session")] = session
		doc, err := s.ReadSearchDocument(t.Context(), "owner", "session", "12")
		if title == "" {
			assert.ErrorIs(t, err, ErrNotFound)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, title, doc.Title)
	}
	_, err := s.ReadSearchDocument(t.Context(), "other", "session", "12")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.ReadSearchDocument(t.Context(), "owner", "session", "99")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSearchMemoryGivenContextThenBoundsAndOrdersVisibleMessages(t *testing.T) {
	s := searchMemoryFixture()
	s.messages["other"] = Message{MessageID: "other", AgentID: "other"}
	docs, err := s.ReadSearchContext(t.Context(), "owner", "message_3", 1, 1)
	require.NoError(t, err)
	require.Len(t, docs, 3)
	assert.Equal(
		t,
		[]string{"message_2", "message_3", "message_4"},
		[]string{docs[0].SourceID, docs[1].SourceID, docs[2].SourceID},
	)
	docs, err = s.ReadSearchContext(t.Context(), "owner", "message_1", 100, 100)
	require.NoError(t, err)
	assert.Len(t, docs, 5)
	for _, tc := range []struct {
		owner, id     string
		before, after int
	}{{"other", "message_3", 1, 1}, {"", "message_3", 0, 0}, {"owner", "missing", 1, 1}, {"owner", "message_3", -1, 0}, {"owner", "message_3", 0, 101}} {
		_, err = s.ReadSearchContext(t.Context(), tc.owner, tc.id, tc.before, tc.after)
		assert.ErrorIs(t, err, ErrNotFound)
	}
}

func TestSearchContextGivenSameSequenceThenUsesTimeAndStableID(t *testing.T) {
	older := time.Now()
	newer := older.Add(time.Second)
	docs := []domain.SearchDocument{
		{SourceID: "b", CreatedAt: newer},
		{SourceID: "a", CreatedAt: newer},
		{SourceID: "c", CreatedAt: older},
	}
	sortSearchContext(docs)
	assert.Equal(
		t,
		[]string{"c", "a", "b"},
		[]string{docs[0].SourceID, docs[1].SourceID, docs[2].SourceID},
	)
}
