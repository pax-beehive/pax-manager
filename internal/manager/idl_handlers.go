package manager

import (
	"context"
	"encoding/json"
	"strings"

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

func PullSessionMailbox(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).PullSessionMailbox(c, ctx, nil)
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

func GetAgent(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).GetAgent(c, ctx, nil)
}

func ListAgentSessions(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListAgentSessions(c, ctx, nil)
}

func GetAgentSession(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).GetAgentSession(c, ctx, nil)
}

func ListAgentMessages(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListAgentMessages(c, ctx, nil)
}

func CreateAgentMessage(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).CreateAgentMessage(c, ctx, nil)
}

func ListAgentSessionMessages(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListAgentSessionMessages(c, ctx, nil)
}

func CreateSessionMessage(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).CreateSessionMessage(c, ctx, nil)
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

func (s *Service) PullSessionMailbox(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.PullSessionMailboxRequest,
) {
	sessionID := req.GetSessionID()
	if sessionID == "" {
		sessionID = sessionIDFromPathContext(ctx)
	}
	s.handleAgentSessionMailbox(
		c,
		ctx,
		agentFromContext(ctx),
		sessionID,
		req.GetOffset(),
		int(req.GetLimit()),
	)
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

func (s *Service) GetAgent(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.GetAgentRequest,
) {
	agentID := req.GetAgentID()
	if agentID == "" {
		agentID = agentIDFromUserAgentPath(ctx)
	}
	s.handleGetAgent(c, ctx, agentID)
}

func (s *Service) ListAgentSessions(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.ListAgentSessionsRequest,
) {
	agentID := req.GetAgentID()
	if agentID == "" {
		agentID = agentIDFromUserAgentPath(ctx)
	}
	s.handleListAgentSessions(c, ctx, agentID)
}

func (s *Service) GetAgentSession(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.GetAgentSessionRequest,
) {
	agentID := req.GetAgentID()
	if agentID == "" {
		agentID = agentIDFromUserAgentPath(ctx)
	}
	sessionID := req.GetSessionID()
	if sessionID == "" {
		sessionID = sessionIDFromPathContext(ctx)
	}
	s.handleGetAgentSession(c, ctx, agentID, sessionID)
}

func (s *Service) ListAgentMessages(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.ListAgentMessagesRequest,
) {
	agentID := req.GetAgentID()
	if agentID == "" {
		agentID = agentIDFromUserAgentPath(ctx)
	}
	s.handleListAgentMessages(
		c,
		ctx,
		agentID,
		req.GetSessionID(),
		req.GetStatus(),
		int(req.GetLimit()),
	)
}

func (s *Service) CreateAgentMessage(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.CreateAgentMessageRequest,
) {
	create := createAgentMessageRequest(req)
	mergeCreateMailboxBody(ctx, &create)
	if create.AgentID == "" {
		create.AgentID = agentIDFromUserAgentPath(ctx)
	}
	s.handleUserMessage(c, ctx, create)
}

func (s *Service) ListAgentSessionMessages(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.ListAgentSessionMessagesRequest,
) {
	agentID := req.GetAgentID()
	if agentID == "" {
		agentID = agentIDFromUserAgentPath(ctx)
	}
	sessionID := req.GetSessionID()
	if sessionID == "" {
		sessionID = sessionIDFromPathContext(ctx)
	}
	s.handleListAgentSessionMessages(c, ctx, agentID, sessionID)
}

func (s *Service) CreateSessionMessage(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.CreateSessionMessageRequest,
) {
	create := createSessionMessageRequest(req)
	mergeCreateMailboxBody(ctx, &create)
	if create.AgentID == "" {
		create.AgentID = agentIDFromUserAgentPath(ctx)
	}
	if create.SessionID == "" {
		create.SessionID = sessionIDFromPathContext(ctx)
	}
	s.handleSessionMessage(c, ctx, create)
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

func agentIDFromUserAgentPath(ctx *app.RequestContext) string {
	if agentID := ctx.Param("agentId"); agentID != "" {
		return agentID
	}
	if raw, ok := ctx.Get("routeAgentID"); ok {
		if agentID, ok := raw.(string); ok && agentID != "" {
			return agentID
		}
	}
	path := string(ctx.Path())
	if path == "" {
		path = string(ctx.Request.URI().PathOriginal())
	}
	if path == "" {
		path = string(ctx.Request.URI().Path())
	}
	return agentIDFromPath(path)
}

func sessionIDFromPathContext(ctx *app.RequestContext) string {
	if sessionID := ctx.Param("sessionId"); sessionID != "" {
		return sessionID
	}
	if raw, ok := ctx.Get("routeSessionID"); ok {
		if sessionID, ok := raw.(string); ok && sessionID != "" {
			return sessionID
		}
	}
	path := string(ctx.Path())
	if path == "" {
		path = string(ctx.Request.URI().PathOriginal())
	}
	if path == "" {
		path = string(ctx.Request.URI().Path())
	}
	return sessionIDFromPath(path)
}

func mergeCreateMailboxBody(ctx *app.RequestContext, req *CreateMailboxRequest) {
	if len(ctx.Request.Body()) == 0 {
		return
	}
	var body CreateMailboxRequest
	if err := json.Unmarshal(ctx.Request.Body(), &body); err != nil {
		return
	}
	if req.SessionID == "" {
		req.SessionID = body.SessionID
	}
	if req.Message == "" {
		req.Message = body.Message
	}
	if req.MessageType == "" {
		req.MessageType = body.MessageType
	}
	if len(req.Payload) == 0 {
		req.Payload = body.Payload
	}
}

func agentIDFromPath(path string) string {
	const prefix = "/api/user/agents/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	agentID, _, _ := strings.Cut(rest, "/")
	return agentID
}

func sessionIDFromPath(path string) string {
	if rest, ok := strings.CutPrefix(path, "/api/agent/sessions/"); ok {
		sessionID, _, _ := strings.Cut(rest, "/")
		return sessionID
	}
	rest, ok := strings.CutPrefix(path, "/api/user/agents/")
	if !ok {
		return ""
	}
	_, rest, ok = strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	rest, ok = strings.CutPrefix(rest, "sessions/")
	if !ok {
		return ""
	}
	sessionID, _, _ := strings.Cut(rest, "/")
	return sessionID
}
