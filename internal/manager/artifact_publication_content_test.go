package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifactPublicationContentGivenUploadPendingWhenReadThenReturnsRetryableState(
	t *testing.T,
) {
	srv, fixture := artifactPrepareFixture(t)
	registerTestArtifactPublication(
		t, srv, fixture, "apub_content_pending", "report.pdf", "sess_artifact",
	)

	rec := getTestArtifactPublicationContent(
		t, srv, fixture.userEmail, "apub_content_pending", "inline",
	)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	data := decodeArtifactPublicationContent(t, rec)
	assert.Equal(t, "not_available", data.Status)
	assert.True(t, data.Retryable)
	assert.Empty(t, data.URL)
	assert.Equal(t, "2", rec.Header().Get("Retry-After"))
	assert.NotContains(t, rec.Body.String(), "expires_at")
}

func TestArtifactPublicationContentGivenPermanentFailureWhenReadThenReturnsTerminalState(
	t *testing.T,
) {
	srv, fixture := artifactPrepareFixture(t)
	registerTestArtifactPublication(
		t, srv, fixture, "apub_content_failed", "report.pdf", "sess_artifact",
	)
	failed := failTestArtifactPublication(
		t, srv, fixture, "apub_content_failed", "local_snapshot_unavailable",
	)
	require.Equal(t, http.StatusOK, failed.Code, failed.Body.String())

	rec := getTestArtifactPublicationContent(
		t, srv, fixture.userEmail, "apub_content_failed", "inline",
	)

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	data := decodeArtifactPublicationContent(t, rec)
	assert.Equal(t, "failed", data.Status)
	assert.False(t, data.Retryable)
	assert.Equal(t, "local_snapshot_unavailable", data.Publication.ErrorCode)
}

func TestArtifactPublicationContentGivenAvailableSafePreviewWhenReadThenPinsGeneration(
	t *testing.T,
) {
	srv, fixture, publicationID := availableArtifactPublicationFixture(
		t, "report.pdf", "application/pdf",
	)

	rec := getTestArtifactPublicationContent(
		t, srv, fixture.userEmail, publicationID, "inline",
	)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeArtifactPublicationContent(t, rec)
	assert.Equal(t, "available", data.Status)
	assert.Equal(t, "pdf", data.PreviewKind)
	assert.Equal(t, "inline", data.Disposition)
	assert.NotEmpty(t, data.URL)
	backend := srv.paxdArtifacts.(*fakePaxdArtifactBackend)
	assert.Equal(t, int64(99), backend.signedArtifact.Generation)
	assert.Contains(t, backend.signedQuery["response-content-disposition"], "inline")
	metadata := getArtifactPublication(t, srv, fixture.userEmail, publicationID)
	require.Equal(t, http.StatusOK, metadata.Code, metadata.Body.String())
	assert.Contains(t, metadata.Body.String(), `"artifact":{"artifact_id":`)
}

func TestArtifactPublicationContentGivenUnsafeInlineTypeWhenReadThenForcesDownload(
	t *testing.T,
) {
	srv, fixture, publicationID := availableArtifactPublicationFixture(
		t, "page.html", "text/html",
	)

	rec := getTestArtifactPublicationContent(
		t, srv, fixture.userEmail, publicationID, "inline",
	)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeArtifactPublicationContent(t, rec)
	assert.Equal(t, "download", data.PreviewKind)
	assert.Equal(t, "attachment", data.Disposition)
	backend := srv.paxdArtifacts.(*fakePaxdArtifactBackend)
	assert.Contains(t, backend.signedQuery["response-content-disposition"], "attachment")
}

func availableArtifactPublicationFixture(
	t *testing.T,
	filename string,
	contentType string,
) (*Server, conversationTestFixture, string) {
	t.Helper()
	srv, fixture := artifactPrepareFixture(t)
	publicationID := "apub_content_available"
	registerTestArtifactPublication(
		t, srv, fixture, publicationID, filename, "sess_artifact",
	)
	srv.paxdArtifacts.(*fakePaxdArtifactBackend).attrs.ContentType = contentType
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/artifact-publications/"+publicationID+"/prepare",
		bytes.NewReader([]byte(`{
			"filename":"`+filename+`",
			"content_type":"`+contentType+`",
			"size_bytes":12,
			"sha256":"`+artifactPrepareTestSHA+`"
		}`)),
	)
	req.Header.Set("X-Pax-Key", fixture.nodeAPIKey)
	setJSON(req)
	preparedRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(preparedRec, req)
	prepared := decodeArtifactPrepare(t, preparedRec)
	completed := completeTestArtifactUpload(t, srv, fixture, prepared.Upload.UploadID)
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	return srv, fixture, publicationID
}

func getTestArtifactPublicationContent(
	t *testing.T,
	srv *Server,
	email string,
	publicationID string,
	disposition string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/artifact-publications/"+publicationID+
			"/content/main?disposition="+disposition,
		nil,
	)
	req.Header.Set("X-User-Email", email)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func decodeArtifactPublicationContent(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) ArtifactPublicationContentData {
	t.Helper()
	var response struct {
		Data ArtifactPublicationContentData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	return response.Data
}
