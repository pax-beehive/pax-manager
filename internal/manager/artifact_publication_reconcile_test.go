package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestArtifactDisplayGivenTerminalFirstWhenPublicationRegistersThenProjectsOneCard(
	t *testing.T,
) {
	srv, fixture := artifactDisplayFixture(t)
	projectArtifactToolLifecycle(t, srv, fixture, "tool_publish_1", "apub_terminal_first", 1)
	require.Empty(t, artifactDisplayMessages(t, srv, fixture))

	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_terminal_first",
		"report.pdf",
		"sess_artifact_display",
	)

	displays := artifactDisplayMessages(t, srv, fixture)
	require.Len(t, displays, 1)
	assertArtifactDisplay(
		t,
		srv,
		displays[0],
		"apub_terminal_first",
		"tool_publish_1",
	)
}

func TestArtifactDisplayGivenPublicationFirstWhenTerminalArrivesThenProjectsOneCard(
	t *testing.T,
) {
	srv, fixture := artifactDisplayFixture(t)
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_publication_first",
		"report.pdf",
		"sess_artifact_display",
	)

	projectArtifactToolLifecycle(t, srv, fixture, "tool_publish_2", "apub_publication_first", 10)

	displays := artifactDisplayMessages(t, srv, fixture)
	require.Len(t, displays, 1)
	assertArtifactDisplay(
		t,
		srv,
		displays[0],
		"apub_publication_first",
		"tool_publish_2",
	)
}

func TestArtifactDisplayGivenReplayedFactsWhenReconciledThenRemainsOneCard(t *testing.T) {
	srv, fixture := artifactDisplayFixture(t)
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_replay",
		"report.pdf",
		"sess_artifact_display",
	)

	projectArtifactToolLifecycle(t, srv, fixture, "tool_publish_replay", "apub_replay", 20)
	projectArtifactToolLifecycle(t, srv, fixture, "tool_publish_replay", "apub_replay", 20)
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_replay",
		"report.pdf",
		"sess_artifact_display",
	)

	require.Len(t, artifactDisplayMessages(t, srv, fixture), 1)
}

