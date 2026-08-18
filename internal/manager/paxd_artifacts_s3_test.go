package manager

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3ArtifactBackend(t *testing.T) {
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	validSHA256 := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	validChecksum := "ASNFZ4mrze8BI0VniavN7wEjRWeJq83vASNFZ4mrze8="

	t.Run(
		"Given an object when signing upload then content type metadata and write-once precondition are signed",
		func(t *testing.T) {
			presigner := &fakeS3Presigner{}
			backend := newS3PaxdArtifactBackendWithClients("pax-artifacts", nil, presigner)

			got, err := backend.SignUploadURL(
				context.Background(),
				"pax-artifacts",
				"releases/paxd",
				"application/octet-stream",
				"abc123",
				expiresAt,
			)

			require.NoError(t, err)
			assert.Equal(t, "https://objects.example/upload", got)
			require.NotNil(t, presigner.putInput)
			assert.Equal(t, "pax-artifacts", aws.ToString(presigner.putInput.Bucket))
			assert.Equal(t, "releases/paxd", aws.ToString(presigner.putInput.Key))
			assert.Equal(
				t,
				"application/octet-stream",
				aws.ToString(presigner.putInput.ContentType),
			)
			assert.Equal(t, "abc123", presigner.putInput.Metadata["sha256"])
			assert.Nil(t, presigner.putInput.ChecksumSHA256)
			assert.Equal(t, "*", aws.ToString(presigner.putInput.IfNoneMatch))
			assert.InDelta(t, 10*time.Minute, presigner.expires, float64(5*time.Second))
		},
	)

	t.Run(
		"Given static credentials when presigning a real upload then the ticket headers are complete and no empty-body checksum is required",
		func(t *testing.T) {
			awsConfig := aws.Config{
				Region: "us-east-1",
				Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
					"access-key", "secret-key", "",
				)),
				RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			}
			presigner := newObjectStorageS3Presigner(newObjectStorageS3Client(
				awsConfig,
				"https://objects.example.com",
				true,
			))
			backend := newS3PaxdArtifactBackendWithClients("pax-artifacts", nil, presigner)

			rawURL, err := backend.SignUploadURL(
				context.Background(),
				"pax-artifacts",
				"releases/paxd",
				"application/octet-stream",
				validSHA256,
				expiresAt,
			)

			require.NoError(t, err)
			parsed, err := url.Parse(rawURL)
			require.NoError(t, err)
			assert.Equal(t, "objects.example.com", parsed.Host)
			signedHeaders := strings.Split(parsed.Query().Get("X-Amz-SignedHeaders"), ";")
			assert.ElementsMatch(
				t,
				[]string{
					"host",
					"if-none-match",
					"x-amz-checksum-sha256",
					"x-amz-meta-sha256",
				},
				signedHeaders,
			)
			assert.NotContains(t, strings.ToLower(rawURL), "crc32")
			assert.Equal(t, map[string]string{
				"Content-Type":          "application/octet-stream",
				"If-None-Match":         "*",
				"x-amz-checksum-sha256": validChecksum,
				"x-amz-meta-sha256":     validSHA256,
			}, objectUploadHeaders("application/octet-stream", validSHA256))
		},
	)

	t.Run(
		"Given SHA256 text when converting to an S3 checksum then only valid hex is encoded",
		func(t *testing.T) {
			assert.Equal(t, validChecksum, objectSHA256Checksum(validSHA256))
			assert.Equal(t, validChecksum, objectSHA256Checksum(strings.ToUpper(validSHA256)))
			assert.Empty(t, objectSHA256Checksum(""))
			assert.Empty(t, objectSHA256Checksum(strings.Repeat("z", 64)))
			assert.Empty(t, objectSHA256Checksum("abc123"))
		},
	)

	t.Run(
		"Given supported response overrides when signing download then they are part of the signature input",
		func(t *testing.T) {
			presigner := &fakeS3Presigner{}
			backend := newS3PaxdArtifactBackendWithClients("pax-artifacts", nil, presigner)

			got, err := backend.SignObjectDownloadURL(
				context.Background(),
				"pax-artifacts",
				"attachments/report.txt",
				0,
				expiresAt,
				map[string]string{
					"response-content-type":        "text/plain",
					"response-content-disposition": `attachment; filename="report.txt"`,
				},
			)

			require.NoError(t, err)
			assert.Equal(t, "https://objects.example/download", got)
			require.NotNil(t, presigner.getInput)
			assert.Equal(t, "text/plain", aws.ToString(presigner.getInput.ResponseContentType))
			assert.Equal(
				t,
				`attachment; filename="report.txt"`,
				aws.ToString(presigner.getInput.ResponseContentDisposition),
			)
			assert.Nil(
				t,
				presigner.getInput.VersionId,
				"synthetic generation must not affect signing",
			)
		},
	)

	t.Run(
		"Given an unknown response query when signing download then it is rejected",
		func(t *testing.T) {
			backend := newS3PaxdArtifactBackendWithClients(
				"pax-artifacts",
				nil,
				&fakeS3Presigner{},
			)

			_, err := backend.SignObjectDownloadURL(
				context.Background(),
				"pax-artifacts",
				"artifact",
				0,
				expiresAt,
				map[string]string{"unsigned": "unsafe"},
			)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported object download query")
		},
	)

	t.Run(
		"Given an artifact when signing its download then the object signer is used",
		func(t *testing.T) {
			presigner := &fakeS3Presigner{}
			backend := newS3PaxdArtifactBackendWithClients("pax-artifacts", nil, presigner)

			got, err := backend.SignDownloadURL(context.Background(), PaxdArtifact{
				Bucket:     "pax-artifacts",
				Object:     "releases/paxd",
				Generation: 0,
			}, expiresAt)

			require.NoError(t, err)
			assert.Equal(t, "https://objects.example/download", got)
			assert.Equal(t, "releases/paxd", aws.ToString(presigner.getInput.Key))
		},
	)

	t.Run(
		"Given a requested synthetic generation when signing download then the current object must match",
		func(t *testing.T) {
			modifiedAt := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
			output := &s3.HeadObjectOutput{
				ContentLength: aws.Int64(12),
				ContentType:   aws.String("text/plain"),
				ETag:          aws.String(`"etag"`),
				LastModified:  &modifiedAt,
				VersionId:     aws.String("version-1"),
			}
			head := &fakeS3HeadClient{output: output}
			presigner := &fakeS3Presigner{}
			backend := newS3PaxdArtifactBackendWithClients("pax-artifacts", head, presigner)
			generation := stableS3ObjectGeneration("pax-artifacts", "artifact", output)

			got, err := backend.SignObjectDownloadURL(
				context.Background(),
				"pax-artifacts",
				"artifact",
				generation,
				expiresAt,
				nil,
			)

			require.NoError(t, err)
			assert.Equal(t, "https://objects.example/download", got)
			require.NotNil(t, head.input)
			assert.Equal(t, "artifact", aws.ToString(head.input.Key))
			require.NotNil(t, presigner.getInput)
			assert.Equal(
				t,
				"version-1",
				aws.ToString(presigner.getInput.VersionId),
				"a generation-checked URL must remain pinned after HEAD",
			)
			presigner.getInput = nil

			_, err = backend.SignObjectDownloadURL(
				context.Background(),
				"pax-artifacts",
				"artifact",
				generation+1,
				expiresAt,
				nil,
			)

			require.Error(t, err)
			assert.ErrorContains(t, err, "generation mismatch")
			assert.Nil(t, presigner.getInput, "a stale object must not receive a download URL")
		},
	)

	t.Run(
		"Given an unversioned object when signing a matching generation then signing remains compatible",
		func(t *testing.T) {
			modifiedAt := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
			output := &s3.HeadObjectOutput{
				ContentLength: aws.Int64(12),
				ETag:          aws.String(`"etag"`),
				LastModified:  &modifiedAt,
			}
			presigner := &fakeS3Presigner{}
			backend := newS3PaxdArtifactBackendWithClients(
				"pax-artifacts",
				&fakeS3HeadClient{output: output},
				presigner,
			)

			_, err := backend.SignObjectDownloadURL(
				context.Background(),
				"pax-artifacts",
				"artifact",
				stableS3ObjectGeneration("pax-artifacts", "artifact", output),
				expiresAt,
				nil,
			)

			require.NoError(t, err)
			require.NotNil(t, presigner.getInput)
			assert.Nil(t, presigner.getInput.VersionId)
		},
	)

	t.Run(
		"Given head metadata when reading attributes then it returns a stable positive generation",
		func(t *testing.T) {
			modifiedAt := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
			head := &fakeS3HeadClient{output: &s3.HeadObjectOutput{
				ContentLength: aws.Int64(12),
				ContentType:   aws.String("text/plain"),
				ETag:          aws.String(`"etag"`),
				LastModified:  &modifiedAt,
				Metadata:      map[string]string{"SHA256": "abc123"},
				VersionId:     aws.String("version-1"),
			}}
			backend := newS3PaxdArtifactBackendWithClients("pax-artifacts", head, nil)

			first, err := backend.ObjectAttrs(
				context.Background(), "pax-artifacts", "attachments/report.txt", 0,
			)
			require.NoError(t, err)
			second, err := backend.ObjectAttrs(
				context.Background(), "pax-artifacts", "attachments/report.txt", first.Generation,
			)
			require.NoError(t, err)

			assert.Positive(t, first.Generation)
			assert.Equal(t, first.Generation, second.Generation)
			assert.Equal(t, int64(12), first.SizeBytes)
			assert.Equal(t, "text/plain", first.ContentType)
			assert.Equal(t, "abc123", first.SHA256)
			assert.Nil(
				t,
				head.input.VersionId,
				"synthetic generation must not select an S3 version",
			)
		},
	)

	t.Run(
		"Given object integrity attributes when fingerprinting then normalized metadata is stable and content changes are detected",
		func(t *testing.T) {
			modifiedAt := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
			base := &s3.HeadObjectOutput{
				ContentLength:  aws.Int64(12),
				ETag:           aws.String(`"etag"`),
				LastModified:   &modifiedAt,
				Metadata:       map[string]string{"SHA256": strings.ToUpper(validSHA256)},
				ChecksumSHA256: aws.String(validChecksum),
			}
			normalized := *base
			normalized.Metadata = map[string]string{"sha256": validSHA256}
			changedMetadata := normalized
			changedMetadata.Metadata = map[string]string{"sha256": strings.Repeat("f", 64)}
			changedChecksum := normalized
			changedChecksum.ChecksumSHA256 = aws.String(
				"//////////////////////////////////////////8=",
			)

			generation := stableS3ObjectGeneration("pax-artifacts", "artifact", base)

			assert.Equal(
				t,
				generation,
				stableS3ObjectGeneration("pax-artifacts", "artifact", &normalized),
			)
			assert.NotEqual(
				t,
				generation,
				stableS3ObjectGeneration("pax-artifacts", "artifact", &changedMetadata),
			)
			assert.NotEqual(
				t,
				generation,
				stableS3ObjectGeneration("pax-artifacts", "artifact", &changedChecksum),
			)
		},
	)

	t.Run(
		"Given separate internal and public endpoints when operating then head is internal and presigning is public",
		func(t *testing.T) {
			var headHost string
			awsConfig := aws.Config{
				Region: "us-east-1",
				Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
					"access-key", "secret-key", "",
				)),
				HTTPClient: &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						headHost = req.URL.Host
						return &http.Response{
							StatusCode: http.StatusOK,
							Header: http.Header{
								"Content-Length":    []string{"12"},
								"Content-Type":      []string{"text/plain"},
								"ETag":              []string{`"etag"`},
								"X-Amz-Meta-Sha256": []string{"abc123"},
							},
							Body: http.NoBody,
						}, nil
					}),
				},
				RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			}
			backend := newS3PaxdArtifactBackend(Config{
				ObjectStorageBucket:         "pax-artifacts",
				ObjectStorageRegion:         "us-east-1",
				ObjectStorageEndpoint:       "http://minio.internal:9000",
				ObjectStoragePublicEndpoint: "https://objects.example.com",
				ObjectStorageForcePathStyle: true,
			}).(*s3PaxdArtifactBackend)
			backend.loadAWSConfigFn = func(context.Context, string) (aws.Config, error) {
				return awsConfig, nil
			}

			attrs, err := backend.ObjectAttrs(
				context.Background(), "pax-artifacts", "attachments/report.txt", 0,
			)
			require.NoError(t, err)
			rawURL, err := backend.SignUploadURL(
				context.Background(),
				"pax-artifacts",
				"attachments/report.txt",
				"text/plain",
				"abc123",
				expiresAt,
			)
			require.NoError(t, err)
			parsed, err := url.Parse(rawURL)
			require.NoError(t, err)

			assert.Equal(t, "minio.internal:9000", headHost)
			assert.Equal(t, "objects.example.com", parsed.Host)
			assert.Equal(t, int64(12), attrs.SizeBytes)
			assert.Equal(t, "abc123", attrs.SHA256)
		},
	)

	t.Run(
		"Given another bucket when operating on an object then it is rejected",
		func(t *testing.T) {
			backend := newS3PaxdArtifactBackendWithClients(
				"pax-artifacts",
				&fakeS3HeadClient{},
				&fakeS3Presigner{},
			)

			_, err := backend.ObjectAttrs(context.Background(), "other", "artifact", 0)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "configured object storage bucket")
		},
	)

	t.Run(
		"Given invalid storage inputs when operating then they are rejected before SDK calls",
		func(t *testing.T) {
			unconfigured := newS3PaxdArtifactBackendWithClients("", nil, &fakeS3Presigner{})
			configured := newS3PaxdArtifactBackendWithClients(
				"pax-artifacts",
				nil,
				&fakeS3Presigner{},
			)

			_, bucketErr := unconfigured.SignUploadURL(
				context.Background(), "pax-artifacts", "artifact", "text/plain", "", expiresAt,
			)
			_, objectErr := configured.SignUploadURL(
				context.Background(), "pax-artifacts", "", "text/plain", "", expiresAt,
			)
			_, expirationErr := configured.SignUploadURL(
				context.Background(),
				"pax-artifacts",
				"artifact",
				"text/plain",
				"",
				time.Now().Add(-time.Minute),
			)

			assert.ErrorContains(t, bucketErr, "not configured")
			assert.ErrorContains(t, objectErr, "object is required")
			assert.ErrorContains(t, expirationErr, "must be in the future")
		},
	)

	t.Run("Given empty SDK responses when operating then they are rejected", func(t *testing.T) {
		backend := newS3PaxdArtifactBackendWithClients(
			"pax-artifacts",
			&fakeS3HeadClient{},
			&fakeS3Presigner{nilResponse: true},
		)

		_, headErr := backend.ObjectAttrs(context.Background(), "pax-artifacts", "artifact", 0)
		_, putErr := backend.SignUploadURL(
			context.Background(), "pax-artifacts", "artifact", "text/plain", "", expiresAt,
		)
		_, getErr := backend.SignObjectDownloadURL(
			context.Background(), "pax-artifacts", "artifact", 0, expiresAt, nil,
		)

		assert.ErrorContains(t, headErr, "empty response")
		assert.ErrorContains(t, putErr, "empty URL")
		assert.ErrorContains(t, getErr, "empty URL")
	})

	t.Run(
		"Given standard AWS environment credentials when creating the production backend then it can presign",
		func(t *testing.T) {
			t.Setenv("AWS_ACCESS_KEY_ID", "access-key")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-key")
			t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
			backend := newS3PaxdArtifactBackend(Config{
				ObjectStorageBucket:         "pax-artifacts",
				ObjectStorageRegion:         "us-east-1",
				ObjectStoragePublicEndpoint: "https://objects.example.com",
				ObjectStorageForcePathStyle: true,
			})

			rawURL, err := backend.SignUploadURL(
				context.Background(), "pax-artifacts", "artifact", "text/plain", "", expiresAt,
			)

			require.NoError(t, err)
			assert.Contains(t, rawURL, "objects.example.com")
		},
	)

	t.Run("Given client failures when using storage then they are returned", func(t *testing.T) {
		wantErr := errors.New("storage unavailable")
		backend := newS3PaxdArtifactBackendWithClients(
			"pax-artifacts",
			&fakeS3HeadClient{err: wantErr},
			&fakeS3Presigner{err: wantErr},
		)

		_, headErr := backend.ObjectAttrs(context.Background(), "pax-artifacts", "artifact", 0)
		_, putErr := backend.SignUploadURL(
			context.Background(), "pax-artifacts", "artifact", "text/plain", "sha", expiresAt,
		)
		_, getErr := backend.SignObjectDownloadURL(
			context.Background(), "pax-artifacts", "artifact", 0, expiresAt, nil,
		)

		assert.ErrorIs(t, headErr, wantErr)
		assert.ErrorIs(t, putErr, wantErr)
		assert.ErrorIs(t, getErr, wantErr)
	})
}

