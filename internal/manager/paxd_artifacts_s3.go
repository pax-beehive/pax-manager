package manager

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

const objectStorageSHA256MetadataKey = "sha256"

type s3HeadObjectClient interface {
	HeadObject(
		ctx context.Context,
		input *s3.HeadObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.HeadObjectOutput, error)
}

type s3Presigner interface {
	PresignGetObject(
		ctx context.Context,
		input *s3.GetObjectInput,
		optFns ...func(*s3.PresignOptions),
	) (*awsv4.PresignedHTTPRequest, error)
	PresignPutObject(
		ctx context.Context,
		input *s3.PutObjectInput,
		optFns ...func(*s3.PresignOptions),
	) (*awsv4.PresignedHTTPRequest, error)
}

type headerBoundS3Presigner struct {
	signer *awsv4.Signer
}

func (p headerBoundS3Presigner) PresignHTTP(
	ctx context.Context,
	credentials aws.Credentials,
	request *http.Request,
	payloadHash string,
	service string,
	region string,
	signingTime time.Time,
	optFns ...func(*awsv4.SignerOptions),
) (string, http.Header, error) {
	optFns = append(optFns, func(options *awsv4.SignerOptions) {
		options.DisableHeaderHoisting = true
	})
	return p.signer.PresignHTTP(
		ctx,
		credentials,
		request,
		payloadHash,
		service,
		region,
		signingTime,
		optFns...,
	)
}

type s3PaxdArtifactBackend struct {
	bucket          string
	region          string
	endpoint        string
	publicEndpoint  string
	forcePathStyle  bool
	clientsMu       sync.Mutex
	headClient      s3HeadObjectClient
	presignClient   s3Presigner
	loadAWSConfigFn func(context.Context, string) (aws.Config, error)
}

func newS3PaxdArtifactBackend(cfg Config) paxdArtifactBackend {
	return &s3PaxdArtifactBackend{
		bucket:          strings.TrimSpace(cfg.ObjectStorageBucket),
		region:          strings.TrimSpace(cfg.ObjectStorageRegion),
		endpoint:        strings.TrimSpace(cfg.ObjectStorageEndpoint),
		publicEndpoint:  strings.TrimSpace(cfg.ObjectStoragePublicEndpoint),
		forcePathStyle:  cfg.ObjectStorageForcePathStyle,
		loadAWSConfigFn: loadObjectStorageAWSConfig,
	}
}

func loadObjectStorageAWSConfig(ctx context.Context, region string) (aws.Config, error) {
	return awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithRequestChecksumCalculation(
			aws.RequestChecksumCalculationWhenRequired,
		),
		// A presigned GET is consumed as a URL only. Optional response checksum
		// validation would bind x-amz-checksum-mode as a header, which URL-only
		// consumers such as curl and browsers cannot recover from the URL.
		awsconfig.WithResponseChecksumValidation(
			aws.ResponseChecksumValidationWhenRequired,
		),
	)
}

func newS3PaxdArtifactBackendWithClients(
	bucket string,
	headClient s3HeadObjectClient,
	presignClient s3Presigner,
) *s3PaxdArtifactBackend {
	return &s3PaxdArtifactBackend{
		bucket:        bucket,
		region:        "us-east-1",
		headClient:    headClient,
		presignClient: presignClient,
	}
}

