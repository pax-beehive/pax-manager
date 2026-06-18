//go:build integration

package integration_test

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

const paxdArtifactUploader = "release-bot@example.iam.gserviceaccount.com"

type paxdArtifact struct {
	ArtifactID string   `json:"artifact_id"`
	Platform   string   `json:"platform"`
	Tags       []string `json:"tags"`
	Version    string   `json:"version"`
	BuildID    string   `json:"build_id"`
	Bucket     string   `json:"bucket"`
	Object     string   `json:"object"`
	Generation int64    `json:"generation"`
	SHA256     string   `json:"sha256"`
	SizeBytes  int64    `json:"size_bytes"`
	CreatedBy  string   `json:"created_by"`
}

type publishPaxdArtifactResponse struct {
	Artifact paxdArtifact `json:"artifact"`
}

type paxdArtifactDownloadResponse struct {
	URL        string       `json:"url"`
	Artifact   paxdArtifact `json:"artifact"`
	SHA256     string       `json:"sha256"`
	SizeBytes  int64        `json:"size_bytes"`
	Version    string       `json:"version"`
	Platform   string       `json:"platform"`
	Tags       []string     `json:"tags"`
	Generation int64        `json:"generation"`
}

func TestPaxdArtifactPublishAndDownloadIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)
	testTag := "itest-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	first := publishPaxdArtifact(
		t,
		fixture,
		testTag,
		"v0.1.0",
		"paxd/"+testTag+"/v0.1.0/linux-amd64/paxd",
		1001,
		"1111111111111111111111111111111111111111111111111111111111111111",
	)
	if first.Artifact.CreatedBy != paxdArtifactUploader {
		t.Fatalf("created_by = %q", first.Artifact.CreatedBy)
	}

	firstDownload := downloadPaxdArtifact(t, fixture, testTag)
	assertPaxdArtifactDownload(
		t,
		firstDownload,
		"v0.1.0",
		"paxd/"+testTag+"/v0.1.0/linux-amd64/paxd",
		1001,
	)

	second := publishPaxdArtifact(
		t,
		fixture,
		testTag,
		"v0.2.0",
		"paxd/"+testTag+"/v0.2.0/linux-amd64/paxd",
		1002,
		"2222222222222222222222222222222222222222222222222222222222222222",
	)
	if second.Artifact.ArtifactID == first.Artifact.ArtifactID {
		t.Fatalf("second publish reused first artifact id: %s", second.Artifact.ArtifactID)
	}

	secondDownload := downloadPaxdArtifact(t, fixture, testTag)
	assertPaxdArtifactDownload(
		t,
		secondDownload,
		"v0.2.0",
		"paxd/"+testTag+"/v0.2.0/linux-amd64/paxd",
		1002,
	)
	if secondDownload.URL == firstDownload.URL {
		t.Fatalf("download url did not update: %s", secondDownload.URL)
	}
}

func publishPaxdArtifact(
	t *testing.T,
	fixture *integrationFixture,
	testTag string,
	version string,
	object string,
	generation int64,
	sha256 string,
) publishPaxdArtifactResponse {
	t.Helper()
	return postJSON[publishPaxdArtifactResponse](
		t,
		fixture,
		"/api/v1/admin/paxd/artifacts",
		map[string]any{
			"platform":   "linux/amd64",
			"tags":       []string{"stable", "latest", testTag},
			"version":    version,
			"build_id":   "build-" + version,
			"bucket":     "paxd-releases",
			"object":     object,
			"generation": generation,
			"sha256":     sha256,
			"size_bytes": 4096,
		},
		map[string]string{"Authorization": "Bearer mock-gcs:" + paxdArtifactUploader},
		http.StatusOK,
	)
}

func downloadPaxdArtifact(
	t *testing.T,
	fixture *integrationFixture,
	testTag string,
) paxdArtifactDownloadResponse {
	t.Helper()
	return getJSON[paxdArtifactDownloadResponse](
		t,
		fixture,
		"/api/v1/public/paxd/download?platform=linux/amd64&tags=stable&tags="+testTag,
		nil,
		http.StatusOK,
	)
}

func assertPaxdArtifactDownload(
	t *testing.T,
	download paxdArtifactDownloadResponse,
	version string,
	object string,
	generation int64,
) {
	t.Helper()
	if download.Version != version ||
		download.Artifact.Version != version ||
		download.Artifact.Object != object ||
		download.Generation != generation ||
		download.Artifact.Generation != generation {
		t.Fatalf("unexpected download artifact: %+v", download)
	}
	signedURL, err := url.Parse(download.URL)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}
	if signedURL.Host != "mock-gcs.local" ||
		signedURL.Query().Get("object") != object ||
		signedURL.Query().Get("generation") != strconv.FormatInt(generation, 10) {
		t.Fatalf(
			"unexpected signed URL: host=%q object=%q generation=%q url=%s",
			signedURL.Host,
			signedURL.Query().Get("object"),
			signedURL.Query().Get("generation"),
			download.URL,
		)
	}
}
