package manager

import (
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifactPreviewPolicyGivenMediaTypeWhenClassifiedThenOnlySafeTypesAreInline(
	t *testing.T,
) {
	for _, test := range []struct {
		contentType string
		kind        string
		inline      bool
	}{
		{"audio/mpeg", "audio", true},
		{"audio/mp4; codecs=mp4a.40.2", "audio", true},
		{" VIDEO/MP4 ; codecs=avc1 ", "video", true},
		{"video/webm", "video", true},
		{"image/png", "image", true},
		{"image/svg+xml", "download", false},
		{"application/pdf", "pdf", true},
		{"text/plain", "text", true},
		{"text/markdown", "markdown", true},
		{"text/x-markdown", "markdown", true},
		{"application/json", "json", true},
		{"application/ld+json", "json", true},
		{"text/html", "download", false},
		{"application/octet-stream", "download", false},
		{"video/mp4; invalid", "video", true},
		{"", "download", false},
	} {
		t.Run(test.contentType, func(t *testing.T) {
			kind, inline := artifactPreviewPolicy(test.contentType)
			assert.Equal(t, test.kind, kind)
			assert.Equal(t, test.inline, inline)
		})
	}
}

func TestArtifactMediaContentGivenAvailableMediaWhenReadThenPreservesInlineAndDownload(
	t *testing.T,
) {
	for _, contentType := range []string{"audio/mp4", "video/mp4"} {
		for _, publication := range []bool{false, true} {
			for _, disposition := range []string{"inline", "attachment"} {
				name := contentType + "/artifact/" + disposition
				if publication {
					name = contentType + "/publication/" + disposition
				}
				t.Run(name, func(t *testing.T) {
					srv, email, path := artifactMediaContentFixture(t, contentType, publication)

					rec := requestArtifactMediaContent(
						t,
						srv,
						email,
						path+"?disposition="+disposition,
					)

					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					backend := srv.paxdArtifacts.(*fakePaxdArtifactBackend)
					assert.Equal(t, contentType, backend.signedQuery["response-content-type"])
					gotDisposition, _, err := mime.ParseMediaType(
						backend.signedQuery["response-content-disposition"],
					)
					require.NoError(t, err)
					assert.Equal(t, disposition, gotDisposition)
					assert.Equal(t, int64(99), backend.signedArtifact.Generation)
					if publication {
						data := decodeArtifactPublicationContent(t, rec)
						assert.Equal(t, contentType[:5], data.PreviewKind)
						assert.Equal(t, disposition, data.Disposition)
					}
				})
			}
		}
	}
}

func TestArtifactMediaContentGivenUnavailableIdentityWhenReadThenDoesNotSignURL(t *testing.T) {
	for _, publication := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "artifact", true: "publication"}[publication],
			func(t *testing.T) {
				srv, _, path := artifactMediaContentFixture(t, "video/mp4", publication)
				backend := srv.paxdArtifacts.(*fakePaxdArtifactBackend)

				otherOwner := requestArtifactMediaContent(t, srv, "other@example.com", path)

				assert.Equal(t, http.StatusNotFound, otherOwner.Code, otherOwner.Body.String())
				assert.Empty(t, backend.signedQuery)
				assert.Empty(t, backend.signedArtifact.Object)

				anonymous := newServer(Config{}, srv.store)
				anonymous.paxdArtifacts = backend
				t.Cleanup(func() {
					require.NoError(t, anonymous.CloseTransportStore(context.Background()))
				})
				unauthenticated := requestArtifactMediaContent(t, anonymous, "", path)

				assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
				assert.Empty(t, backend.signedQuery)
				assert.Empty(t, backend.signedArtifact.Object)
			},
		)
	}
}

func TestArtifactMediaContentGivenExpiredTicketWhenReadAgainThenRenewsThroughExistingRoute(
	t *testing.T,
) {
	for _, publication := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "artifact", true: "publication"}[publication],
			func(t *testing.T) {
				srv, email, path := artifactMediaContentFixture(t, "video/mp4", publication)
				now := srv.clock()
				srv.clock = func() time.Time { return now }
				backend := srv.paxdArtifacts.(*fakePaxdArtifactBackend)
				first := requestArtifactMediaContent(t, srv, email, path)
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				firstExpiry := backend.expiresAt
				firstObject := backend.signedArtifact
				assert.Equal(t, now.Add(15*time.Minute), firstExpiry)

				now = firstExpiry.Add(time.Minute)
				renewed := requestArtifactMediaContent(t, srv, email, path)

				require.Equal(t, http.StatusOK, renewed.Code, renewed.Body.String())
				assert.Equal(t, now.Add(15*time.Minute), backend.expiresAt)
				assert.True(t, backend.expiresAt.After(firstExpiry))
				assert.Equal(t, firstObject, backend.signedArtifact)
				response := decodeData[struct {
					ExpiresAt time.Time `json:"expires_at"`
				}](t, renewed.Body.Bytes())
				assert.Equal(t, backend.expiresAt, response.ExpiresAt)
			},
		)
	}
}

func artifactMediaContentFixture(
	t *testing.T,
	contentType string,
	publication bool,
) (*Server, string, string) {
	t.Helper()
	srv, fixture, publicationID := availableArtifactPublicationFixture(
		t, "media.mp4", contentType,
	)
	path := "/api/v1/user/self/artifact-publications/" + publicationID + "/content/main"
	if !publication {
		metadata, err := srv.store.GetArtifactPublication(
			context.Background(), testUserPrincipal(t, srv, fixture.userEmail), publicationID,
		)
		require.NoError(t, err)
		path = "/api/v1/user/self/artifacts/" + metadata.ArtifactID + "/content/main"
	}
	return srv, fixture.userEmail, path
}

func requestArtifactMediaContent(
	t *testing.T,
	srv *Server,
	email string,
	path string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if email != "" {
		req.Header.Set("X-User-Email", email)
	}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}
