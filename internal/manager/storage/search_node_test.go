package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func nodeSearchMemoryFixture() *MemoryStore {
	s := searchMemoryFixture()
	s.nodes["new-node"] = Node{NodeID: "new-node"}
	agent := s.agents["agent"]
	agent.NodeID = "new-node"
	s.agents["agent"] = agent
	session := s.sessions[sessionKey("agent", "session")]
	session.NodeID = "node"
	s.sessions[sessionKey("agent", "session")] = session
	return s
}

func TestSearchMemoryNodeAttributionPreservesHistoryAndBlanksConflicts(t *testing.T) {
	s := nodeSearchMemoryFixture()
	for _, source := range []struct{ kind, id string }{{"message", "message_1"}, {"session", "12"}} {
		doc, err := s.ReadSearchDocument(t.Context(), "owner", source.kind, source.id)
		require.NoError(t, err)
		assert.Equal(t, "node", doc.NodeID, "current agent node must not relabel old sessions")
	}
	message := s.messages["message_1"]
	message.NodeID = "new-node"
	s.messages[message.MessageID] = message
	doc, err := s.ReadSearchDocument(t.Context(), "owner", "message", message.MessageID)
	require.NoError(t, err)
	assert.Empty(t, doc.NodeID)
	message.SessionID = ""
	s.messages[message.MessageID] = message
	doc, err = s.ReadSearchDocument(t.Context(), "owner", "message", message.MessageID)
	require.NoError(t, err)
	assert.Equal(t, "new-node", doc.NodeID)
	_, err = s.ReadSearchDocument(t.Context(), "other-owner", "message", message.MessageID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSearchMemorySummaryNodeRequiresUnanimousTransitiveSources(t *testing.T) {
	for _, mode := range []string{"same", "mixed", "unknown", "conflicting", "source-free-child", "primary-mixed", "primary-unknown", "child-primary-mixed", "child-primary-unknown"} {
		t.Run(mode, func(t *testing.T) {
			s := nodeSearchMemoryFixture()
			message := s.messages["message_2"]
			switch mode {
			case "mixed", "unknown":
				node := "new-node"
				if mode == "unknown" {
					node = ""
				}
				s.sessions[sessionKey("agent", "related")] = AgentSession{
					AgentID:   "agent",
					SessionID: "related",
					NodeID:    node,
				}
				message.SessionID = "related"
			case "conflicting":
				message.NodeID = "new-node"
			}
			s.messages[message.MessageID] = message
			child := domain.SummaryVersion{SummaryID: "child", Revision: 1, OwnerUserID: "owner",
				Coverage: []domain.MessageVersionRef{{MessageID: "message_2"}}}
			if mode == "source-free-child" {
				child.Coverage = nil
			}
			if mode == "child-primary-mixed" || mode == "child-primary-unknown" {
				node := "new-node"
				if mode == "child-primary-unknown" {
					node = ""
				}
				s.sessions[sessionKey("agent", "child-primary")] = AgentSession{
					ID: 13, AgentID: "agent", SessionID: "child-primary", NodeID: node,
				}
				child.SubjectType, child.SubjectID = "fragment", "secretary:session:13"
			}
			s.secretaryLocked().summaries["child"] = []domain.SummaryVersion{child}
			s.secretaryLocked().summaries["root"] = []domain.SummaryVersion{{
				SummaryID: "root", Revision: 1, OwnerUserID: "owner", Body: "port summary",
				SubjectType: "fragment", SubjectID: "secretary:session:12",
				Coverage:       []domain.MessageVersionRef{{MessageID: "message_1"}},
				SummarySources: []domain.SummaryRef{{SummaryID: "child", Revision: 1}},
			}}
			if mode == "primary-mixed" || mode == "primary-unknown" {
				node := "new-node"
				if mode == "primary-unknown" {
					node = ""
				}
				s.sessions[sessionKey("agent", "primary")] = AgentSession{
					ID:        13,
					AgentID:   "agent",
					SessionID: "primary",
					NodeID:    node,
				}
				summary := s.secretaryLocked().summaries["root"][0]
				summary.SubjectID = "secretary:session:13"
				s.secretaryLocked().summaries["root"][0] = summary
			}
			doc, err := s.ReadSearchDocument(t.Context(), "owner", "summary", "root")
			require.NoError(t, err)
			if mode == "same" {
				assert.Equal(t, "node", doc.NodeID)
			} else {
				assert.Empty(t, doc.NodeID, "the primary source must not hide unknown or cross-node evidence")
			}
		})
	}
}
