package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGivenDisabledBinaryWhenResolvingThenNeverOfferDownload(t *testing.T) {
	srv, _ := testServer(t, "quality@example.com")
	backend := &fakePaxdArtifactBackend{}
	srv.paxdArtifacts = backend
	_, err := srv.store.CreatePaxdArtifact(context.Background(), CreatePaxdArtifactRequest{
		Product: "paxd", Platform: "linux/amd64", Version: "0.1.49",
		Bucket: "releases", Object: "paxd/0.1.49", Tags: []string{"stable", "disabled"},
	}, "publisher")
	require.NoError(t, err)
	for _, query := range []string{"", "&tags=stable", "&tags=disabled", "&version=0.1.49"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/public/paxd/download?platform=linux/amd64"+query, nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/public/paxd/download?platform=linux/amd64&version=0.1.49&metadata=1", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	data := decodeData[map[string]any](t, rec.Body.Bytes())
	require.Equal(t, "disabled", data["status"])
	require.Equal(t, "BINARY_DISABLED_UPGRADE_RECOMMENDED", data["warning"])
	require.NotContains(t, data, "url")
	require.Empty(t, backend.signedArtifact.ArtifactID)
}

func TestGivenLegacyTagsWhenNormalizingThenOneQualityLabelRemains(t *testing.T) {
	for _, tc := range []struct {
		input []string
		want  []string
	}{
		{nil, []string{"testing"}},
		{[]string{"installer", "stable", "testing"}, []string{"installer", "stable"}},
		{[]string{"stable", "disabled", "testing"}, []string{"disabled"}},
		{[]string{"testing"}, []string{"testing"}},
	} {
		require.Equal(t, tc.want, normalizeBinaryQualityTags(tc.input))
	}
}

func TestGivenKnownBadCurrentVersionWhenCheckingUpdateThenRecommendReplacement(t *testing.T) {
	srv, _ := testServer(t, "quality@example.com")
	srv.paxdArtifacts = &fakePaxdArtifactBackend{}
	for _, item := range []struct{ version, state string }{{"0.1.48", "stable"}, {"0.1.49", "disabled"}} {
		_, err := srv.store.CreatePaxdArtifact(context.Background(), CreatePaxdArtifactRequest{
			Product: "paxd", Platform: "linux/amd64", Version: item.version, Bucket: "releases",
			Object: "paxd/" + item.version, Tags: []string{item.state},
		}, "publisher")
		require.NoError(t, err)
	}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/public/paxd/download?platform=linux/amd64&tags=stable&current_version=0.1.49",
		nil,
	))
	require.Equal(t, http.StatusOK, rec.Code)
	data := decodeData[map[string]any](t, rec.Body.Bytes())
	require.Equal(t, "0.1.48", data["version"])
	require.Equal(t, "disabled", data["current_status"])
	require.Equal(t, "BINARY_DISABLED_UPGRADE_RECOMMENDED", data["warning"])
}

func TestGivenOnlyDisabledBinaryWhenCheckingThenWarnEvenWithoutReplacement(t *testing.T) {
	srv, _ := testServer(t, "quality@example.com")
	_, err := srv.store.CreatePaxdArtifact(
		context.Background(),
		CreatePaxdArtifactRequest{
			Product:  "paxl",
			Platform: "linux/amd64",
			Version:  "0.1.49",
			Bucket:   "r",
			Object:   "bad",
			Tags:     []string{"disabled"},
		},
		"publisher",
	)
	require.NoError(t, err)
	for _, query := range []string{"&current_version=0.1.49", "&metadata=1", "&metadata=1&version=missing"} {
		rec := httptest.NewRecorder()
		srv.routes().
			ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/public/paxl/download?platform=linux/amd64"+query, nil))
		switch query {
		case "&current_version=0.1.49":
			require.Equal(t, http.StatusGone, rec.Code)
			require.Contains(t, rec.Body.String(), "known issues")
		case "&metadata=1":
			require.Equal(t, http.StatusBadRequest, rec.Code)
		default:
			require.Equal(t, http.StatusNotFound, rec.Code)
		}
	}
}

func TestGivenLegacyStableAndTestingTagsThenOnlyStableIsDownloadable(t *testing.T) {
	srv, _ := testServer(t, "quality@example.com")
	srv.paxdArtifacts = &fakePaxdArtifactBackend{}
	_, err := srv.store.CreatePaxdArtifact(
		context.Background(),
		CreatePaxdArtifactRequest{
			Product:  "paxd",
			Platform: "linux/amd64",
			Version:  "0.1.49",
			Bucket:   "r",
			Object:   "legacy",
			Tags:     []string{"testing", "stable"},
		},
		"publisher",
	)
	require.NoError(t, err)
	for _, state := range []string{"stable", "testing"} {
		rec := httptest.NewRecorder()
		srv.routes().
			ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/public/paxd/download?platform=linux/amd64&tags="+state, nil))
		if state == "testing" {
			require.Equal(t, http.StatusNotFound, rec.Code)
		} else {
			require.Equal(t, http.StatusOK, rec.Code)
			data := decodeData[PaxdArtifactDownloadResponse](t, rec.Body.Bytes())
			require.Equal(t, []string{"stable"}, data.Tags)
		}
	}
}
