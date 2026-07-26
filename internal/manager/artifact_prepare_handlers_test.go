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

const artifactPrepareTestSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestArtifactPrepareGivenSameNaturalIdentityWhenPreparedThenReusesOneUpload(t *testing.T) {
	srv, fixture := artifactPrepareFixture(t)
	registerTestArtifactPublication(t, srv, fixture, "apub_same_1", "report.pdf", "sess_artifact")
	registerTestArtifactPublication(t, srv, fixture, "apub_same_2", "report.pdf", "sess_artifact")

	first := prepareTestArtifact(
		t,
		srv,
		fixture,
		"apub_same_1",
		"report.pdf",
		artifactPrepareTestSHA,
	)
	second := prepareTestArtifact(
		t,
		srv,
		fixture,
		"apub_same_2",
		"report.pdf",
		artifactPrepareTestSHA,
	)

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	firstPrepared := decodeArtifactPrepare(t, first)
	secondPrepared := decodeArtifactPrepare(t, second)
	require.Equal(t, "upload_required", firstPrepared.Status)
	require.Equal(t, firstPrepared.ArtifactID, secondPrepared.ArtifactID)
	require.Equal(t, firstPrepared.Upload.UploadID, secondPrepared.Upload.UploadID)
	require.Equal(t, "gcs_resumable", firstPrepared.Upload.Protocol)
	require.Equal(t, http.MethodPost, firstPrepared.Upload.Method)
	require.Equal(t, int64(256*1024), firstPrepared.Upload.ChunkAlignment)
}

func TestArtifactPrepareGivenDifferentFilenameOrSessionWhenPreparedThenDoesNotReuseUpload(
	t *testing.T,
) {
	srv, fixture := artifactPrepareFixture(t)
	createArtifactPublicationSession(t, srv, fixture, "sess_artifact_other")
	registerTestArtifactPublication(t, srv, fixture, "apub_base", "report.pdf", "sess_artifact")
	registerTestArtifactPublication(t, srv, fixture, "apub_filename", "other.pdf", "sess_artifact")
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_session",
		"report.pdf",
		"sess_artifact_other",
	)

	base := decodeArtifactPrepare(
		t,
		prepareTestArtifact(t, srv, fixture, "apub_base", "report.pdf", artifactPrepareTestSHA),
	)
	filename := decodeArtifactPrepare(
		t,
		prepareTestArtifact(t, srv, fixture, "apub_filename", "other.pdf", artifactPrepareTestSHA),
	)
	session := decodeArtifactPrepare(
		t,
		prepareTestArtifact(t, srv, fixture, "apub_session", "report.pdf", artifactPrepareTestSHA),
	)

	assert.NotEqual(t, base.ArtifactID, filename.ArtifactID)
	assert.NotEqual(t, base.Upload.UploadID, filename.Upload.UploadID)
	assert.NotEqual(t, base.ArtifactID, session.ArtifactID)
	assert.NotEqual(t, base.Upload.UploadID, session.Upload.UploadID)
}