func TestObjectStorageOpenAPIGivenUploadProtocolWhenGeneratedThenItDocumentsAuthAndRecovery(
	t *testing.T,
) {
	raw, err := openAPIDocument("https://manager.example")

	require.NoError(t, err)
	document := string(raw)
	assert.Contains(t, document, `"adminUserAPIKey"`)
	assert.Contains(t, document, `"bearerFormat": "pax user API key"`)
	assert.Contains(t, document, "HTTP 412")
	assert.Contains(t, document, "completion")
}

type fakeS3HeadClient struct {
	input  *s3.HeadObjectInput
	output *s3.HeadObjectOutput
	err    error
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (c *fakeS3HeadClient) HeadObject(
	_ context.Context,
	input *s3.HeadObjectInput,
	_ ...func(*s3.Options),
) (*s3.HeadObjectOutput, error) {
	c.input = input
	return c.output, c.err
}

type fakeS3Presigner struct {
	getInput    *s3.GetObjectInput
	putInput    *s3.PutObjectInput
	expires     time.Duration
	err         error
	nilResponse bool
}

func (c *fakeS3Presigner) PresignGetObject(
	_ context.Context,
	input *s3.GetObjectInput,
	optFns ...func(*s3.PresignOptions),
) (*awsv4.PresignedHTTPRequest, error) {
	c.getInput = input
	c.captureOptions(optFns)
	if c.nilResponse {
		return nil, c.err
	}
	return &awsv4.PresignedHTTPRequest{
		URL:    "https://objects.example/download",
		Method: http.MethodGet,
	}, c.err
}

func (c *fakeS3Presigner) PresignPutObject(
	_ context.Context,
	input *s3.PutObjectInput,
	optFns ...func(*s3.PresignOptions),
) (*awsv4.PresignedHTTPRequest, error) {
	c.putInput = input
	c.captureOptions(optFns)
	if c.nilResponse {
		return nil, c.err
	}
	return &awsv4.PresignedHTTPRequest{
		URL:    "https://objects.example/upload",
		Method: http.MethodPut,
	}, c.err
}

func (c *fakeS3Presigner) captureOptions(optFns []func(*s3.PresignOptions)) {
	options := s3.PresignOptions{}
	for _, fn := range optFns {
		fn(&options)
	}
	c.expires = options.Expires
}