func (b *s3PaxdArtifactBackend) SignDownloadURL(
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

func (b *s3PaxdArtifactBackend) SignObjectDownloadURL(
	ctx context.Context,
	bucket string,
	object string,
	generation int64,
	expiresAt time.Time,
	extraQuery map[string]string,
) (string, error) {
	if err := b.validateObject(bucket, object); err != nil {
		return "", err
	}
	versionID := ""
	if generation > 0 {
		output, err := b.headObject(ctx, bucket, object)
		if err != nil {
			return "", err
		}
		if stableS3ObjectGeneration(bucket, object, output) != generation {
			return "", apperr.Error{
				Status:  http.StatusConflict,
				Message: "object storage generation mismatch",
			}
		}
		versionID = strings.TrimSpace(aws.ToString(output.VersionId))
	}
	duration, err := presignDuration(expiresAt)
	if err != nil {
		return "", err
	}
	input := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(object)}
	if versionID != "" {
		input.VersionId = aws.String(versionID)
	}
	for key, value := range extraQuery {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "response-content-type":
			if value != "" {
				input.ResponseContentType = aws.String(value)
			}
		case "response-content-disposition":
			if value != "" {
				input.ResponseContentDisposition = aws.String(value)
			}
		default:
			return "", apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "unsupported object download query: " + key,
			}
		}
	}
	presigner, err := b.presigner(ctx)
	if err != nil {
		return "", err
	}
	request, err := presigner.PresignGetObject(ctx, input, func(options *s3.PresignOptions) {
		options.Expires = duration
	})
	if err != nil {
		return "", fmt.Errorf("presign object download: %w", err)
	}
	if request == nil || request.URL == "" {
		return "", fmt.Errorf("presign object download: empty URL")
	}
	return request.URL, nil
}

func (b *s3PaxdArtifactBackend) SignUploadURL(
	ctx context.Context,
	bucket string,
	object string,
	contentType string,
	objectSHA256 string,
	expiresAt time.Time,
) (string, error) {
	if err := b.validateObject(bucket, object); err != nil {
		return "", err
	}
	duration, err := presignDuration(expiresAt)
	if err != nil {
		return "", err
	}
	input := &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(object),
		ContentType: aws.String(contentType),
		IfNoneMatch: aws.String("*"),
	}
	if objectSHA256 != "" {
		input.Metadata = map[string]string{objectStorageSHA256MetadataKey: objectSHA256}
	}
	if checksum := objectSHA256Checksum(objectSHA256); checksum != "" {
		input.ChecksumSHA256 = aws.String(checksum)
	}
	presigner, err := b.presigner(ctx)
	if err != nil {
		return "", err
	}
	request, err := presigner.PresignPutObject(ctx, input, func(options *s3.PresignOptions) {
		options.Expires = duration
	})
	if err != nil {
		return "", fmt.Errorf("presign object upload: %w", err)
	}
	if request == nil || request.URL == "" {
		return "", fmt.Errorf("presign object upload: empty URL")
	}
	return request.URL, nil
}

func (b *s3PaxdArtifactBackend) ObjectAttrs(
	ctx context.Context,
	bucket string,
	object string,
	_ int64,
) (paxdArtifactObjectAttrs, error) {
	output, err := b.headObject(ctx, bucket, object)
	if err != nil {
		return paxdArtifactObjectAttrs{}, err
	}
	return paxdArtifactObjectAttrs{
		Generation:  stableS3ObjectGeneration(bucket, object, output),
		SizeBytes:   aws.ToInt64(output.ContentLength),
		ContentType: aws.ToString(output.ContentType),
		SHA256:      objectMetadataValue(output.Metadata, objectStorageSHA256MetadataKey),
	}, nil
}

func (b *s3PaxdArtifactBackend) headObject(
	ctx context.Context,
	bucket string,
	object string,
) (*s3.HeadObjectOutput, error) {
	if err := b.validateObject(bucket, object); err != nil {
		return nil, err
	}
	client, err := b.headObjectClient(ctx)
	if err != nil {
		return nil, err
	}
	input := &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(object)}
	output, err := client.HeadObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("head object: %w", err)
	}
	if output == nil {
		return nil, fmt.Errorf("head object: empty response")
	}
	return output, nil
}

func (b *s3PaxdArtifactBackend) validateObject(bucket string, object string) error {
	if b.bucket == "" {
		return apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "object storage bucket is not configured",
		}
	}
	if strings.TrimSpace(bucket) != b.bucket {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "bucket must match the configured object storage bucket",
		}
	}
	if strings.TrimSpace(object) == "" {
		return apperr.Error{Status: http.StatusBadRequest, Message: "object is required"}
	}
	return nil
}

