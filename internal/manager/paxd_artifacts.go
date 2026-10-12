package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

const (
	defaultPaxdArtifactDownloadTTL = 15 * time.Minute
	paxdArtifactProduct            = "paxd"
	paxlArtifactProduct            = "paxl"
)

var (
	paxdProductPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	paxdPlatformPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)
	paxdTagPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]*$`)
	paxdSHA256Pattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type paxdArtifactBackend interface {
	SignDownloadURL(ctx context.Context, artifact PaxdArtifact, expiresAt time.Time) (string, error)
	SignObjectDownloadURL(
		ctx context.Context,
		bucket string,
		object string,
		generation int64,
		expiresAt time.Time,
		query map[string]string,
	) (string, error)
	SignUploadURL(
		ctx context.Context,
		bucket string,
		object string,
		contentType string,
		sha256 string,
		expiresAt time.Time,
	) (string, error)
	ObjectAttrs(
		ctx context.Context,
		bucket string,
		object string,
		generation int64,
	) (paxdArtifactObjectAttrs, error)
}

type paxdArtifactObjectAttrs struct {
	Generation  int64
	SizeBytes   int64
	ContentType string
	SHA256      string
}

func (s *Service) handleDownloadPaxdArtifact(c context.Context, ctx *app.RequestContext) {
	s.handleDownloadArtifact(c, ctx, paxdArtifactProduct)
}

func (s *Service) handleDownloadPaxlArtifact(c context.Context, ctx *app.RequestContext) {
	s.handleDownloadArtifact(c, ctx, paxlArtifactProduct)
}

func (s *Service) handleDownloadPaxdInstaller(c context.Context, ctx *app.RequestContext) {
	s.handleDownloadInstaller(c, ctx, paxdArtifactProduct)
}

func (s *Service) handleDownloadPaxlInstaller(c context.Context, ctx *app.RequestContext) {
	s.handleDownloadInstaller(c, ctx, paxlArtifactProduct)
}

func (s *Service) handleDownloadGenericArtifact(c context.Context, ctx *app.RequestContext) {
	s.handleDownloadArtifact(c, ctx, "")
}

func (s *Service) handleDownloadArtifact(
	c context.Context,
	ctx *app.RequestContext,
	routeProduct string,
) {
	req, err := paxdArtifactDownloadRequest(ctx, routeProduct)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	currentStatus := s.currentBinaryQuality(c, ctx, req)
	artifact, err := s.store.FindPaxdArtifact(c, req)
	if err != nil {
		if currentStatus == "disabled" && errors.Is(err, ErrNotFound) {
			writeError(
				ctx,
				http.StatusGone,
				"Current binary has known issues. No replacement is available; upgrade when a verified version is published.",
			)
			return
		}
		writeEndpointError(ctx, err)
		return
	}
	artifact.Tags = normalizeBinaryQualityTags(artifact.Tags)
	if req.IncludeDisabled {
		writeData(ctx, http.StatusOK, binaryQualityMetadata(artifact))
		return
	}
	expiresAt := s.clock().UTC().Add(s.paxdArtifactDownloadTTL())
	url, err := s.paxdArtifacts.SignDownloadURL(c, artifact, expiresAt)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, PaxdArtifactDownloadResponse{
		CurrentStatus: currentStatus,
		Warning:       binaryQualityWarning(currentStatus),
		URL:           url,
		ExpiresAt:     expiresAt,
		Artifact:      artifact,
		SHA256:        artifact.SHA256,
		SizeBytes:     artifact.SizeBytes,
		Version:       artifact.Version,
		Product:       artifact.Product,
		Platform:      artifact.Platform,
		Tags:          artifact.Tags,
		Generation:    artifact.Generation,
	})
}

func (s *Service) handleDownloadInstaller(
	c context.Context,
	ctx *app.RequestContext,
	routeProduct string,
) {
	artifact, err := s.store.FindPaxdArtifact(c, FindPaxdArtifactRequest{
		Product:  routeProduct,
		Platform: "script",
		Tags:     []string{"installer", "stable"},
	})
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	expiresAt := s.clock().UTC().Add(s.paxdArtifactDownloadTTL())
	url, err := s.paxdArtifacts.SignDownloadURL(c, artifact, expiresAt)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	ctx.Redirect(http.StatusFound, []byte(url))
}

func (s *Service) handlePublishPaxdArtifact(c context.Context, ctx *app.RequestContext) {
	s.handlePublishArtifact(c, ctx, paxdArtifactProduct)
}

func (s *Service) handlePublishGenericArtifact(c context.Context, ctx *app.RequestContext) {
	s.handlePublishArtifact(c, ctx, "")
}

func (s *Service) handlePublishArtifact(
	c context.Context,
	ctx *app.RequestContext,
	routeProduct string,
) {
	principal, err := s.authenticateArtifactPublisher(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}

	var req CreatePaxdArtifactRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req, err = normalizeCreatePaxdArtifactRequest(req, routeProduct)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if s.cfg.ObjectStorageBucket == "" {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusInternalServerError,
			Message: "object storage bucket is not configured",
		})
		return
	}
	if req.Bucket != s.cfg.ObjectStorageBucket {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "bucket must match the configured object storage bucket",
		})
		return
	}
	attrs, err := s.paxdArtifacts.ObjectAttrs(c, req.Bucket, req.Object, 0)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if req.Generation == 0 {
		req.Generation = attrs.Generation
	} else if attrs.Generation != 0 && attrs.Generation != req.Generation {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusConflict,
			Message: "object storage generation mismatch",
		})
		return
	}
	if req.SizeBytes == 0 {
		req.SizeBytes = attrs.SizeBytes
	} else if req.SizeBytes != attrs.SizeBytes {
		writeEndpointError(ctx, artifactIntegrityConflict("uploaded object size does not match"))
		return
	}
	if req.ContentType == "" {
		req.ContentType = attrs.ContentType
	} else if req.ContentType != attrs.ContentType {
		writeEndpointError(ctx, artifactIntegrityConflict("uploaded object content type does not match"))
		return
	}
	if attrs.SHA256 == "" {
		writeEndpointError(
			ctx,
			artifactIntegrityConflict("uploaded object sha256 metadata is missing"),
		)
		return
	}
	if !strings.EqualFold(attrs.SHA256, req.SHA256) {
		writeEndpointError(ctx, artifactIntegrityConflict("uploaded object sha256 does not match"))
		return
	}

	artifact, err := s.store.CreatePaxdArtifact(c, req, principal.User.Email)
	writeEndpointResult(ctx, http.StatusOK, map[string]any{"artifact": artifact}, err)
}

func paxdArtifactDownloadRequest(
	ctx *app.RequestContext,
	routeProduct string,
) (FindPaxdArtifactRequest, error) {
	product, err := normalizeArtifactProduct(
		routeProduct,
		string(ctx.QueryArgs().Peek("product")),
	)
	if err != nil {
		return FindPaxdArtifactRequest{}, err
	}
	platform := normalizePaxdPlatform(string(ctx.QueryArgs().Peek("platform")))
	if platform == "" {
		return FindPaxdArtifactRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "platform is required",
		}
	}
	tags, err := normalizePaxdTags(queryList(ctx, "tags"))
	if err != nil {
		return FindPaxdArtifactRequest{}, err
	}
	version := strings.TrimSpace(string(ctx.QueryArgs().Peek("version")))
	metadata := string(ctx.QueryArgs().Peek("metadata")) == "1"
	if metadata && version == "" {
		return FindPaxdArtifactRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "metadata requires an exact version",
		}
	}
	return FindPaxdArtifactRequest{
		Product:         product,
		Platform:        platform,
		Tags:            tags,
		Version:         version,
		IncludeDisabled: metadata,
	}, nil
}

func (s *Service) authenticateArtifactPublisher(
	c context.Context,
	ctx *app.RequestContext,
) (UserPrincipal, error) {
	token := bearerToken(string(ctx.Request.Header.Peek("Authorization")))
	if token == "" {
		return UserPrincipal{}, apperr.Error{
			Status:  http.StatusUnauthorized,
			Message: "missing bearer user API key",
		}
	}
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		return UserPrincipal{}, err
	}
	if !principal.IsAdmin {
		return UserPrincipal{}, apperr.Error{
			Status:  http.StatusForbidden,
			Message: "an admin user API key is required",
		}
	}
	return principal, nil
}

func normalizeCreatePaxdArtifactRequest(
	req CreatePaxdArtifactRequest,
	routeProduct string,
) (CreatePaxdArtifactRequest, error) {
	product, err := normalizeArtifactProduct(routeProduct, req.Product)
	if err != nil {
		return CreatePaxdArtifactRequest{}, err
	}
	req.Product = product
	req.Platform = normalizePaxdPlatform(req.Platform)
	if req.Platform == "" {
		return CreatePaxdArtifactRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "platform is required",
		}
	}
	tags, err := normalizePaxdTags(req.Tags)
	if err != nil {
		return CreatePaxdArtifactRequest{}, err
	}
	req.Tags = normalizeBinaryQualityTags(tags)
	req.Bucket = strings.TrimSpace(req.Bucket)
	req.Object = strings.TrimSpace(req.Object)
	req.Version = strings.TrimSpace(req.Version)
	req.BuildID = strings.TrimSpace(req.BuildID)
	req.ContentType = strings.TrimSpace(req.ContentType)
	req.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
	if req.Bucket == "" || req.Object == "" {
		return CreatePaxdArtifactRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "bucket and object are required",
		}
	}
	if req.Version == "" {
		return CreatePaxdArtifactRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "version is required",
		}
	}
	if !paxdSHA256Pattern.MatchString(req.SHA256) {
		return CreatePaxdArtifactRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "sha256 must be a 64 character lowercase hex string",
		}
	}
	if req.Generation < 0 || req.SizeBytes < 0 {
		return CreatePaxdArtifactRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "generation and size_bytes must be non-negative",
		}
	}
	return req, nil
}

func normalizeArtifactProduct(routeProduct string, requestProduct string) (string, error) {
	product := strings.ToLower(strings.TrimSpace(routeProduct))
	if product == "" {
		product = strings.ToLower(strings.TrimSpace(requestProduct))
	}
	if product == "" {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "product is required",
		}
	}
	if !paxdProductPattern.MatchString(product) {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "product contains an invalid value",
		}
	}
	if product != paxdArtifactProduct && product != paxlArtifactProduct {
		return "", apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "product is not supported",
		}
	}
	return product, nil
}

func normalizePaxdPlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if !paxdPlatformPattern.MatchString(platform) {
		return ""
	}
	return platform
}

func normalizePaxdTags(raw []string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range raw {
		for _, tag := range strings.Split(value, ",") {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if tag == "" {
				continue
			}
			if !paxdTagPattern.MatchString(tag) {
				return nil, apperr.Error{
					Status:  http.StatusBadRequest,
					Message: "tags contain an invalid value",
				}
			}
			seen[tag] = true
		}
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags, nil
}

func queryList(ctx *app.RequestContext, key string) []string {
	values := []string{}
	ctx.QueryArgs().VisitAll(func(k []byte, v []byte) {
		if string(k) == key {
			values = append(values, string(v))
		}
	})
	return values
}

func bearerToken(header string) string {
	authType, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(authType, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func (s *Service) paxdArtifactDownloadTTL() time.Duration {
	if s.cfg.PaxdArtifactDownloadTTL <= 0 {
		return defaultPaxdArtifactDownloadTTL
	}
	return s.cfg.PaxdArtifactDownloadTTL
}
