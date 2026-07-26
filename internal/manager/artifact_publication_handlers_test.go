package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArtifactPublicationGivenOwnedNodeSessionWhenRegisteredTwiceThenReturnsSamePublication(
	t *testing.T,
) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	createArtifactPublicationSession(t, srv, fixture, "sess_artifact_1")
	body := `{
		"source":{"agent_id":"` + fixture.agentID + `","session_id":"sess_artifact_1"},
		"filename":"report.pdf",
		"title":"Analysis report"
	}`

	first := registerArtifactPublication(t, srv, fixture.nodeAPIKey, "apub_1", body)
	second := registerArtifactPublication(t, srv, fixture.nodeAPIKey, "apub_1", body)

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	firstPublication := decodeArtifactPublication(t, first)
	secondPublication := decodeArtifactPublication(t, second)
	require.Equal(t, firstPublication, secondPublication)
	require.Equal(t, "apub_1", firstPublication.PublicationID)
	require.Equal(t, "queued", firstPublication.Status)
	require.Equal(t, fixture.nodeID, firstPublication.NodeID)
	require.Equal(t, fixture.agentID, firstPublication.AgentID)
	require.Equal(t, "sess_artifact_1", firstPublication.SessionID)
	require.Equal(t, "report.pdf", firstPublication.Filename)
	require.Equal(t, "Analysis report", firstPublication.Title)
}

func TestArtifactPublicationGivenDifferentNodeOrSourceWhenRegisteredThenRejectsBoundaryViolation(
	t *testing.T,
) {
	srv, _ := testServer(t, "owner@example.com")
	source := testNodeAgent(t, srv, "owner@example.com")
	otherNode := testNodeAgent(t, srv, "owner@example.com")
	otherOwner := testNodeAgent(t, srv, "other@example.com")
	createArtifactPublicationSession(t, srv, source, "sess_artifact_source")
	createArtifactPublicationSession(t, srv, otherNode, "sess_artifact_other")

	crossNode := registerArtifactPublication(
		t,
		srv,
		otherNode.nodeAPIKey,
		"apub_cross_node",
		`{"source":{"agent_id":"`+source.agentID+`","session_id":"sess_artifact_source"},"filename":"report.pdf"}`,
	)
	wrongSession := registerArtifactPublication(
		t,
		srv,
		source.nodeAPIKey,
		"apub_wrong_session",
		`{"source":{"agent_id":"`+source.agentID+`","session_id":"sess_artifact_other"},"filename":"report.pdf"}`,
	)
	crossOwner := registerArtifactPublication(
		t,
		srv,
		otherOwner.nodeAPIKey,
		"apub_cross_owner",
		`{"source":{"agent_id":"`+source.agentID+`","session_id":"sess_artifact_source"},"filename":"report.pdf"}`,
	)

	require.Equal(t, http.StatusNotFound, crossNode.Code, crossNode.Body.String())
	require.Equal(t, http.StatusNotFound, wrongSession.Code, wrongSession.Body.String())
	require.Equal(t, http.StatusNotFound, crossOwner.Code, crossOwner.Body.String())
}

func TestArtifactPublicationGivenExistingIDWhenSourceChangesThenReturnsConflict(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	createArtifactPublicationSession(t, srv, fixture, "sess_artifact_1")
	createArtifactPublicationSession(t, srv, fixture, "sess_artifact_2")

	first := registerArtifactPublication(
		t,
		srv,
		fixture.nodeAPIKey,
		"apub_conflict",
		`{"source":{"agent_id":"`+fixture.agentID+`","session_id":"sess_artifact_1"},"filename":"report.pdf"}`,
	)
	conflict := registerArtifactPublication(
		t,
		srv,
		fixture.nodeAPIKey,
		"apub_conflict",
		`{"source":{"agent_id":"`+fixture.agentID+`","session_id":"sess_artifact_2"},"filename":"report.pdf"}`,
	)

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
}

func TestArtifactPublicationGivenOwnerAndOtherUserWhenReadThenOnlyOwnerCanResolveIt(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	createArtifactPublicationSession(t, srv, fixture, "sess_artifact_1")
	registered := registerArtifactPublication(
		t,
		srv,
		fixture.nodeAPIKey,
		"apub_read",
		`{"source":{"agent_id":"`+fixture.agentID+`","session_id":"sess_artifact_1"},"filename":"report.pdf"}`,
	)
	require.Equal(t, http.StatusOK, registered.Code, registered.Body.String())

	ownerRead := getArtifactPublication(t, srv, "owner@example.com", "apub_read")
	otherRead := getArtifactPublication(t, srv, "other@example.com", "apub_read")

	require.Equal(t, http.StatusOK, ownerRead.Code, ownerRead.Body.String())
	require.Equal(t, "apub_read", decodeArtifactPublication(t, ownerRead).PublicationID)
	require.Equal(t, http.StatusNotFound, otherRead.Code, otherRead.Body.String())
}

type artifactPublicationTestView struct {
	PublicationID string `json:"publication_id"`
	NodeID        string `json:"node_id"`
	AgentID       string `json:"agent_id"`
	SessionID     string `json:"session_id"`
	Filename      string `json:"filename"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code"`
	ErrorMessage  string `json:"error_message"`
}

func createArtifactPublicationSession(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
	sessionID string,
) {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/nodes/"+fixture.nodeID+"/agents/"+fixture.agentID+"/sessions",
		bytes.NewReader([]byte(`{"session_id":"`+sessionID+`","native_id":"`+sessionID+`"}`)),
	)
	req.Header.Set("X-User-Email", fixture.userEmail)
	setJSON(req)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func registerArtifactPublication(
	t *testing.T,
	srv *Server,
	nodeAPIKey string,
	publicationID string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/node/artifact-publications/"+publicationID,
		bytes.NewReader([]byte(body)),
	)
	req.Header.Set("X-Pax-Key", nodeAPIKey)
	setJSON(req)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func getArtifactPublication(
	t *testing.T,
	srv *Server,
	email string,
	publicationID string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/artifact-publications/"+publicationID,
		nil,
	)
	req.Header.Set("X-User-Email", email)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func decodeArtifactPublication(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) artifactPublicationTestView {
	t.Helper()
	var response struct {
		Data struct {
			Publication artifactPublicationTestView `json:"publication"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	return response.Data.Publication
}
