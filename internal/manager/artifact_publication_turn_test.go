package manager

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestArtifactDisplayGivenLatePublicationThenKeepsSourceTurnAndPosition(t *testing.T) {
	srv, fixture := artifactDisplayFixture(t)
	terminal := artifactTurnTerminal(t, srv, fixture, "turn_first", "tool_first", "apub_late")
	done := artifactTurnDone(t, srv, fixture, "turn_first")
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_late",
		"board.mmd",
		"sess_artifact_display",
	)

	displays := artifactDisplayMessages(t, srv, fixture)
	require.Len(t, displays, 1)
	display := displays[0]
	assert.Equal(t, terminal.TurnID, display.TurnID)
	assert.Equal(t, terminal.MessageID, display.ParentMessageID)
	assert.Greater(t, display.SessionSeq, done.SessionSeq)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(display.RawJSON, &raw))
	assert.Equal(t, float64(terminal.SessionSeq), raw["source_session_seq"])
	parts, err := srv.store.ListMessageParts(t.Context(), display.MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.JSONEq(t, string(display.RawJSON), string(parts[0].PayloadJSON))

	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_late",
		"board.mmd",
		"sess_artifact_display",
	)
	replayed := artifactDisplayMessages(t, srv, fixture)
	require.Len(t, replayed, 1)
	assert.Equal(t, display.MessageID, replayed[0].MessageID)
	assert.Equal(t, display.SessionSeq, replayed[0].SessionSeq)
}

func TestArtifactDisplayGivenReusedToolIDThenKeepsUploadsInTheirOwnTurns(t *testing.T) {
	srv, fixture := artifactDisplayFixture(t)
	first := artifactTurnTerminal(t, srv, fixture, "turn_first", "tool_reused", "apub_first")
	second := artifactTurnTerminal(t, srv, fixture, "turn_second", "tool_reused", "apub_second")
	for _, id := range []string{"apub_first", "apub_second"} {
		registerTestArtifactPublication(t, srv, fixture, id, "board.mmd", "sess_artifact_display")
	}

	displays := artifactDisplayMessages(t, srv, fixture)
	require.Len(t, displays, 2)
	byTurn := make(map[string]domain.Message)
	for _, display := range displays {
		byTurn[display.TurnID] = display
	}
	for _, terminal := range []domain.Message{first, second} {
		display := byTurn[terminal.TurnID]
		assert.Equal(t, terminal.MessageID, display.ParentMessageID)
		var raw struct {
			Replaces []string `json:"replaces_message_ids"`
		}
		require.NoError(t, json.Unmarshal(display.RawJSON, &raw))
		assert.Equal(t, []string{terminal.MessageID}, raw.Replaces)
	}
}

func artifactTurnTerminal(t *testing.T, srv *Server, fixture conversationTestFixture,
	turnID, toolID, publicationID string,
) domain.Message {
	t.Helper()
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	marker, err := json.Marshal(map[string]any{
		"accepted": true, "pax_artifact_publication": map[string]string{"publication_id": publicationID},
	})
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{"params": map[string]any{"update": map[string]any{
		"toolCallId": toolID, "status": "completed", "rawOutput": string(marker),
	}}})
	require.NoError(t, err)
	message := domain.Message{
		MessageID: "terminal_" + turnID + "_" + toolID, OwnerUserID: principal.User.UserID,
		NodeID: fixture.nodeID, AgentID: fixture.agentID, SessionID: "sess_artifact_display",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
		Role: "assistant", Status: "received", MessageType: "tool_call", TurnID: turnID, RawJSON: raw,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	return message
}

func artifactTurnDone(t *testing.T, srv *Server, fixture conversationTestFixture,
	turnID string,
) domain.Message {
	t.Helper()
	message := domain.Message{
		MessageID: "done_" + turnID, AgentID: fixture.agentID, SessionID: "sess_artifact_display",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
		MessageType: "end_turn", TurnID: turnID,
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	return message
}
