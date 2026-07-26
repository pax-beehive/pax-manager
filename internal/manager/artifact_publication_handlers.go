package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	maxArtifactPublicationIDLength = 128
	maxArtifactFilenameLength      = 255
	maxArtifactTitleLength         = 1024
)

func (s *Service) handlePutArtifactPublication(
	c context.Context,
	ctx *app.RequestContext,
) {
	node := nodeFromContext(ctx)
	publicationID := strings.TrimSpace(ctx.Param("publication_id"))
	var req RegisterArtifactPublicationRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req, err := normalizeRegisterArtifactPublicationRequest(publicationID, req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
	agent, err := s.store.GetNodeAgent(c, node.NodeID, req.Source.AgentID)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if agent.OwnerUserID != node.OwnerUserID {
		writeEndpointError(ctx, domain.ErrNotFound)
		return
	}
	session, err := s.store.GetSession(c, principal, req.Source.SessionID)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if session.AgentID != req.Source.AgentID ||
		(session.NodeID != "" && session.NodeID != node.NodeID) {
		writeEndpointError(ctx, domain.ErrNotFound)
		return
	}
	publication, err := s.store.PutArtifactPublication(c, ArtifactPublication{
		PublicationID: publicationID,
		OwnerUserID:   node.OwnerUserID,
		NodeID:        node.NodeID,
		AgentID:       req.Source.AgentID,
		SessionID:     session.SessionID,
		Filename:      req.Filename,
		Title:         req.Title,
		Status:        domain.ArtifactPublicationStatusQueued,
	})
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

func (s *Service) handleGetArtifactPublication(
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
	writeData(ctx, http.StatusOK, ArtifactPublicationData{Publication: publication})
}

func normalizeRegisterArtifactPublicationRequest(
	publicationID string,
	req RegisterArtifactPublicationRequest,
) (RegisterArtifactPublicationRequest, error) {
	req.Source.AgentID = strings.TrimSpace(req.Source.AgentID)
	req.Source.SessionID = strings.TrimSpace(req.Source.SessionID)
	req.Filename = strings.TrimSpace(req.Filename)
	req.Title = strings.TrimSpace(req.Title)
	switch {
	case publicationID == "" || len(publicationID) > maxArtifactPublicationIDLength:
		return RegisterArtifactPublicationRequest{}, invalidArtifactPublication(
			"publication_id is invalid",
		)
	case req.Source.AgentID == "" || req.Source.SessionID == "":
		return RegisterArtifactPublicationRequest{}, invalidArtifactPublication(
			"source.agent_id and source.session_id are required",
		)
	case req.Filename == "" ||
		req.Filename == "." ||
		req.Filename == ".." ||
		len(req.Filename) > maxArtifactFilenameLength ||
		filepath.Base(req.Filename) != req.Filename ||
		strings.Contains(req.Filename, `\`):
		return RegisterArtifactPublicationRequest{}, invalidArtifactPublication(
			"filename must be a basename",
		)
	case len(req.Title) > maxArtifactTitleLength:
		return RegisterArtifactPublicationRequest{}, invalidArtifactPublication(
			"title is too long",
		)
	default:
		return req, nil
	}
}

func invalidArtifactPublication(message string) error {
	return apperr.Error{Status: http.StatusBadRequest, Message: message}
}
