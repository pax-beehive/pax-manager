package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	artifactPrepareStatusUploadRequired = "upload_required"
	artifactPrepareStatusAvailable      = "available"
)

func (s *Service) handlePrepareArtifactPublication(
	c context.Context,
	ctx *app.RequestContext,
) {
	node := nodeFromContext(ctx)
	publicationID := strings.TrimSpace(ctx.Param("publication_id"))
	var req PrepareArtifactPublicationRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req, err := normalizePrepareArtifactPublicationRequest(req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
	publication, err := s.store.GetArtifactPublication(c, principal, publicationID)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if publication.NodeID != node.NodeID {
		writeEndpointError(ctx, domain.ErrNotFound)
		return
	}
	if publication.Filename != req.Filename {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusConflict,
			Message: "filename does not match registered publication",
		})
		return
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
	object := sessionArtifactObjectName(node.OwnerUserID, objectID, req.Filename)
	expiresAt := s.clock().UTC().Add(s.sessionArtifactUploadTTL())
	publication, upload, err := s.store.PrepareArtifactPublication(
		c,
		node,
		publicationID,
		req,
		bucket,
		object,
		expiresAt,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if err := reconcileArtifactPublicationDisplaysForSession(
		c, s.store, publication.AgentID, publication.SessionID,
	); err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if upload.Status == domain.ArtifactUploadStatusCompleted {
		writeData(ctx, http.StatusOK, PrepareArtifactPublicationData{
			Status:     artifactPrepareStatusAvailable,
			ArtifactID: upload.ArtifactID,
		})
		return
	}
	url, err := s.paxdArtifacts.SignUploadURL(
		c,
		upload.Bucket,
		upload.Object,
		upload.ContentType,
		upload.SHA256,
		upload.ExpiresAt,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, PrepareArtifactPublicationData{
		Status:     artifactPrepareStatusUploadRequired,
		ArtifactID: upload.ArtifactID,
		Upload: &NodeArtifactUploadTicket{
			UploadID:  upload.UploadID,
			Protocol:  "s3_presigned_put",
			Method:    http.MethodPut,
			URL:       url,
			Headers:   objectUploadHeaders(upload.ContentType, upload.SHA256),
			ExpiresAt: upload.ExpiresAt,
		},
	})
}

func (s *Service) handleCompleteNodeArtifactUpload(
	c context.Context,
	ctx *app.RequestContext,
) {
	node := nodeFromContext(ctx)
	upload, err := s.store.GetNodeArtifactUpload(
		c,
		node,
		strings.TrimSpace(ctx.Param("upload_id")),
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if upload.Status == domain.ArtifactUploadStatusCompleted {
		completedUpload, artifact, err := s.store.CompleteNodeArtifactUpload(
			c,
			node,
			upload.UploadID,
			ArtifactContent{},
		)
		if err == nil {
			err = reconcileArtifactPublicationDisplaysForSession(
				c, s.store, completedUpload.AgentID, completedUpload.SessionID,
			)
		}
		writeNodeArtifactCompletion(ctx, artifact, err)
		return
	}
	attrs, err := s.paxdArtifacts.ObjectAttrs(c, upload.Bucket, upload.Object, 0)
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
	completedUpload, artifact, err := s.store.CompleteNodeArtifactUpload(
		c,
		node,
		upload.UploadID,
		ArtifactContent{
			ContentType: attrs.ContentType,
			SizeBytes:   attrs.SizeBytes,
			SHA256:      attrs.SHA256,
			Generation:  attrs.Generation,
		},
	)
	if err == nil {
		err = reconcileArtifactPublicationDisplaysForSession(
			c, s.store, completedUpload.AgentID, completedUpload.SessionID,
		)
	}
	writeNodeArtifactCompletion(ctx, artifact, err)
}

func writeNodeArtifactCompletion(
	ctx *app.RequestContext,
	artifact SessionArtifact,
	err error,
) {
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, CompleteNodeArtifactUploadData{
		Status:     artifactPrepareStatusAvailable,
		ArtifactID: artifact.ArtifactID,
	})
}

func normalizePrepareArtifactPublicationRequest(
	req PrepareArtifactPublicationRequest,
) (PrepareArtifactPublicationRequest, error) {
	req.Filename = strings.TrimSpace(req.Filename)
	req.ContentType = strings.TrimSpace(req.ContentType)
	req.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
	if req.Filename == "" || req.Filename == "." || req.Filename == ".." {
		return PrepareArtifactPublicationRequest{}, invalidArtifactPublication(
			"filename must be a basename",
		)
	}
	if req.ContentType == "" {
		req.ContentType = "application/octet-stream"
	}
	if req.SizeBytes < 0 {
		return PrepareArtifactPublicationRequest{}, invalidArtifactPublication(
			"size_bytes must be non-negative",
		)
	}
	if err := validateSinglePutObjectSize(req.SizeBytes); err != nil {
		return PrepareArtifactPublicationRequest{}, err
	}
	if !paxdSHA256Pattern.MatchString(req.SHA256) {
		return PrepareArtifactPublicationRequest{}, invalidArtifactPublication(
			"sha256 must be a lowercase hex sha256 digest",
		)
	}
	return req, nil
}

func artifactIntegrityConflict(message string) error {
	return apperr.Error{Status: http.StatusConflict, Message: message}
}
