package manager

import (
	"context"
	"mime"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *Service) handleUserAttachmentContent(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "private, no-store")
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
	if attachment.UploadStatus != domain.UserAttachmentUploadCompleted {
		writeError(ctx, http.StatusConflict, "attachment upload is not completed")
		return
	}
	bucket, err := s.sessionArtifactBucket()
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if attachment.Bucket != bucket {
		writeError(ctx, http.StatusForbidden, "attachment bucket is not allowed")
		return
	}
	disposition := "attachment"
	switch attachment.ContentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp":
		disposition = "inline"
	}
	url, err := s.paxdArtifacts.SignObjectDownloadURL(
		c, attachment.Bucket, attachment.Object, attachment.Generation,
		s.clock().UTC().Add(s.paxdArtifactDownloadTTL()),
		map[string]string{
			"response-content-type": attachment.ContentType,
			"response-content-disposition": mime.FormatMediaType(disposition,
				map[string]string{"filename": attachment.Filename}),
		},
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	ctx.Redirect(http.StatusFound, []byte(url))
}
