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
const emptyArtifactTestSHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

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
	require.Equal(t, "s3_presigned_put", firstPrepared.Upload.Protocol)
	require.Equal(t, http.MethodPut, firstPrepared.Upload.Method)
	require.Equal(t, "*", firstPrepared.Upload.Headers["If-None-Match"])
	require.Equal(
		t,
		"ASNFZ4mrze8BI0VniavN7wEjRWeJq83vASNFZ4mrze8=",
		firstPrepared.Upload.Headers["x-amz-checksum-sha256"],
	)
	require.Equal(t, artifactPrepareTestSHA, firstPrepared.Upload.Headers["x-amz-meta-sha256"])
	require.Zero(t, firstPrepared.Upload.ChunkAlignment)
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

func TestNodeArtifactUploadGivenDeclaredZeroBytesWhenCompletingThenSizeMustMatch(t *testing.T) {
	tests := []struct {
		name       string
		storedSize int64
		wantStatus int
	}{
		{name: "empty object", storedSize: 0, wantStatus: http.StatusOK},
		{name: "non-empty object", storedSize: 1, wantStatus: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, fixture := artifactPrepareFixture(t)
			registerTestArtifactPublication(
				t,
				srv,
				fixture,
				"apub_zero",
				"empty.txt",
				"sess_artifact",
			)
			backend := srv.paxdArtifacts.(*fakePaxdArtifactBackend)
			backend.attrs.SizeBytes = test.storedSize
			backend.attrs.ContentType = "text/plain"
			backend.attrs.SHA256 = emptyArtifactTestSHA

			prepared := prepareTestArtifactWithSize(
				t,
				srv,
				fixture,
				"apub_zero",
				"empty.txt",
				"text/plain",
				0,
				emptyArtifactTestSHA,
			)
			require.Equal(t, http.StatusOK, prepared.Code, prepared.Body.String())
			ticket := decodeArtifactPrepare(t, prepared)

			completed := completeTestArtifactUpload(t, srv, fixture, ticket.Upload.UploadID)

			require.Equal(t, test.wantStatus, completed.Code, completed.Body.String())
			if test.wantStatus == http.StatusConflict {
				assert.Contains(t, completed.Body.String(), "size does not match")
			}
		})
	}
}

type artifactPrepareTestData struct {
	Status     string `json:"status"`
	ArtifactID string `json:"artifact_id"`
	Upload     struct {
		UploadID       string            `json:"upload_id"`
		Protocol       string            `json:"protocol"`
		Method         string            `json:"method"`
		URL            string            `json:"url"`
		ChunkAlignment int64             `json:"chunk_alignment"`
		Headers        map[string]string `json:"headers"`
	} `json:"upload"`
}

func artifactPrepareFixture(t *testing.T) (*Server, conversationTestFixture) {
	t.Helper()
	srv, _ := testServer(t, "artifact-owner@example.com")
	srv.cfg.ObjectStorageBucket = "session-artifacts-test"
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

func prepareTestArtifactWithSize(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
	publicationID string,
	filename string,
	contentType string,
	sizeBytes int64,
	sha256 string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"filename":     filename,
		"content_type": contentType,
		"size_bytes":   sizeBytes,
		"sha256":       sha256,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/artifact-publications/"+publicationID+"/prepare",
		bytes.NewReader(body),
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
