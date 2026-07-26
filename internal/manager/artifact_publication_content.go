package manager

import (
	"context"
	"mime"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	artifactPublicationContentNotAvailable = "not_available"
	artifactPublicationContentFailed       = "failed"
	artifactPublicationContentAvailable    = "available"
)

func (s *Service) handleGetArtifactPublicationContent(
	c context.Context,
	ctx *app.RequestContext,
) {
	principal, err := s.userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	publication, err := s.store.GetArtifactPublication(
		c,
		principal,
		strings.TrimSpace(ctx.Param("publication_id")),
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	switch publication.Status {
	case domain.ArtifactPublicationStatusFailed:
		writeData(ctx, http.StatusConflict, ArtifactPublicationContentData{
			Status:      artifactPublicationContentFailed,
			Retryable:   false,
			Publication: publication,
		})
		return
	case domain.ArtifactPublicationStatusAvailable:
	default:
		ctx.Header("Retry-After", "2")
		writeData(ctx, http.StatusAccepted, ArtifactPublicationContentData{
			Status:      artifactPublicationContentNotAvailable,
			Retryable:   true,
			Publication: publication,
		})
		return
	}
	if publication.ArtifactID == "" {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusConflict,
			Message: "available publication has no artifact",
		})
		return
	}
	artifact, content, err := s.store.GetArtifactContent(
		c,
		principal,
		publication.ArtifactID,
		strings.TrimSpace(ctx.Param("content_ref")),
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if content.Bucket == "" || content.Object == "" || content.Generation <= 0 {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusConflict,
			Message: "artifact content is not finalized",
		})
		return
	}
	previewKind, inlineSafe := artifactPreviewPolicy(content.ContentType)
	disposition := requestedArtifactDisposition(ctx)
	if disposition == "inline" && !inlineSafe {
		disposition = "attachment"
	}
	query := artifactContentResponseQuery(content, disposition)
	expiresAt := s.clock().UTC().Add(s.paxdArtifactDownloadTTL())
	signedURL, err := s.paxdArtifacts.SignObjectDownloadURL(
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
		ctx.Redirect(http.StatusFound, []byte(signedURL))
		return
	}
	writeData(ctx, http.StatusOK, ArtifactPublicationContentData{
		Status:      artifactPublicationContentAvailable,
		Retryable:   false,
		Publication: publication,
		Artifact:    &artifact,
		Content:     &content,
		URL:         signedURL,
		ExpiresAt:   &expiresAt,
		PreviewKind: previewKind,
		Disposition: disposition,
	})
}

func requestedArtifactDisposition(ctx *app.RequestContext) string {
	if strings.EqualFold(
		strings.TrimSpace(string(ctx.QueryArgs().Peek("disposition"))),
		"attachment",
	) {
		return "attachment"
	}
	return "inline"
}

func artifactPreviewPolicy(contentType string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	}
	mediaType = strings.ToLower(mediaType)
	switch {
	case strings.HasPrefix(mediaType, "image/") && mediaType != "image/svg+xml":
		return "image", true
	case mediaType == "application/pdf":
		return "pdf", true
	case mediaType == "text/plain":
		return "text", true
	case mediaType == "text/markdown" || mediaType == "text/x-markdown":
		return "markdown", true
	case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		return "json", true
	default:
		return "download", false
	}
}

func artifactContentResponseQuery(
	content ArtifactContent,
	disposition string,
) map[string]string {
	query := map[string]string{}
	if content.ContentType != "" {
		query["response-content-type"] = content.ContentType
	}
	params := map[string]string{}
	if content.Filename != "" {
		params["filename"] = content.Filename
	}
	query["response-content-disposition"] = mime.FormatMediaType(disposition, params)
	return query
}
