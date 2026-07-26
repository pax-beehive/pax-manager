package manager

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestArtifactPublicationGivenPermanentNodeFailureWhenReportedThenUserSeesFailed(
	t *testing.T,
) {
	srv, fixture := artifactPrepareFixture(t)
	registerTestArtifactPublication(
		t, srv, fixture, "apub_failed", "report.pdf", "sess_artifact",
	)

	first := failTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_failed",
		"local_snapshot_unavailable",
	)
	second := failTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_failed",
		"local_snapshot_unavailable",
	)

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.JSONEq(t, first.Body.String(), second.Body.String())
	read := getArtifactPublication(t, srv, fixture.userEmail, "apub_failed")
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	publication := decodeArtifactPublication(t, read)
	assert.Equal(t, domain.ArtifactPublicationStatusFailed, publication.Status)
	assert.Equal(t, "local_snapshot_unavailable", publication.ErrorCode)
}

func TestArtifactPublicationGivenAvailableArtifactWhenFailureArrivesThenDoesNotDowngrade(
	t *testing.T,
) {
	srv, fixture := artifactPrepareFixture(t)
	registerTestArtifactPublication(
		t, srv, fixture, "apub_available", "report.pdf", "sess_artifact",
	)
	prepared := decodeArtifactPrepare(
		t,
		prepareTestArtifact(
			t, srv, fixture, "apub_available", "report.pdf", artifactPrepareTestSHA,
		),
	)
	completed := completeTestArtifactUpload(t, srv, fixture, prepared.Upload.UploadID)
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())

	failed := failTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_available",
		"local_snapshot_unavailable",
	)

	require.Equal(t, http.StatusOK, failed.Code, failed.Body.String())
	publication := decodeArtifactPublication(t, failed)
	assert.Equal(t, domain.ArtifactPublicationStatusAvailable, publication.Status)
	assert.Empty(t, publication.ErrorCode)
}

func failTestArtifactPublication(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
	publicationID string,
	code string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/artifact-publications/"+publicationID+"/failed",
		bytes.NewReader([]byte(`{"error_code":"`+code+`","message":"background failed"}`)),
	)
	req.Header.Set("X-Pax-Key", fixture.nodeAPIKey)
	setJSON(req)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}
