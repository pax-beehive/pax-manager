//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

const integrationPublisherEmail = "local@example.local"

var integrationArtifactBucket = integrationObjectStorageBucket()

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
	publisherAPIKey := createIntegrationPublisherAPIKey(t, fixture)
	testTag := "itest-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	firstObject := "paxd/" + testTag + "/v0.1.0/linux-amd64/paxd"
	firstPayload := bytes.Repeat([]byte("1"), 4096)
	firstSHA := putIntegrationArtifact(
		t,
		firstObject,
		firstPayload,
		"application/octet-stream",
	)
	first := publishPaxdArtifact(
		t,
		fixture,
		publisherAPIKey,
		testTag,
		"v0.1.0",
		firstObject,
		firstSHA,
	)
	if first.Artifact.CreatedBy != integrationPublisherEmail {
		t.Fatalf("created_by = %q", first.Artifact.CreatedBy)
	}

	firstDownload := downloadPaxdArtifact(t, fixture, testTag)
	assertPaxdArtifactDownload(t, firstDownload, "v0.1.0", firstObject)
	require.Equal(t, firstSHA, firstDownload.SHA256)
	assertIntegrationArtifactBody(t, firstDownload.URL, firstPayload, firstDownload.SHA256)

	secondObject := "paxd/" + testTag + "/v0.2.0/linux-amd64/paxd"
	secondPayload := bytes.Repeat([]byte("2"), 4096)
	secondSHA := putIntegrationArtifact(
		t,
		secondObject,
		secondPayload,
		"application/octet-stream",
	)
	second := publishPaxdArtifact(
		t,
		fixture,
		publisherAPIKey,
		testTag,
		"v0.2.0",
		secondObject,
		secondSHA,
	)
	if second.Artifact.ArtifactID == first.Artifact.ArtifactID {
		t.Fatalf("second publish reused first artifact id: %s", second.Artifact.ArtifactID)
	}

	secondDownload := downloadPaxdArtifact(t, fixture, testTag)
	assertPaxdArtifactDownload(t, secondDownload, "v0.2.0", secondObject)
	require.Equal(t, secondSHA, secondDownload.SHA256)
	assertIntegrationArtifactBody(t, secondDownload.URL, secondPayload, secondDownload.SHA256)
	require.False(t, secondDownload.URL == firstDownload.URL, "download URL did not update")
}

func TestPaxlInstallerPublishAndRedirectIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)
	publisherAPIKey := createIntegrationPublisherAPIKey(t, fixture)
	testTag := "itest-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	object := "paxl/" + testTag + "/v0.1.0/install.sh"
	installerPayload := []byte("#!/bin/sh\nprintf 'paxl integration installer\\n'\n")
	objectSHA := putIntegrationArtifact(
		t,
		object,
		installerPayload,
		"text/x-shellscript",
	)

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
			"bucket":       integrationArtifactBucket,
			"object":       object,
			"generation":   0,
			"sha256":       objectSHA,
			"size_bytes":   len(installerPayload),
			"content_type": "text/x-shellscript",
		},
		map[string]string{"Authorization": "Bearer " + publisherAPIKey},
		http.StatusOK,
	)
	if published.Artifact.Product != "paxl" ||
		published.Artifact.Platform != "script" ||
		published.Artifact.Object != object ||
		published.Artifact.Generation <= 0 {
		t.Fatalf("published installer artifact = %+v", published.Artifact)
	}

	client := *fixture.client
	redirectCount := 0
	redirectTarget := ""
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		redirectCount = len(via)
		if redirectCount == 1 {
			redirectTarget = req.URL.String()
			return nil
		}
		return http.ErrUseLastResponse
	}
	req := fixture.newRequest(t, http.MethodGet, "/api/v1/public/paxl/install.sh", nil)
	resp, err := client.Do(req)
	if err != nil {
		require.FailNow(t, "get paxl installer", "request failed without exposing signed URL")
	}
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "paxl installer download status")
	require.Equal(t, 1, redirectCount, "paxl installer redirect count")
	assertIntegrationSignedURL(t, redirectTarget, object)
	require.Equal(t, "text/x-shellscript", resp.Header.Get("Content-Type"))
	assertIntegrationArtifactPayload(t, readAll(t, resp), installerPayload, objectSHA)
}

func TestObjectStorageGivenWriteOnceAndChecksumHeadersWhenPuttingThenMinIOEnforcesThem(
	t *testing.T,
) {
	client := integrationObjectStorageClient()
	payload := []byte("verified integration payload")
	correctDigest := sha256.Sum256(payload)
	wrongDigest := sha256.Sum256([]byte("different payload"))
	objectPrefix := "storage-contract/" + strconv.FormatInt(time.Now().UnixNano(), 10)

	_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:         aws.String(integrationArtifactBucket),
		Key:            aws.String(objectPrefix + "/bad-checksum"),
		Body:           bytes.NewReader(payload),
		ContentLength:  aws.Int64(int64(len(payload))),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(wrongDigest[:])),
		IfNoneMatch:    aws.String("*"),
	})
	require.Error(t, err, "MinIO must reject a body that does not match ChecksumSHA256")

	writeOnceInput := func() *s3.PutObjectInput {
		return &s3.PutObjectInput{
			Bucket:         aws.String(integrationArtifactBucket),
			Key:            aws.String(objectPrefix + "/write-once"),
			Body:           bytes.NewReader(payload),
			ContentLength:  aws.Int64(int64(len(payload))),
			ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(correctDigest[:])),
			IfNoneMatch:    aws.String("*"),
		}
	}
	_, err = client.PutObject(context.Background(), writeOnceInput())
	require.NoError(t, err)
	_, err = client.PutObject(context.Background(), writeOnceInput())
	require.Error(t, err, "MinIO must reject an overwrite guarded by If-None-Match")
}

