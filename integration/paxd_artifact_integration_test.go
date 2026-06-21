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
	Product    string   `json:"product"`
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

func TestPaxlInstallerPublishAndRedirectIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)
	testTag := "itest-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	object := "paxl/" + testTag + "/v0.1.0/install.sh"
	generation := int64(2001)

	published := postJSON[publishPaxdArtifactResponse](
		t,
		fixture,
		"/api/v1/admin/artifacts",
		map[string]any{
			"product":      "paxl",
			"platform":     "script",
			"tags":         []string{"stable", "installer", testTag},
			"version":      "v0.1.0",
			"build_id":     "build-v0.1.0",
			"bucket":       "paxl-releases",
			"object":       object,
			"generation":   generation,
			"sha256":       "3333333333333333333333333333333333333333333333333333333333333333",
			"size_bytes":   1024,
			"content_type": "text/x-shellscript",
		},
		map[string]string{"Authorization": "Bearer mock-gcs:" + paxdArtifactUploader},
		http.StatusOK,
	)
	if published.Artifact.Product != "paxl" ||
		published.Artifact.Platform != "script" ||
		published.Artifact.Object != object {
		t.Fatalf("published installer artifact = %+v", published.Artifact)
	}

	client := *fixture.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req := fixture.newRequest(t, http.MethodGet, "/api/v1/public/paxl/install.sh", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get paxl installer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_ = readAll(t, resp)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("paxl installer status = %d, want %d", resp.StatusCode, http.StatusFound)
	}

	location := resp.Header.Get("Location")
	signedURL, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse installer location: %v", err)
	}
	if signedURL.Host != "mock-gcs.local" ||
		signedURL.Query().Get("object") != object ||
		signedURL.Query().Get("generation") != strconv.FormatInt(generation, 10) {
		t.Fatalf(
			"unexpected installer location: host=%q object=%q generation=%q url=%s",
			signedURL.Host,
			signedURL.Query().Get("object"),
			signedURL.Query().Get("generation"),
			location,
		)
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
