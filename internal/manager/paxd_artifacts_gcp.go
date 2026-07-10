package manager

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/idtoken"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

type gcpPaxdArtifactBackend struct {
	signingServiceAccount string
}

func newGCPPaxdArtifactBackend(cfg Config) paxdArtifactBackend {
	if cfg.PaxdArtifactGCSMock {
		return mockPaxdArtifactBackend{}
	}
	return &gcpPaxdArtifactBackend{
		signingServiceAccount: cfg.PaxdArtifactSigningServiceAccount,
	}
}

func (b *gcpPaxdArtifactBackend) SignDownloadURL(
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

func (b *gcpPaxdArtifactBackend) SignObjectDownloadURL(
	ctx context.Context,
	bucket string,
	object string,
	generation int64,
	expiresAt time.Time,
	extraQuery map[string]string,
) (string, error) {
	if b.signingServiceAccount == "" {
		return "", apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "paxd artifact signing service account is not configured",
		}
	}
	query := url.Values{}
	if generation > 0 {
		query.Set("generation", strconv.FormatInt(generation, 10))
	}
	for key, value := range extraQuery {
		if key != "" && value != "" {
			query.Set(key, value)
		}
	}
	return storage.SignedURL(bucket, object, &storage.SignedURLOptions{
		Scheme:          storage.SigningSchemeV4,
		Method:          http.MethodGet,
		Expires:         expiresAt,
		GoogleAccessID:  b.signingServiceAccount,
		QueryParameters: query,
		SignBytes: func(payload []byte) ([]byte, error) {
			svc, err := iamcredentials.NewService(ctx)
			if err != nil {
				return nil, err
			}
			resp, err := svc.Projects.ServiceAccounts.SignBlob(
				"projects/-/serviceAccounts/"+b.signingServiceAccount,
				&iamcredentials.SignBlobRequest{
					Payload: base64.StdEncoding.EncodeToString(payload),
				},
			).Context(ctx).Do()
			if err != nil {
				return nil, err
			}
			return base64.StdEncoding.DecodeString(resp.SignedBlob)
		},
	})
}

func (b *gcpPaxdArtifactBackend) SignUploadURL(
	ctx context.Context,
	bucket string,
	object string,
	contentType string,
	expiresAt time.Time,
) (string, error) {
	if b.signingServiceAccount == "" {
		return "", apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "paxd artifact signing service account is not configured",
		}
	}
	return storage.SignedURL(bucket, object, &storage.SignedURLOptions{
		Scheme:         storage.SigningSchemeV4,
		Method:         http.MethodPut,
		Expires:        expiresAt,
		GoogleAccessID: b.signingServiceAccount,
		ContentType:    contentType,
		SignBytes: func(payload []byte) ([]byte, error) {
			svc, err := iamcredentials.NewService(ctx)
			if err != nil {
				return nil, err
			}
			resp, err := svc.Projects.ServiceAccounts.SignBlob(
				"projects/-/serviceAccounts/"+b.signingServiceAccount,
				&iamcredentials.SignBlobRequest{
					Payload: base64.StdEncoding.EncodeToString(payload),
				},
			).Context(ctx).Do()
			if err != nil {
				return nil, err
			}
			return base64.StdEncoding.DecodeString(resp.SignedBlob)
		},
	})
}

func (b *gcpPaxdArtifactBackend) VerifyUploader(
	ctx context.Context,
	token string,
	audience string,
) (string, error) {
	if audience == "" {
		return "", apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "paxd artifact upload audience is not configured",
		}
	}
	payload, err := idtoken.Validate(ctx, token, audience)
	if err != nil {
		return "", apperr.Error{Status: http.StatusUnauthorized, Message: "invalid bearer token"}
	}
	if email, ok := payload.Claims["email"].(string); ok && email != "" {
		return email, nil
	}
	if subject := payload.Subject; subject != "" {
		return subject, nil
	}
	return "", apperr.Error{Status: http.StatusUnauthorized, Message: "token missing principal"}
}

func (b *gcpPaxdArtifactBackend) ObjectAttrs(
	ctx context.Context,
	bucket string,
	object string,
	generation int64,
) (paxdArtifactObjectAttrs, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return paxdArtifactObjectAttrs{}, err
	}
	defer func() { _ = client.Close() }()
	obj := client.Bucket(bucket).Object(object)
	if generation > 0 {
		obj = obj.Generation(generation)
	}
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return paxdArtifactObjectAttrs{}, err
	}
	return paxdArtifactObjectAttrs{
		Generation:  attrs.Generation,
		SizeBytes:   attrs.Size,
		ContentType: attrs.ContentType,
	}, nil
}
