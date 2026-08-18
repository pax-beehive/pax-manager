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
	url, err := s.paxdArtifacts.SignUploadURL(
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
	writeData(ctx, http.StatusOK, UserAttachmentUploadTicket{
		Attachment: attachment,
		Upload: UserAttachmentUpload{
			Protocol:  "s3_presigned_put",
			Method:    http.MethodPut,
			URL:       url,
			Headers:   objectUploadHeaders(attachment.ContentType, attachment.SHA256),
			ExpiresAt: expiresAt,
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
	attrs = normalizeUploadedObjectAttrs(attrs, attachment.ContentType)
	if err := validateUploadedObject(
		attrs,
		attachment.SizeBytes,
		attachment.ContentType,
		attachment.SHA256,
	); err != nil {
		writeEndpointError(ctx, err)
		return
	}
	completed, err := s.store.CompleteUserAttachment(
		c,
		principal,
		attachment.AttachmentID,
		ArtifactContent{
			ContentType: attrs.ContentType,
			SizeBytes:   attrs.SizeBytes,
			SHA256:      attrs.SHA256,
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
	if err := validateSinglePutObjectSize(req.SizeBytes); err != nil {
		return CreateUserAttachmentRequest{}, err
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
