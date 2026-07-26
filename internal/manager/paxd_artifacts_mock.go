package manager

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

type mockPaxdArtifactBackend struct{}

func (b mockPaxdArtifactBackend) SignDownloadURL(
	ctx context.Context,
	artifact PaxdArtifact,
	expiresAt time.Time,
) (string, error) {
	return b.SignObjectDownloadURL(
		ctx,
		artifact.Bucket,
		artifact.Object,
		artifact.Generation,
		expiresAt,
		nil,
	)
}

func (b mockPaxdArtifactBackend) SignObjectDownloadURL(
	ctx context.Context,
	bucket string,
	object string,
	generation int64,
	expiresAt time.Time,
	extraQuery map[string]string,
) (string, error) {
	values := url.Values{}
	values.Set("bucket", bucket)
	values.Set("object", object)
	values.Set("generation", strconv.FormatInt(generation, 10))
	values.Set("expires", expiresAt.UTC().Format(time.RFC3339))
	for key, value := range extraQuery {
		values.Set(key, value)
	}
	return "https://mock-gcs.local/paxd/download?" + values.Encode(), nil
}

func (b mockPaxdArtifactBackend) SignUploadURL(
	ctx context.Context,
	bucket string,
	object string,
	contentType string,
	expiresAt time.Time,
) (string, error) {
	values := url.Values{}
	values.Set("bucket", bucket)
	values.Set("object", object)
	values.Set("content_type", contentType)
	values.Set("expires", expiresAt.UTC().Format(time.RFC3339))
	return "https://mock-gcs.local/paxd/upload?" + values.Encode(), nil
}

func (b mockPaxdArtifactBackend) SignResumableUploadURL(
	ctx context.Context,
	bucket string,
	object string,
	contentType string,
	sha256 string,
	expiresAt time.Time,
) (string, error) {
	values := url.Values{}
	values.Set("bucket", bucket)
	values.Set("object", object)
	values.Set("content_type", contentType)
	values.Set("expires", expiresAt.UTC().Format(time.RFC3339))
	return "https://mock-gcs.local/paxd/resumable-upload?" + values.Encode(), nil
}

func (b mockPaxdArtifactBackend) VerifyUploader(
	ctx context.Context,
	token string,
	audience string,
) (string, error) {
	principal, ok := strings.CutPrefix(token, "mock-gcs:")
	if !ok || principal == "" {
		return "", apperr.Error{Status: http.StatusUnauthorized, Message: "invalid bearer token"}
	}
	return principal, nil
}

func (b mockPaxdArtifactBackend) ObjectAttrs(
	ctx context.Context,
	bucket string,
	object string,
	generation int64,
) (paxdArtifactObjectAttrs, error) {
	if generation <= 0 {
		generation = time.Now().UTC().UnixNano()
	}
	return paxdArtifactObjectAttrs{
		Generation:  generation,
		SizeBytes:   0,
		ContentType: "application/octet-stream",
	}, nil
}
