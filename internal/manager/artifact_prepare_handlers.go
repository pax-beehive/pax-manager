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
	_, upload, err := s.store.PrepareArtifactPublication(
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
	if upload.Status == domain.ArtifactUploadStatusCompleted {
		writeData(ctx, http.StatusOK, PrepareArtifactPublicationData{
			Status:     artifactPrepareStatusAvailable,
			ArtifactID: upload.ArtifactID,
		})
		return
	}
	url, err := s.paxdArtifacts.SignResumableUploadURL(
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
			UploadID: upload.UploadID,
			Protocol: "gcs_resumable",
			Method:   http.MethodPost,
			URL:      url,
			Headers: map[string]string{
				"Content-Type":       upload.ContentType,
				"x-goog-resumable":   "start",
				"x-goog-meta-sha256": upload.SHA256,
			},
			ChunkAlignment: gcsResumableChunkAlignment,
			ExpiresAt:      upload.ExpiresAt,
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
		_, artifact, err := s.store.CompleteNodeArtifactUpload(
			c,
			node,
			upload.UploadID,
			ArtifactContent{},
		)
		writeNodeArtifactCompletion(ctx, artifact, err)
		return
	}
	attrs, err := s.paxdArtifacts.ObjectAttrs(c, upload.Bucket, upload.Object, 0)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if attrs.Generation <= 0 {
		writeEndpointError(
			ctx,
			artifactIntegrityConflict("uploaded artifact generation is missing"),
		)
		return
	}
	if attrs.SizeBytes != upload.SizeBytes {
		writeEndpointError(ctx, artifactIntegrityConflict("uploaded artifact size does not match"))
		return
	}
	if attrs.SHA256 == "" {
		writeEndpointError(
			ctx,
			artifactIntegrityConflict("uploaded artifact sha256 metadata is missing"),
		)
		return
	}
	if !strings.EqualFold(attrs.SHA256, upload.SHA256) {
		writeEndpointError(
			ctx,
			artifactIntegrityConflict("uploaded artifact sha256 does not match"),
		)
		return
	}
	if attrs.ContentType == "" || attrs.ContentType == "application/octet-stream" {
		attrs.ContentType = upload.ContentType
	}
	_, artifact, err := s.store.CompleteNodeArtifactUpload(
		c,
		node,
		upload.UploadID,
		ArtifactContent{
			ContentType: attrs.ContentType,
			SizeBytes:   attrs.SizeBytes,
			SHA256:      upload.SHA256,
			Generation:  attrs.Generation,
		},
	)
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
