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
		expiresAt,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, ArtifactUploadTicket{
		UploadID:   upload.UploadID,
		Method:     http.MethodPut,
		URL:        url,
		Bucket:     upload.Bucket,
		Object:     upload.Object,
		ExpiresAt:  expiresAt,
		Headers:    map[string]string{"Content-Type": upload.ContentType},
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
	if attrs.ContentType == "" ||
		(attrs.ContentType == "application/octet-stream" &&
			upload.ContentType != "" &&
			upload.ContentType != "application/octet-stream") {
		attrs.ContentType = upload.ContentType
	}
	completed, artifact, err := s.store.CompleteArtifactUpload(
		c,
		principal,
		uploadID,
		ArtifactContent{
			Filename:    upload.Filename,
			ContentType: attrs.ContentType,
			SizeBytes:   attrs.SizeBytes,
			SHA256:      upload.SHA256,
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
			Message: "artifact content is not backed by gcs",
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
	if s.cfg.SessionArtifactGCSBucket != "" {
		return s.cfg.SessionArtifactGCSBucket, nil
	}
	if s.cfg.PaxdArtifactGCSMock {
		return "mock-session-artifacts", nil
	}
	return "", apperr.Error{
		Status:  http.StatusInternalServerError,
		Message: "session artifact gcs bucket is not configured",
	}
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