func publishPaxdArtifact(
	t *testing.T,
	fixture *integrationFixture,
	publisherAPIKey string,
	testTag string,
	version string,
	object string,
	objectSHA string,
) publishPaxdArtifactResponse {
	t.Helper()
	return postJSON[publishPaxdArtifactResponse](
		t,
		fixture,
		"/api/v1/admin/paxd/artifacts",
		map[string]any{
			"platform":     "linux/amd64",
			"tags":         []string{"stable", "latest", testTag},
			"version":      version,
			"build_id":     "build-" + version,
			"bucket":       integrationArtifactBucket,
			"object":       object,
			"generation":   0,
			"sha256":       objectSHA,
			"size_bytes":   4096,
			"content_type": "application/octet-stream",
		},
		map[string]string{"Authorization": "Bearer " + publisherAPIKey},
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
) {
	t.Helper()
	require.Equal(t, version, download.Version)
	require.Equal(t, version, download.Artifact.Version)
	require.Equal(t, object, download.Artifact.Object)
	require.Positive(t, download.Generation)
	require.Equal(t, download.Generation, download.Artifact.Generation)
	assertIntegrationSignedURL(t, download.URL, object)
}

func assertIntegrationSignedURL(t *testing.T, rawURL string, object string) {
	t.Helper()
	signedURL, err := url.Parse(rawURL)
	if err != nil {
		require.FailNow(t, "parse signed URL", "signed artifact URL is invalid")
	}
	expectedEndpoint, err := url.Parse(integrationObjectStorageEndpoint())
	require.NoError(t, err)
	require.Equal(t, expectedEndpoint.Host, signedURL.Host)
	require.True(
		t,
		strings.HasSuffix(signedURL.Path, "/"+integrationArtifactBucket+"/"+object),
		"signed artifact path does not match the expected object",
	)
	require.NotEmpty(t, signedURL.Query().Get("X-Amz-Signature"))
}

func assertIntegrationArtifactBody(
	t *testing.T,
	rawURL string,
	wantPayload []byte,
	wantSHA string,
) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		require.FailNow(t, "create signed artifact request", "signed artifact URL is invalid")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		require.FailNow(t, "download signed artifact", "request failed without exposing signed URL")
	}
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "signed artifact download status")
	assertIntegrationArtifactPayload(t, readAll(t, resp), wantPayload, wantSHA)
}

func assertIntegrationArtifactPayload(
	t *testing.T,
	payload []byte,
	wantPayload []byte,
	wantSHA string,
) {
	t.Helper()
	require.Equal(t, wantPayload, payload)
	digest := sha256.Sum256(payload)
	require.Equal(t, wantSHA, hex.EncodeToString(digest[:]))
}

func createIntegrationPublisherAPIKey(
	t *testing.T,
	fixture *integrationFixture,
) string {
	t.Helper()
	created := postJSON[createUserAPIKeyResponse](
		t,
		fixture,
		"/api/user/api-keys",
		map[string]any{"name": "artifact publisher"},
		map[string]string{"X-User-Email": integrationPublisherEmail},
		http.StatusOK,
	)
	if created.Key == "" {
		t.Fatal("publisher API key is empty")
	}
	return created.Key
}

func putIntegrationArtifact(
	t *testing.T,
	object string,
	payload []byte,
	contentType string,
) string {
	t.Helper()
	digest := sha256.Sum256(payload)
	objectSHA := hex.EncodeToString(digest[:])
	checksumSHA256 := base64.StdEncoding.EncodeToString(digest[:])
	client := integrationObjectStorageClient()
	_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:         aws.String(integrationArtifactBucket),
		Key:            aws.String(object),
		Body:           bytes.NewReader(payload),
		ContentLength:  aws.Int64(int64(len(payload))),
		ContentType:    aws.String(contentType),
		ChecksumSHA256: aws.String(checksumSHA256),
		IfNoneMatch:    aws.String("*"),
		Metadata:       map[string]string{"sha256": objectSHA},
	})
	if err != nil {
		t.Fatalf("put integration artifact: %v", err)
	}
	return objectSHA
}

func integrationObjectStorageClient() *s3.Client {
	awsConfig := aws.Config{
		Region: "us-east-1",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			"minioadmin", "minioadmin", "",
		)),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	}
	return s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(integrationObjectStorageEndpoint())
		options.UsePathStyle = true
	})
}

func integrationObjectStorageEndpoint() string {
	if endpoint := strings.TrimSpace(os.Getenv("PAX_MANAGER_OBJECT_STORAGE_PUBLIC_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	return "http://localhost:19000"
}

func integrationObjectStorageBucket() string {
	if bucket := strings.TrimSpace(os.Getenv("PAX_MANAGER_OBJECT_STORAGE_BUCKET")); bucket != "" {
		return bucket
	}
	return "pax-artifacts"
}
