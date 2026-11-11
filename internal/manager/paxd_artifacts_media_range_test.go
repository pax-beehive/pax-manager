package manager

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3MediaGivenSignedDownloadWhenSeekingThenRangeIsNotBoundBySignature(t *testing.T) {
	ctx := context.Background()
	creds := aws.Credentials{AccessKeyID: "access-key", SecretAccessKey: "secret-key"}
	presigner := newObjectStorageS3Presigner(newObjectStorageS3Client(aws.Config{
		Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			creds.AccessKeyID, creds.SecretAccessKey, "",
		),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}, "https://objects.example.com", true))
	backend := newS3PaxdArtifactBackendWithClients("pax-artifacts", nil, presigner)
	rawURL, err := backend.SignObjectDownloadURL(
		ctx, "pax-artifacts", "media/video.mp4", 0, time.Now().Add(15*time.Minute),
		map[string]string{
			"response-content-type":        "video/mp4",
			"response-content-disposition": "inline; filename=video.mp4",
		},
	)
	require.NoError(t, err)
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	query := parsed.Query()
	signedHeaders := strings.Split(query.Get("X-Amz-SignedHeaders"), ";")
	assert.Equal(t, []string{"host"}, signedHeaders)
	assert.Equal(t, "video/mp4", query.Get("response-content-type"))
	assert.Equal(t, "inline; filename=video.mp4", query.Get("response-content-disposition"))
	signingTime, err := time.Parse("20060102T150405Z", query.Get("X-Amz-Date"))
	require.NoError(t, err)
	verifier := awsv4.NewSigner(func(options *awsv4.SignerOptions) {
		options.DisableURIPathEscaping = true
		options.DisableHeaderHoisting = true
	})
	for _, byteRange := range []string{"bytes=0-1023", "bytes=1048576-", "bytes=-65536"} {
		t.Run(byteRange, func(t *testing.T) {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			require.NoError(t, err)
			request.Header.Set("Range", byteRange)
			assert.Equal(t, byteRange, request.Header.Get("Range"))

			// S3 reconstructs the signature from SignedHeaders, not extra request headers.
			canonical := request.Clone(ctx)
			canonical.Header = make(http.Header)
			for _, header := range signedHeaders {
				if header != "host" {
					canonical.Header[http.CanonicalHeaderKey(header)] = request.Header.Values(
						header,
					)
				}
			}
			canonicalQuery := canonical.URL.Query()
			canonicalQuery.Del("X-Amz-Signature")
			canonical.URL.RawQuery = canonicalQuery.Encode()
			expectedURL, _, err := verifier.PresignHTTP(
				ctx, creds, canonical, "UNSIGNED-PAYLOAD", "s3", "us-east-1", signingTime,
			)
			require.NoError(t, err)
			expected, err := url.Parse(expectedURL)
			require.NoError(t, err)
			assert.Equal(t, query.Get("X-Amz-Signature"), expected.Query().Get("X-Amz-Signature"))
		})
	}
}

func TestS3MediaGivenExpiredTicketWhenSigningThenRejectsBeforePresigning(t *testing.T) {
	presigner := &fakeS3Presigner{}
	backend := newS3PaxdArtifactBackendWithClients("pax-artifacts", nil, presigner)

	_, err := backend.SignObjectDownloadURL(
		context.Background(), "pax-artifacts", "media/video.mp4", 0,
		time.Now().Add(-time.Minute), nil,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "expiration must be in the future")
	assert.Nil(t, presigner.getInput)
}
