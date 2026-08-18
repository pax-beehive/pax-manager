package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	defaultSessionArtifactUploadTTL = 15 * time.Minute
	sessionArtifactContentRefMain   = "main"
	maxSinglePutObjectSizeBytes     = int64(5 * 1024 * 1024 * 1024)
)

var artifactKindPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func (s *Service) handleCreateArtifactUpload(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var req CreateArtifactUploadRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req, err = normalizeCreateArtifactUploadRequest(req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if req.SessionID != "" {
		if _, err := s.store.GetSession(c, principal, req.SessionID); err != nil {
			writeEndpointError(ctx, err)
			return
		}
	}
	bucket, err := s.sessionArtifactBucket()
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	objectID, err := auth.NewSecret("artobj")
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	object := sessionArtifactObjectName(principal.User.UserID, objectID, req.Filename)
	expiresAt := s.clock().UTC().Add(s.sessionArtifactUploadTTL())
	upload, err := s.store.CreateArtifactUpload(c, principal, req, bucket, object, expiresAt)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	url, err := s.paxdArtifacts.SignUploadURL(
		c,
		upload.Bucket,
		upload.Object,
		upload.ContentType,
		upload.SHA256,
		expiresAt,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	headers := objectUploadHeaders(upload.ContentType, upload.SHA256)
	writeData(ctx, http.StatusOK, ArtifactUploadTicket{
		UploadID:   upload.UploadID,
		Protocol:   "s3_presigned_put",
		Method:     http.MethodPut,
		URL:        url,
		Bucket:     upload.Bucket,
		Object:     upload.Object,
		ExpiresAt:  expiresAt,
		Headers:    headers,
		Upload:     upload,
		ContentRef: sessionArtifactContentRefMain,
		CompleteURL: strings.ReplaceAll(strings.ReplaceAll(
			routeCompleteArtifactUpload,
			":user_id",
			principal.User.UserID,
		),
			":upload_id", upload.UploadID,
		),
	})
}

func (s *Service) handleCompleteArtifactUpload(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	uploadID := ctx.Param("upload_id")
	upload, err := s.store.GetArtifactUpload(c, principal, uploadID)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var req CompleteArtifactUploadRequest
	if len(ctx.Request.Body()) > 0 {
		if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
			writeError(ctx, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	req = normalizeCompleteArtifactUploadRequest(upload, req)
	if !artifactKindPattern.MatchString(req.Kind) {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "kind is invalid",
		})
		return
	}
	if req.SessionID != "" {
		if _, err := s.store.GetSession(c, principal, req.SessionID); err != nil {
			writeEndpointError(ctx, err)
			return
		}
	}
	attrs, err := s.paxdArtifacts.ObjectAttrs(c, upload.Bucket, upload.Object, upload.Generation)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	attrs = normalizeUploadedObjectAttrs(attrs, upload.ContentType)
	if err := validateUploadedObject(
		attrs,
		upload.SizeBytes,
		upload.ContentType,
		upload.SHA256,
	); err != nil {
		writeEndpointError(ctx, err)
		return
	}
	completed, artifact, err := s.store.CompleteArtifactUpload(
		c,
		principal,
		uploadID,
		ArtifactContent{
			Filename:    upload.Filename,
			ContentType: attrs.ContentType,
			SizeBytes:   attrs.SizeBytes,
			SHA256:      attrs.SHA256,
			Generation:  attrs.Generation,
		},
		req,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, CompleteArtifactUploadData{
		Upload:   completed,
		Artifact: artifact,
	})
}

func (s *Service) handleCreateSessionArtifact(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var req CreateSessionArtifactRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req, err = normalizeCreateSessionArtifactRequest(req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if req.SessionID != "" {
		if _, err := s.store.GetSession(c, principal, req.SessionID); err != nil {
			writeEndpointError(ctx, err)
			return
		}
	}
	artifact, err := s.store.CreateSessionArtifact(c, principal, req)
	writeEndpointResult(ctx, http.StatusOK, map[string]any{"artifact": artifact}, err)
}

func (s *Service) handleGetSessionArtifact(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	artifact, err := s.store.GetSessionArtifact(c, principal, ctx.Param("artifact_id"))
	writeEndpointResult(ctx, http.StatusOK, map[string]any{"artifact": artifact}, err)
}

func (s *Service) handleListSessionArtifacts(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	sessionID := ctx.Param("session_id")
	if _, err := s.store.GetSession(c, principal, sessionID); err != nil {
		writeEndpointError(ctx, err)
		return
	}
	artifacts, err := s.store.ListSessionArtifacts(c, ListSessionArtifactsFilter{
		Principal: principal,
		SessionID: sessionID,
		Kind:      strings.TrimSpace(string(ctx.QueryArgs().Peek("kind"))),
		Status:    strings.TrimSpace(string(ctx.QueryArgs().Peek("status"))),
		Limit:     queryInt(ctx, "limit"),
	})
	writeEndpointResult(ctx, http.StatusOK, map[string]any{"artifacts": artifacts}, err)
}

func (s *Service) handleGetArtifactContent(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	artifact, content, err := s.store.GetArtifactContent(
		c,
		principal,
		ctx.Param("artifact_id"),
		ctx.Param("content_ref"),
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if content.Bucket == "" || content.Object == "" {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "artifact content is not backed by object storage",
		})
		return
	}
	allowedBucket, err := s.sessionArtifactBucket()
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if content.Bucket != allowedBucket {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusForbidden,
			Message: "artifact content bucket is not allowed",
		})
		return
	}
	expiresAt := s.clock().UTC().Add(s.paxdArtifactDownloadTTL())
	query := map[string]string{}
	if content.ContentType != "" {
		query["response-content-type"] = content.ContentType
	}
	if content.Filename != "" {
		disposition := "inline"
		if string(ctx.QueryArgs().Peek("disposition")) == "attachment" {
			disposition = "attachment"
		}
		query["response-content-disposition"] = disposition + `; filename="` +
			strings.ReplaceAll(content.Filename, `"`, ``) + `"`
	}
	url, err := s.paxdArtifacts.SignObjectDownloadURL(
		c,
		content.Bucket,
		content.Object,
		content.Generation,
		expiresAt,
		query,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if redirectQueryEnabled(ctx) {
		ctx.Redirect(http.StatusFound, []byte(url))
		return
	}
	writeData(ctx, http.StatusOK, ArtifactContentURLResponse{
		URL:       url,
		ExpiresAt: expiresAt,
		Artifact:  artifact,
		Content:   content,
	})
}

func (s *Service) handleAttachSessionArtifact(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var req AttachArtifactRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.ArtifactID = ctx.Param("artifact_id")
	artifact, err := s.store.AttachSessionArtifact(c, principal, req)
	writeEndpointResult(ctx, http.StatusOK, map[string]any{"artifact": artifact}, err)
}

func normalizeCreateArtifactUploadRequest(
	req CreateArtifactUploadRequest,
) (CreateArtifactUploadRequest, error) {
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.Kind = normalizeArtifactKind(req.Kind)
	req.Title = strings.TrimSpace(req.Title)
	req.Summary = strings.TrimSpace(req.Summary)
	req.Filename = strings.TrimSpace(req.Filename)
	req.ContentType = strings.TrimSpace(req.ContentType)
	req.SHA256 = strings.TrimSpace(strings.ToLower(req.SHA256))
	if req.Filename == "" {
		return CreateArtifactUploadRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "filename is required",
		}
	}
	if req.Kind == "" {
		req.Kind = "file"
	}
	if req.ContentType == "" {
		req.ContentType = "application/octet-stream"
	}
	if req.SizeBytes < 0 {
		return CreateArtifactUploadRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "size_bytes must be non-negative",
		}
	}
	if err := validateSinglePutObjectSize(req.SizeBytes); err != nil {
		return CreateArtifactUploadRequest{}, err
	}
	if req.SHA256 != "" && !paxdSHA256Pattern.MatchString(req.SHA256) {
		return CreateArtifactUploadRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "sha256 must be a lowercase hex sha256 digest",
		}
	}
	if !artifactKindPattern.MatchString(req.Kind) {
		return CreateArtifactUploadRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "kind is invalid",
		}
	}
	return req, nil
}

