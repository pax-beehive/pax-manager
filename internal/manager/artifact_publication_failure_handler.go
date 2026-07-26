package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

const maxArtifactFailureMessageLength = 2048

var artifactFailureCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)

func (s *Service) handleFailArtifactPublication(
	c context.Context,
	ctx *app.RequestContext,
) {
	node := nodeFromContext(ctx)
	var req FailArtifactPublicationRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.ErrorCode = strings.ToLower(strings.TrimSpace(req.ErrorCode))
	req.Message = strings.TrimSpace(req.Message)
	if !artifactFailureCodePattern.MatchString(req.ErrorCode) {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "error_code is invalid",
		})
		return
	}
	if len(req.Message) > maxArtifactFailureMessageLength {
		writeEndpointError(ctx, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "message is too long",
		})
		return
	}
	publication, err := s.store.FailArtifactPublication(
		c,
		node,
		strings.TrimSpace(ctx.Param("publication_id")),
		req.ErrorCode,
		req.Message,
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if err := reconcileArtifactPublicationDisplaysForSession(
		c,
		s.store,
		publication.AgentID,
		publication.SessionID,
	); err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, ArtifactPublicationData{Publication: publication})
}