func (b *s3PaxdArtifactBackend) headObjectClient(
	ctx context.Context,
) (s3HeadObjectClient, error) {
	if b.headClient != nil {
		return b.headClient, nil
	}
	if err := b.initializeClients(ctx); err != nil {
		return nil, err
	}
	return b.headClient, nil
}

func (b *s3PaxdArtifactBackend) presigner(ctx context.Context) (s3Presigner, error) {
	if b.presignClient != nil {
		return b.presignClient, nil
	}
	if err := b.initializeClients(ctx); err != nil {
		return nil, err
	}
	return b.presignClient, nil
}

func (b *s3PaxdArtifactBackend) initializeClients(ctx context.Context) error {
	b.clientsMu.Lock()
	defer b.clientsMu.Unlock()
	if b.headClient != nil && b.presignClient != nil {
		return nil
	}
	region := b.region
	if region == "" {
		region = "us-east-1"
	}
	loadConfig := b.loadAWSConfigFn
	if loadConfig == nil {
		loadConfig = loadObjectStorageAWSConfig
	}
	awsConfig, err := loadConfig(ctx, region)
	if err != nil {
		return fmt.Errorf("load object storage AWS configuration: %w", err)
	}
	if b.headClient == nil {
		b.headClient = newObjectStorageS3Client(
			awsConfig,
			b.endpoint,
			b.forcePathStyle,
		)
	}
	if b.presignClient == nil {
		publicEndpoint := b.publicEndpoint
		if publicEndpoint == "" {
			publicEndpoint = b.endpoint
		}
		b.presignClient = newObjectStorageS3Presigner(newObjectStorageS3Client(
			awsConfig,
			publicEndpoint,
			b.forcePathStyle,
		))
	}
	return nil
}

func newObjectStorageS3Client(
	awsConfig aws.Config,
	endpoint string,
	forcePathStyle bool,
) *s3.Client {
	return s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.UsePathStyle = forcePathStyle
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	})
}

func newObjectStorageS3Presigner(client *s3.Client) *s3.PresignClient {
	return s3.NewPresignClient(client, func(options *s3.PresignOptions) {
		options.Presigner = headerBoundS3Presigner{signer: awsv4.NewSigner()}
	})
}

func presignDuration(expiresAt time.Time) (time.Duration, error) {
	duration := time.Until(expiresAt)
	if duration <= 0 {
		return 0, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "object storage signed URL expiration must be in the future",
		}
	}
	return duration, nil
}

func objectMetadataValue(metadata map[string]string, key string) string {
	for metadataKey, value := range metadata {
		if strings.EqualFold(metadataKey, key) {
			return value
		}
	}
	return ""
}

func objectSHA256Checksum(objectSHA256 string) string {
	if len(objectSHA256) != sha256.Size*2 {
		return ""
	}
	digest, err := hex.DecodeString(objectSHA256)
	if err != nil || len(digest) != sha256.Size {
		return ""
	}
	return base64.StdEncoding.EncodeToString(digest)
}

func stableS3ObjectGeneration(
	bucket string,
	object string,
	output *s3.HeadObjectOutput,
) int64 {
	lastModified := ""
	if output.LastModified != nil {
		lastModified = output.LastModified.UTC().Format(time.RFC3339Nano)
	}
	payload := strings.Join([]string{
		bucket,
		object,
		aws.ToString(output.VersionId),
		aws.ToString(output.ETag),
		lastModified,
		strconv.FormatInt(aws.ToInt64(output.ContentLength), 10),
		strings.ToLower(strings.TrimSpace(objectMetadataValue(
			output.Metadata,
			objectStorageSHA256MetadataKey,
		))),
		strings.TrimSpace(aws.ToString(output.ChecksumSHA256)),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	generation := int64(binary.BigEndian.Uint64(digest[:8]) & math.MaxInt64)
	if generation == 0 {
		return 1
	}
	return generation
}