func normalizeCreateSessionArtifactRequest(
	req CreateSessionArtifactRequest,
) (CreateSessionArtifactRequest, error) {
	req.Kind = normalizeArtifactKind(req.Kind)
	if req.Kind == "" || !artifactKindPattern.MatchString(req.Kind) {
		return CreateSessionArtifactRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "kind is invalid",
		}
	}
	if req.SchemaVersion <= 0 {
		req.SchemaVersion = 1
	}
	req.Status = strings.TrimSpace(req.Status)
	if req.Status == "" {
		req.Status = domain.SessionArtifactStatusAvailable
	}
	for _, content := range req.Contents {
		if content.Bucket != "" || content.Object != "" ||
			content.StorageURI != "" || content.Generation != 0 {
			return CreateSessionArtifactRequest{}, apperr.Error{
				Status:  http.StatusBadRequest,
				Message: "object-storage-backed artifact contents must be created via artifact uploads",
			}
		}
	}
	return req, nil
}

func normalizeCompleteArtifactUploadRequest(
	upload ArtifactUpload,
	req CompleteArtifactUploadRequest,
) CompleteArtifactUploadRequest {
	req.Kind = firstNonEmpty(normalizeArtifactKind(req.Kind), upload.Kind, "file")
	if req.SchemaVersion <= 0 {
		req.SchemaVersion = 1
	}
	req.Title = firstNonEmpty(strings.TrimSpace(req.Title), upload.Title, upload.Filename)
	req.Summary = firstNonEmpty(strings.TrimSpace(req.Summary), upload.Summary)
	req.Status = firstNonEmpty(strings.TrimSpace(req.Status), domain.SessionArtifactStatusAvailable)
	req.SessionID = firstNonEmpty(strings.TrimSpace(req.SessionID), upload.SessionID)
	req.MessageID = strings.TrimSpace(req.MessageID)
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	return req
}

