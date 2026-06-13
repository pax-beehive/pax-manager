package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	hzapi "github.com/pax-beehive/pax-manager/internal/transport/http/model/paxmanager/api"
)

func (s *Service) handleUserAgents(c context.Context, ctx *app.RequestContext) {
	status, data, err := s.userapi.ListAgents(c, requestMetadata(ctx))
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleListAgentSessions(
	c context.Context,
	ctx *app.RequestContext,
	agentID string,
) {
	status, data, err := s.userapi.ListAgentSessions(c, requestMetadata(ctx), agentID)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleGetSession(c context.Context, ctx *app.RequestContext, sessionID string) {
	status, data, err := s.userapi.GetSession(c, requestMetadata(ctx), sessionID)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleListSessionMessages(
	c context.Context,
	ctx *app.RequestContext,
	sessionID string,
) {
	status, data, err := s.userapi.ListSessionMessages(c, requestMetadata(ctx), sessionID)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleUserMessage(
	c context.Context,
	ctx *app.RequestContext,
	req CreateMailboxRequest,
) {
	status, data, err := s.userapi.CreateMailboxMessage(c, requestMetadata(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleUserMailbox(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.ListMailboxRequest,
) {
	status, data, err := s.userapi.ListMailbox(
		c,
		requestMetadata(ctx),
		req.GetAgentID(),
		req.GetSessionID(),
		req.GetStatus(),
		int(req.GetLimit()),
	)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleCreateRegistrationToken(
	c context.Context,
	ctx *app.RequestContext,
	req CreateRegistrationTokenRequest,
) {
	status, data, err := s.userapi.CreateRegistrationToken(c, requestMetadata(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleCreateUserAPIKey(
	c context.Context,
	ctx *app.RequestContext,
	req CreateUserAPIKeyRequest,
) {
	status, data, err := s.userapi.CreateUserAPIKey(c, requestMetadata(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleListUserAPIKeys(c context.Context, ctx *app.RequestContext) {
	status, data, err := s.userapi.ListUserAPIKeys(c, requestMetadata(ctx))
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleRevokeUserAPIKey(c context.Context, ctx *app.RequestContext, keyID string) {
	status, data, err := s.userapi.RevokeUserAPIKey(c, requestMetadata(ctx), keyID)
	writeEndpointResult(ctx, status, data, err)
}