func TestArtifactPrepareGivenCompletedIdentityWhenPreparedAgainThenSkipsUpload(t *testing.T) {
	srv, fixture := artifactPrepareFixture(t)
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_complete_1",
		"report.pdf",
		"sess_artifact",
	)
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_complete_2",
		"report.pdf",
		"sess_artifact",
	)
	prepared := decodeArtifactPrepare(
		t,
		prepareTestArtifact(
			t,
			srv,
			fixture,
			"apub_complete_1",
			"report.pdf",
			artifactPrepareTestSHA,
		),
	)

	completed := completeTestArtifactUpload(t, srv, fixture, prepared.Upload.UploadID)
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	var completion struct {
		Data struct {
			Status     string `json:"status"`
			ArtifactID string `json:"artifact_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(completed.Body.Bytes(), &completion))
	require.Equal(t, "available", completion.Data.Status)
	require.Equal(t, prepared.ArtifactID, completion.Data.ArtifactID)

	reused := decodeArtifactPrepare(
		t,
		prepareTestArtifact(
			t,
			srv,
			fixture,
			"apub_complete_2",
			"report.pdf",
			artifactPrepareTestSHA,
		),
	)
	require.Equal(t, "available", reused.Status)
	require.Equal(t, prepared.ArtifactID, reused.ArtifactID)
	require.Empty(t, reused.Upload.UploadID)
}

func TestArtifactUploadGivenCompletedUploadWhenCompletedAgainThenReturnsSameArtifact(t *testing.T) {
	srv, fixture := artifactPrepareFixture(t)
	registerTestArtifactPublication(
		t,
		srv,
		fixture,
		"apub_idempotent",
		"report.pdf",
		"sess_artifact",
	)
	prepared := decodeArtifactPrepare(
		t,
		prepareTestArtifact(
			t,
			srv,
			fixture,
			"apub_idempotent",
			"report.pdf",
			artifactPrepareTestSHA,
		),
	)

	first := completeTestArtifactUpload(t, srv, fixture, prepared.Upload.UploadID)
	second := completeTestArtifactUpload(t, srv, fixture, prepared.Upload.UploadID)

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.JSONEq(t, first.Body.String(), second.Body.String())
}

func TestArtifactUploadGivenInvalidStoredHashWhenCompletedThenRejectsPublication(t *testing.T) {
	tests := []struct {
		name   string
		sha256 string
	}{
		{name: "missing hash metadata", sha256: ""},
		{
			name:   "mismatched hash metadata",
			sha256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, fixture := artifactPrepareFixture(t)
			registerTestArtifactPublication(
				t,
				srv,
				fixture,
				"apub_bad_hash",
				"report.pdf",
				"sess_artifact",
			)
			prepared := decodeArtifactPrepare(
				t,
				prepareTestArtifact(
					t, srv, fixture, "apub_bad_hash", "report.pdf", artifactPrepareTestSHA,
				),
			)
			backend := srv.paxdArtifacts.(*fakePaxdArtifactBackend)
			backend.attrs.SHA256 = tt.sha256

			completed := completeTestArtifactUpload(t, srv, fixture, prepared.Upload.UploadID)

			require.Equal(t, http.StatusConflict, completed.Code, completed.Body.String())
			publication := getArtifactPublication(
				t, srv, "artifact-owner@example.com", "apub_bad_hash",
			)
			require.Equal(t, http.StatusOK, publication.Code, publication.Body.String())
			assert.Contains(t, publication.Body.String(), `"status":"uploading"`)
		})
	}
}

type artifactPrepareTestData struct {
	Status     string `json:"status"`
	ArtifactID string `json:"artifact_id"`
	Upload     struct {
		UploadID       string `json:"upload_id"`
		Protocol       string `json:"protocol"`
		Method         string `json:"method"`
		URL            string `json:"url"`
		ChunkAlignment int64  `json:"chunk_alignment"`
	} `json:"upload"`
}

func artifactPrepareFixture(t *testing.T) (*Server, conversationTestFixture) {
	t.Helper()
	srv, _ := testServer(t, "artifact-owner@example.com")
	srv.cfg.SessionArtifactGCSBucket = "session-artifacts-test"
	srv.paxdArtifacts = &fakePaxdArtifactBackend{
		attrs: paxdArtifactObjectAttrs{
			Generation:  99,
			SizeBytes:   12,
			ContentType: "application/pdf",
			SHA256:      artifactPrepareTestSHA,
		},
	}
	fixture := testNodeAgent(t, srv, "artifact-owner@example.com")
	createArtifactPublicationSession(t, srv, fixture, "sess_artifact")
	return srv, fixture
}

func registerTestArtifactPublication(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
	publicationID string,
	filename string,
	sessionID string,
) {
	t.Helper()
	rec := registerArtifactPublication(
		t,
		srv,
		fixture.nodeAPIKey,
		publicationID,
		`{"source":{"agent_id":"`+fixture.agentID+`","session_id":"`+sessionID+
			`"},"filename":"`+filename+`"}`,
	)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func prepareTestArtifact(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
	publicationID string,
	filename string,
	sha256 string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/artifact-publications/"+publicationID+"/prepare",
		bytes.NewReader([]byte(`{
			"filename":"`+filename+`",
			"content_type":"application/pdf",
			"size_bytes":12,
			"sha256":"`+sha256+`"
		}`)),
	)
	req.Header.Set("X-Pax-Key", fixture.nodeAPIKey)
	setJSON(req)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func completeTestArtifactUpload(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
	uploadID string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/artifact-uploads/"+uploadID+"/complete",
		nil,
	)
	req.Header.Set("X-Pax-Key", fixture.nodeAPIKey)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func decodeArtifactPrepare(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) artifactPrepareTestData {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var response struct {
		Data artifactPrepareTestData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	return response.Data
}
