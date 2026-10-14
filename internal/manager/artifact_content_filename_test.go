package manager

import (
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestArtifactContentGivenFilenameWhenSigningThenDispositionIsASCIIAndLossless(t *testing.T) {
	filenames := map[string]string{
		"ascii":  "notes.txt",
		"quoted": `report "final"; draft.txt`,
		"unicode": "\u5728\u767e\u4e07\u8c6a\u88c5\u5f55\u97f3\u68da\u5927\u58f0\u542c" +
			"\u738b\u83f2\u300a\u7ea2\u8c46\u300b\u3010Hi-res\u3011 [BV1ec41157Je].m4a",
		"emoji": "caf\u00e9 \U0001f3b5.m4a",
	}
	for name, filename := range filenames {
		t.Run(name, func(t *testing.T) {
			srv, artifactID, backend := artifactContentFilenameFixture(t, filename)
			for _, disposition := range []string{"inline", "attachment"} {
				for _, redirect := range []bool{false, true} {
					mode := disposition + "/ticket"
					if redirect {
						mode = disposition + "/redirect"
					}
					t.Run(mode, func(t *testing.T) {
						path := "/api/v1/user/self/artifacts/" + artifactID + "/content/main"
						path += "?disposition=" + disposition
						if redirect {
							path += "&redirect=1"
						}
						req := httptest.NewRequest(http.MethodGet, path, nil)
						req.Header.Set("X-User-Email", "filename@example.com")
						rec := httptest.NewRecorder()
						srv.routes().ServeHTTP(rec, req)
						if redirect {
							require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
							assert.Equal(
								t,
								"https://signed.example/test/object",
								rec.Header().Get("Location"),
							)
						} else {
							require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
							result := decodeData[ArtifactContentURLResponse](t, rec.Body.Bytes())
							assert.Equal(t, filename, result.Content.Filename)
						}
						header := backend.signedQuery["response-content-disposition"]
						assert.NotRegexp(t, `[^\x00-\x7f]`, header)
						gotDisposition, params, err := mime.ParseMediaType(header)
						require.NoError(t, err)
						assert.Equal(t, disposition, gotDisposition)
						assert.Equal(t, filename, params["filename"])
						assert.Equal(t, "audio/mp4", backend.signedQuery["response-content-type"])
					})
				}
			}
		})
	}
}

func artifactContentFilenameFixture(
	t *testing.T,
	filename string,
) (*Server, string, *fakePaxdArtifactBackend) {
	t.Helper()
	srv, _ := testServer(t, "filename@example.com")
	srv.cfg.ObjectStorageBucket = "session-artifacts-test"
	backend := &fakePaxdArtifactBackend{}
	srv.paxdArtifacts = backend
	user, err := srv.store.GetUserByEmail(context.Background(), "filename@example.com")
	require.NoError(t, err)
	artifact, err := srv.store.CreateSessionArtifact(
		context.Background(),
		domain.UserPrincipal{User: user},
		CreateSessionArtifactRequest{
			Kind: "file",
			Contents: []ArtifactContent{{
				Ref:         "main",
				Bucket:      "session-artifacts-test",
				Object:      "test/object",
				Filename:    filename,
				ContentType: "audio/mp4",
			}},
		},
	)
	require.NoError(t, err)
	return srv, artifact.ArtifactID, backend
}
