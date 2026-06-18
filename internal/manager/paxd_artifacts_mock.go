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
	values := url.Values{}
	values.Set("bucket", artifact.Bucket)
	values.Set("object", artifact.Object)
	values.Set("generation", strconv.FormatInt(artifact.Generation, 10))
	values.Set("expires", expiresAt.UTC().Format(time.RFC3339))
	return "https://mock-gcs.local/paxd/download?" + values.Encode(), nil
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
		return paxdArtifactObjectAttrs{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "mock gcs requires generation",
		}
	}
	return paxdArtifactObjectAttrs{
		Generation:  generation,
		ContentType: "application/octet-stream",
	}, nil
}
