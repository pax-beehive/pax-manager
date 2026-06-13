package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	hzapi "github.com/pax-beehive/pax-manager/internal/transport/http/model/paxmanager/api"
)

func Health(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).Health(c, ctx, &hzapi.EmptyRequest{})
}

func RegisterAgent(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).RegisterAgent(c, ctx, nil)
}

func ReportAgentStatus(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ReportAgentStatus(c, ctx, nil)
}

func PullMailbox(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).PullMailbox(c, ctx, nil)
}

func UpdateMailboxOffset(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).UpdateMailboxOffset(c, ctx, nil)
}

func ReportMessageResult(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ReportMessageResult(c, ctx, nil)
}

func ListAgents(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListAgents(c, ctx, &hzapi.EmptyRequest{})
}

func ListAgentSessions(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListAgentSessions(c, ctx, nil)
}

func GetSession(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).GetSession(c, ctx, nil)
}

func ListSessionMessages(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListSessionMessages(c, ctx, nil)
}

func CreateMailboxMessage(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).CreateMailboxMessage(c, ctx, nil)
}

func ListMailbox(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListMailbox(c, ctx, nil)
}

func CreateAgentRegistrationToken(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).CreateAgentRegistrationToken(c, ctx, nil)
}

func ListUserAPIKeys(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListUserAPIKeys(c, ctx, &hzapi.EmptyRequest{})
}

func CreateUserAPIKey(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).CreateUserAPIKey(c, ctx, nil)
}

func RevokeUserAPIKey(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).RevokeUserAPIKey(c, ctx, nil)
}

func (s *Service) Health(c context.Context, ctx *app.RequestContext, _ *hzapi.EmptyRequest) {
	s.handleHealth(c, ctx)
}

func (s *Service) RegisterAgent(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.RegisterAgentRequest,
) {
	s.handleAgentRegister(c, ctx, registerAgentRequest(req))
}

func (s *Service) ReportAgentStatus(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.AgentStatusReportRequest,
) {
	s.handleAgentStatus(c, ctx, agentFromContext(ctx), agentStatusReport(req))
}

func (s *Service) PullMailbox(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.PullMailboxRequest,
) {
	s.handleAgentMailbox(c, ctx, agentFromContext(ctx), req.GetOffset(), int(req.GetLimit()))
}

func (s *Service) UpdateMailboxOffset(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.UpdateMailboxOffsetRequest,
) {
	s.handleAgentOffset(c, ctx, agentFromContext(ctx), req.GetOffset())
}

func (s *Service) ReportMessageResult(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.ReportMessageResultRequest,
) {
	result := messageResultRequest(req)
	if result.MessageID == "" {
		result.MessageID = ctx.Param("messageId")
	}
	s.handleAgentMessageResult(c, ctx, agentFromContext(ctx), result)
}

func (s *Service) ListAgents(c context.Context, ctx *app.RequestContext, _ *hzapi.EmptyRequest) {
	s.handleUserAgents(c, ctx)
}

func (s *Service) ListAgentSessions(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.ListAgentSessionsRequest,
) {
	agentID := req.GetAgentID()
	if agentID == "" {
		agentID = ctx.Param("agentId")
	}
	s.handleListAgentSessions(c, ctx, agentID)
}

func (s *Service) GetSession(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.GetSessionRequest,
) {
	sessionID := req.GetSessionID()
	if sessionID == "" {
		sessionID = ctx.Param("sessionId")
	}
	s.handleGetSession(c, ctx, sessionID)
}

func (s *Service) ListSessionMessages(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.ListSessionMessagesRequest,
) {
	sessionID := req.GetSessionID()
	if sessionID == "" {
		sessionID = ctx.Param("sessionId")
	}
	s.handleListSessionMessages(c, ctx, sessionID)
}

func (s *Service) CreateMailboxMessage(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.CreateMailboxRequest,
) {
	s.handleUserMessage(c, ctx, createMailboxRequest(req))
}

func (s *Service) ListMailbox(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.ListMailboxRequest,
) {
	s.handleUserMailbox(c, ctx, req)
}

func (s *Service) CreateAgentRegistrationToken(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.CreateRegistrationTokenRequest,
) {
	s.handleCreateRegistrationToken(c, ctx, createRegistrationTokenRequest(req))
}

func (s *Service) ListUserAPIKeys(
	c context.Context,
	ctx *app.RequestContext,
	_ *hzapi.EmptyRequest,
) {
	s.handleListUserAPIKeys(c, ctx)
}

func (s *Service) CreateUserAPIKey(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.CreateUserAPIKeyRequest,
) {
	s.handleCreateUserAPIKey(c, ctx, CreateUserAPIKeyRequest{Name: req.GetName()})
}

func (s *Service) RevokeUserAPIKey(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.RevokeUserAPIKeyRequest,
) {
	keyID := req.GetKeyID()
	if keyID == "" {
		keyID = ctx.Param("keyId")
	}
	s.handleRevokeUserAPIKey(c, ctx, keyID)
}