func TestArtifactDisplayGivenMissedTriggersWhenRepairRunsThenReconstructsCard(t *testing.T) {
	srv, fixture := artifactDisplayFixture(t)
	projectArtifactToolLifecycle(t, srv, fixture, "tool_publish_repair", "apub_repair", 30)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	_, err := srv.store.PutArtifactPublication(t.Context(), ArtifactPublication{
		PublicationID: "apub_repair",
		OwnerUserID:   principal.User.UserID,
		NodeID:        fixture.nodeID,
		AgentID:       fixture.agentID,
		SessionID:     "sess_artifact_display",
		Filename:      "report.pdf",
		Status:        domain.ArtifactPublicationStatusQueued,
	})
	require.NoError(t, err)
	require.Empty(t, artifactDisplayMessages(t, srv, fixture))

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/agents/"+fixture.agentID+
			"/sessions/sess_artifact_display/history",
		nil,
	)
	req.Header.Set("X-User-Email", fixture.userEmail)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"message_type":"pax:artifact"`)
	require.Len(t, artifactDisplayMessages(t, srv, fixture), 1)
}

func TestArtifactDisplayGivenTwoPublishCallsForSameFileThenKeepsTwoOccurrences(
	t *testing.T,
) {
	srv, fixture := artifactDisplayFixture(t)
	for _, publicationID := range []string{"apub_occurrence_1", "apub_occurrence_2"} {
		registerTestArtifactPublication(
			t,
			srv,
			fixture,
			publicationID,
			"report.pdf",
			"sess_artifact_display",
		)
	}

	projectArtifactToolLifecycle(t, srv, fixture, "tool_occurrence_1", "apub_occurrence_1", 40)
	projectArtifactToolLifecycle(t, srv, fixture, "tool_occurrence_2", "apub_occurrence_2", 50)

	displays := artifactDisplayMessages(t, srv, fixture)
	require.Len(t, displays, 2)
	assert.NotEqual(t, displays[0].MessageID, displays[1].MessageID)
}

func TestArtifactDisplayGivenNormalTranscriptThenReplacesRawToolLifecycle(t *testing.T) {
	srv, fixture := artifactDisplayFixture(t)
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_normal",
		"report.pdf",
		"sess_artifact_display",
	)
	projectArtifactToolLifecycle(t, srv, fixture, "tool_normal", "apub_normal", 60)
	messages, err := srv.store.ListMessages(
		t.Context(),
		fixture.agentID,
		"sess_artifact_display",
		100,
	)
	require.NoError(t, err)
	withParts := make([]domain.MessageWithParts, 0, len(messages))
	for _, message := range messages {
		parts, err := srv.store.ListMessageParts(t.Context(), message.MessageID)
		require.NoError(t, err)
		withParts = append(withParts, domain.MessageWithParts{Message: message, Parts: parts})
	}

	normal := domain.NormalTranscriptMessages(withParts)

	require.Len(t, normal, 1)
	assert.Equal(t, domain.MessageTypePaxArtifact, normal[0].MessageType)
	require.Len(t, normal[0].Parts, 1)
	assert.Equal(
		t,
		"artifact-publication://apub_normal/main",
		normal[0].Parts[0].ArtifactURI,
	)
}

func artifactDisplayFixture(t *testing.T) (*Server, conversationTestFixture) {
	t.Helper()
	srv, _ := testServer(t, "artifact-display@example.com")
	fixture := testNodeAgent(t, srv, "artifact-display@example.com")
	createArtifactPublicationSession(t, srv, fixture, "sess_artifact_display")
	return srv, fixture
}

func projectArtifactToolLifecycle(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
	toolCallID string,
	publicationID string,
	firstSeq int64,
) {
	t.Helper()
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	started := json.RawMessage(`{
		"jsonrpc":"2.0",
		"method":"session/update",
		"params":{
			"sessionId":"sess_artifact_display",
			"update":{
				"sessionUpdate":"tool_call",
				"toolCallId":"` + toolCallID + `",
				"status":"in_progress"
			}
		}
	}`)
	completed := json.RawMessage(`{
		"jsonrpc":"2.0",
		"method":"session/update",
		"params":{
			"sessionId":"sess_artifact_display",
			"update":{
				"sessionUpdate":"tool_call_update",
				"toolCallId":"` + toolCallID + `",
				"status":"completed",
				"content":[{
					"type":"content",
					"content":{
						"type":"text",
						"text":"{\"accepted\":true,\"pax_artifact_publication\":{\"publication_id\":\"` +
		publicationID + `\"}}"
					}
				}]
			}
		}
	}`)
	require.NoError(t, projectACPTransportMessage(
		t.Context(),
		srv.store,
		fixture.agentID,
		principal.User.UserID,
		fixture.nodeID,
		domain.TransportStreamPaxdToManager,
		firstSeq,
		"",
		started,
	))
	require.NoError(t, projectACPTransportMessage(
		t.Context(),
		srv.store,
		fixture.agentID,
		principal.User.UserID,
		fixture.nodeID,
		domain.TransportStreamPaxdToManager,
		firstSeq+1,
		"",
		completed,
	))
}

func artifactDisplayMessages(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
) []domain.Message {
	t.Helper()
	messages, err := srv.store.ListMessages(
		t.Context(),
		fixture.agentID,
		"sess_artifact_display",
		100,
	)
	require.NoError(t, err)
	displays := make([]domain.Message, 0)
	for _, message := range messages {
		if message.MessageType == domain.MessageTypePaxArtifact {
			displays = append(displays, message)
		}
	}
	return displays
}

func assertArtifactDisplay(
	t *testing.T,
	srv *Server,
	display domain.Message,
	publicationID string,
	toolCallID string,
) {
	t.Helper()
	assert.Equal(
		t,
		"artifact-publication:sess_artifact_display:"+toolCallID+":display",
		display.LogicalKey,
	)
	assert.NotEmpty(t, display.ParentMessageID)
	assert.Contains(t, string(display.RawJSON), `"publication_id":"`+publicationID+`"`)
	parts, err := srv.store.ListMessageParts(t.Context(), display.MessageID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, domain.MessagePartArtifact, parts[0].PartType)
	assert.Equal(
		t,
		"artifact-publication://"+publicationID+"/main",
		parts[0].ArtifactURI,
	)
}
