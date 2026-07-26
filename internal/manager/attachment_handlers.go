package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const gcsResumableChunkAlignment int64 = 256 * 1024

func (s *Service) handleCreateUserAttachment(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var req CreateUserAttachmentRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req, err = normalizeCreateUserAttachmentRequest(req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	bucket, err := s.sessionArtifactBucket()
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	objectID, err := auth.NewSecret("attobj")
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	object := userAttachmentObjectName(principal.User.UserID, objectID, req.Filename)
	expiresAt := s.clock().UTC().Add(s.sessionArtifactUploadTTL())
	attachment, err := s.store.CreateUserAttachment(c, principal, req, bucket, object, expiresAt)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	url, err := s.paxdArtifacts.SignResumableUploadURL(
		c,
		attachment.Bucket,
		attachment.Object,
		attachment.ContentType,
		attachment.SHA256,
		expiresAt,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	headers := map[string]string{
		"Content-Type":     attachment.ContentType,
		"x-goog-resumable": "start",
	}
	if attachment.SHA256 != "" {
		headers["x-goog-meta-sha256"] = attachment.SHA256
	}
	writeData(ctx, http.StatusOK, UserAttachmentUploadTicket{
		Attachment: attachment,
		Upload: UserAttachmentUpload{
			Protocol:       "gcs_resumable",
			Method:         http.MethodPost,
			URL:            url,
			Headers:        headers,
			ChunkAlignment: gcsResumableChunkAlignment,
			ExpiresAt:      expiresAt,
		},
		CompleteURL: strings.ReplaceAll(strings.ReplaceAll(
			routeCompleteUserAttachment,
			":user_id",
			principal.User.UserID,
		), ":attachment_id", attachment.AttachmentID),
	})
}

func (s *Service) handleCompleteUserAttachment(c context.Context, ctx *app.RequestContext) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	attachment, err := s.store.GetUserAttachment(c, principal, ctx.Param("attachment_id"))
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if attachment.UploadStatus == domain.UserAttachmentUploadCompleted {
		writeData(ctx, http.StatusOK, CompleteUserAttachmentData{Attachment: attachment})
		return
	}
	attrs, err := s.paxdArtifacts.ObjectAttrs(c, attachment.Bucket, attachment.Object, 0)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if attachment.SizeBytes > 0 && attrs.SizeBytes != attachment.SizeBytes {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusConflict,
			Message: "uploaded attachment size does not match declared size",
		})
		return
	}
	completed, err := s.store.CompleteUserAttachment(
		c,
		principal,
		attachment.AttachmentID,
		ArtifactContent{
			ContentType: attrs.ContentType,
			SizeBytes:   attrs.SizeBytes,
			SHA256:      attachment.SHA256,
			Generation:  attrs.Generation,
		},
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, CompleteUserAttachmentData{Attachment: completed})
}

func normalizeCreateUserAttachmentRequest(
	req CreateUserAttachmentRequest,
) (CreateUserAttachmentRequest, error) {
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	req.Filename = filepath.Base(strings.TrimSpace(req.Filename))
	if req.Filename == "" || req.Filename == "." || req.Filename == ".." {
		return CreateUserAttachmentRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "filename is required",
		}
	}
	req.ContentType = strings.TrimSpace(req.ContentType)
	if req.ContentType == "" {
		req.ContentType = "application/octet-stream"
	}
	if req.SizeBytes < 0 {
		return CreateUserAttachmentRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "size_bytes must not be negative",
		}
	}
	req.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
	if req.SHA256 != "" && !paxdSHA256Pattern.MatchString(req.SHA256) {
		return CreateUserAttachmentRequest{}, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "sha256 must be a lowercase hex digest",
		}
	}
	return req, nil
}

func userAttachmentObjectName(userID string, objectID string, filename string) string {
	cleanName := filepath.Base(strings.TrimSpace(filename))
	if cleanName == "" || cleanName == "." {
		cleanName = "attachment.bin"
	}
	return "user-attachments/" + userID + "/" + objectID + "/" + cleanName
}