func normalizeArtifactKind(kind string) string {
	return strings.TrimSpace(strings.ToLower(kind))
}

func (s *Service) sessionArtifactBucket() (string, error) {
	if s.cfg.ObjectStorageBucket != "" {
		return s.cfg.ObjectStorageBucket, nil
	}
	return "", apperr.Error{
		Status:  http.StatusInternalServerError,
		Message: "object storage bucket is not configured",
	}
}

func objectUploadHeaders(contentType string, objectSHA256 string) map[string]string {
	headers := map[string]string{
		"Content-Type":  contentType,
		"If-None-Match": "*",
	}
	if objectSHA256 != "" {
		headers["x-amz-meta-sha256"] = objectSHA256
	}
	if checksum := objectSHA256Checksum(objectSHA256); checksum != "" {
		headers["x-amz-checksum-sha256"] = checksum
	}
	return headers
}

func validateUploadedObject(
	attrs paxdArtifactObjectAttrs,
	expectedSize int64,
	expectedContentType string,
	expectedSHA256 string,
) error {
	if attrs.Generation <= 0 {
		return artifactIntegrityConflict("uploaded object generation is missing")
	}
	if attrs.SizeBytes != expectedSize {
		return artifactIntegrityConflict("uploaded object size does not match")
	}
	if expectedContentType != "" && attrs.ContentType != expectedContentType {
		return artifactIntegrityConflict("uploaded object content type does not match")
	}
	if expectedSHA256 != "" {
		if attrs.SHA256 == "" {
			return artifactIntegrityConflict("uploaded object sha256 metadata is missing")
		}
		if !strings.EqualFold(attrs.SHA256, expectedSHA256) {
			return artifactIntegrityConflict("uploaded object sha256 does not match")
		}
	}
	return nil
}

func normalizeUploadedObjectAttrs(
	attrs paxdArtifactObjectAttrs,
	expectedContentType string,
) paxdArtifactObjectAttrs {
	if attrs.ContentType == "" ||
		(attrs.ContentType == "application/octet-stream" &&
			expectedContentType != "" &&
			expectedContentType != "application/octet-stream") {
		attrs.ContentType = expectedContentType
	}
	return attrs
}

func validateSinglePutObjectSize(sizeBytes int64) error {
	if sizeBytes > maxSinglePutObjectSizeBytes {
		return apperr.Error{
			Status: http.StatusBadRequest,
			Message: "size_bytes exceeds the 5 GiB single PUT limit; " +
				"multipart upload is not supported",
		}
	}
	return nil
}

func (s *Service) sessionArtifactUploadTTL() time.Duration {
	if s.cfg.SessionArtifactUploadTTL <= 0 {
		return defaultSessionArtifactUploadTTL
	}
	return s.cfg.SessionArtifactUploadTTL
}

func sessionArtifactObjectName(userID string, objectID string, filename string) string {
	cleanName := filepath.Base(filename)
	cleanName = strings.ReplaceAll(cleanName, " ", "_")
	if cleanName == "." || cleanName == "/" || cleanName == "" {
		cleanName = "artifact.bin"
	}
	return "session-artifacts/" + userID + "/" + objectID + "/" + cleanName
}

func redirectQueryEnabled(ctx *app.RequestContext) bool {
	value := strings.ToLower(string(ctx.QueryArgs().Peek("redirect")))
	return value == "1" || value == "true" || value == "yes"
}
