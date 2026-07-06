package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Service) handleUserAgents(c context.Context, ctx *app.RequestContext) {
	status, data, err := s.userapi.ListAgents(c, requestMetadata(ctx))
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleGetAgent(c context.Context, ctx *app.RequestContext, agentID string) {
	status, data, err := s.userapi.GetAgent(c, requestMetadata(ctx), agentID)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleDeleteAgent(c context.Context, ctx *app.RequestContext, agentID string) {
	status, data, err := s.userapi.DeleteAgent(c, requestMetadata(ctx), agentID)
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

func (s *Service) handleGetAgentSession(
	c context.Context,
	ctx *app.RequestContext,
	agentID string,
	sessionID string,
) {
	status, data, err := s.userapi.GetAgentSession(
		c,
		requestMetadata(ctx),
		agentID,
		sessionID,
	)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleListAgentMessages(
	c context.Context,
	ctx *app.RequestContext,
	agentID string,
	sessionID string,
	statusFilter string,
	limit int,
) {
	status, data, err := s.userapi.ListMailbox(
		c,
		requestMetadata(ctx),
		agentID,
		sessionID,
		statusFilter,
		limit,
	)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleListAgentSessionMessages(
	c context.Context,
	ctx *app.RequestContext,
	agentID string,
	sessionID string,
) {
	status, data, err := s.userapi.ListAgentSessionMessages(
		c,
		requestMetadata(ctx),
		agentID,
		sessionID,
	)
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

func (s *Service) handleSessionMessage(
	c context.Context,
	ctx *app.RequestContext,
	req CreateMailboxRequest,
) {
	status, data, err := s.userapi.CreateSessionMessage(c, requestMetadata(ctx), req)
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
