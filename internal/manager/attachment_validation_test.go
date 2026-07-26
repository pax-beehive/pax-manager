package manager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserAttachmentGivenParentDirectoryFilenameWhenCreatedThenItIsRejected(t *testing.T) {
	srv, _ := testServer(t, "attachment-path@example.com")
	srv.cfg.SessionArtifactGCSBucket = "session-artifacts-test"
	srv.paxdArtifacts = &fakePaxdArtifactBackend{}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/attachments",
		strings.NewReader(`{"filename":"..","content_type":"text/plain"}`),
	)
	req.Header.Set("X-User-Email", "attachment-path@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
