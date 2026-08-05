package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	hzapi "github.com/pax-beehive/pax-manager/internal/transport/http/model/paxmanager/api"
)

func ptrString(v string) *string {
	return &v
}

func ptrInt32(v int) *int32 {
	out := int32(v)
	return &out
}

func ptrInt64(v int64) *int64 {
	return &v
}

func Health(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).Health(c, ctx, &hzapi.EmptyRequest{})
}

func RegisterAgent(c context.Context, ctx *app.RequestContext) {
	var req hzapi.RegisterAgentRequest
	decodeBody(ctx, &req)
	serviceFromContext(ctx).RegisterAgent(c, ctx, &req)
}

func ReportAgentStatus(c context.Context, ctx *app.RequestContext) {
	var req hzapi.AgentStatusReportRequest
	decodeBody(ctx, &req)
	serviceFromContext(ctx).ReportAgentStatus(c, ctx, &req)
}

func PullMailbox(c context.Context, ctx *app.RequestContext) {
	req := hzapi.PullMailboxRequest{}
	req.Offset = ptrInt64(queryInt64(ctx, "offset"))
	req.Limit = ptrInt32(queryInt(ctx, "limit"))
	serviceFromContext(ctx).PullMailbox(c, ctx, &req)
}

func PullSessionMailbox(c context.Context, ctx *app.RequestContext) {
	req := hzapi.PullSessionMailboxRequest{}
	req.SessionID = ptrString(ctx.Param("sessionId"))
	req.Offset = ptrInt64(queryInt64(ctx, "offset"))
	req.Limit = ptrInt32(queryInt(ctx, "limit"))
	serviceFromContext(ctx).PullSessionMailbox(c, ctx, &req)
}

func UpdateMailboxOffset(c context.Context, ctx *app.RequestContext) {
	var req hzapi.UpdateMailboxOffsetRequest
	decodeBody(ctx, &req)
	serviceFromContext(ctx).UpdateMailboxOffset(c, ctx, &req)
}

func ReportMessageResult(c context.Context, ctx *app.RequestContext) {
	var req hzapi.ReportMessageResultRequest
	decodeBody(ctx, &req)
	req.MessageID = ptrString(ctx.Param("messageId"))
	serviceFromContext(ctx).ReportMessageResult(c, ctx, &req)
}

func ListAgents(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).ListAgents(c, ctx, &hzapi.EmptyRequest{})
}

func GetAgent(c context.Context, ctx *app.RequestContext) {
	req := hzapi.GetAgentRequest{}
	req.AgentID = ptrString(ctx.Param("agentId"))
	serviceFromContext(ctx).GetAgent(c, ctx, &req)
}

func DeleteAgent(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).handleDeleteAgent(c, ctx, agentIDFromUserAgentPath(ctx))
}

func ListAgentSessions(c context.Context, ctx *app.RequestContext) {
	req := hzapi.ListAgentSessionsRequest{}
	req.AgentID = ptrString(ctx.Param("agentId"))
	serviceFromContext(ctx).ListAgentSessions(c, ctx, &req)
}

func GetAgentSession(c context.Context, ctx *app.RequestContext) {
	req := hzapi.GetAgentSessionRequest{}
	req.AgentID = ptrString(ctx.Param("agentId"))
	req.SessionID = ptrString(ctx.Param("sessionId"))
	serviceFromContext(ctx).GetAgentSession(c, ctx, &req)
}

func ListAgentMessages(c context.Context, ctx *app.RequestContext) {
	req := hzapi.ListAgentMessagesRequest{}
	req.AgentID = ptrString(ctx.Param("agentId"))
	req.SessionID = ptrString(string(ctx.QueryArgs().Peek("session_id")))
	req.Status = ptrString(string(ctx.QueryArgs().Peek("status")))
	req.Limit = ptrInt32(queryInt(ctx, "limit"))
	serviceFromContext(ctx).ListAgentMessages(c, ctx, &req)
}

func CreateAgentMessage(c context.Context, ctx *app.RequestContext) {
	req := hzapi.CreateAgentMessageRequest{}
	req.AgentID = ptrString(ctx.Param("agentId"))
	serviceFromContext(ctx).CreateAgentMessage(c, ctx, &req)
}

func ListAgentSessionMessages(c context.Context, ctx *app.RequestContext) {
	req := hzapi.ListAgentSessionMessagesRequest{}
	req.AgentID = ptrString(ctx.Param("agentId"))
	req.SessionID = ptrString(ctx.Param("sessionId"))
	serviceFromContext(ctx).ListAgentSessionMessages(c, ctx, &req)
}

func CreateSessionMessage(c context.Context, ctx *app.RequestContext) {
	req := hzapi.CreateSessionMessageRequest{}
	req.AgentID = ptrString(ctx.Param("agentId"))
	req.SessionID = ptrString(ctx.Param("sessionId"))
	serviceFromContext(ctx).CreateSessionMessage(c, ctx, &req)
}

func CreateAgentRegistrationToken(c context.Context, ctx *app.RequestContext) {
	var req hzapi.CreateRegistrationTokenRequest
	decodeBody(ctx, &req)
	serviceFromContext(ctx).CreateAgentRegistrationToken(c, ctx, &req)
}

type generatedHandlerFunc func(context.Context, *app.RequestContext)

var generatedHandlerBridge = map[string]generatedHandlerFunc{
	"ArchiveProject":                ArchiveProject,
	"CreateNodeAgent":               CreateNodeAgent,
	"CreateNodeAgentApproval":       CreateNodeAgentApproval,
	"CreateNodeAgentMessage":        CreateNodeAgentMessage,
	"CreateNodeAgentSession":        CreateNodeAgentSession,
	"CreateNodeAgentSessionMessage": CreateNodeAgentSessionMessage,
	"CreateNodeOutboundMessage":     CreateNodeOutboundMessage,
	"CreateNodeRegistrationToken":   CreateNodeRegistrationToken,
	"CreateProject":                 CreateProject,
	"CreateProjectTarget":           CreateProjectTarget,
	"CreateUserSecret":              CreateUserSecret,
	"DecideUserApproval":            DecideUserApproval,
	"DeleteNode":                    DeleteNode,
	"DeleteNodeAgent":               DeleteNodeAgent,
	"GetCurrentUser":                GetCurrentUser,
	"GetNode":                       GetNode,
	"GetNodeAgent":                  GetNodeAgent,
	"GetNodeAgentApproval":          GetNodeAgentApproval,
	"GetNodeAgentSession":           GetNodeAgentSession,
	"GetProject":                    GetProject,
	"GetProjectTarget":              GetProjectTarget,
	"GetUserApproval":               GetUserApproval,
	"GetUserSecret":                 GetUserSecret,
	"ListNodeAgentMessages":         ListNodeAgentMessages,
	"ListNodeAgentSessionMessages":  ListNodeAgentSessionMessages,
	"ListNodeAgentSessions":         ListNodeAgentSessions,
	"ListNodeAgents":                ListNodeAgents,
	"ListNodes":                     ListNodes,
	"ListProjects":                  ListProjects,
	"ListProjectTargets":            ListProjectTargets,
	"ListUserApprovalGrants":        ListUserApprovalGrants,
	"ListUserApprovals":             ListUserApprovals,
	"ListUserSecrets":               ListUserSecrets,
	"ListUserSessions":              ListSessions,
	"MarkNodeMessageDelivered":      MarkNodeMessageDelivered,
	"PullNodeAgentMailbox":          PullNodeAgentMailbox,
	"PullNodeAgentSessionMailbox":   PullNodeAgentSessionMailbox,
	"PullNodeMailbox":               PullNodeMailbox,
	"RegisterNode":                  RegisterNode,
	"RegisterNodeAgent":             RegisterNodeAgent,
	"ReportNodeMessageResult":       ReportNodeMessageResult,
	"ReportNodeStatus":              ReportNodeStatus,
	"ResolveNodeSecret":             ResolveNodeSecret,
	"RevokeUserApprovalGrant":       RevokeUserApprovalGrant,
	"UpdateNode":                    UpdateNode,
	"UpdateNodeAgent":               UpdateNodeAgent,
	"UpdateNodeAgentSession":        UpdateNodeAgentSession,
	"UpdateNodeMailboxOffset":       UpdateNodeMailboxOffset,
	"UpdateProject":                 UpdateProject,
	"UpdateProjectTarget":           UpdateProjectTarget,
	"WriteNodeSecretVersion":        WriteNodeSecretVersion,
}

var generatedNodeDaemonHandlerBridge = map[string]generatedHandlerFunc{
	"CreateNodeDaemonAgentConnection":  CreateNodeDaemonAgentConnection,
	"DiscoverNodeDaemonHarnesses":      DiscoverNodeDaemonHarnesses,
	"GetNodeDaemonStatus":              GetNodeDaemonStatus,
	"GetNodeDaemonCommand":             GetNodeDaemonCommand,
	"ListNodeDaemonAgentConnections":   ListNodeDaemonAgentConnections,
	"ListNodeDaemonHarnesses":          ListNodeDaemonHarnesses,
	"RemoveNodeDaemonAgentConnection":  RemoveNodeDaemonAgentConnection,
	"RestartNodeDaemon":                RestartNodeDaemon,
	"RestartNodeDaemonAgentConnection": RestartNodeDaemonAgentConnection,
	"StopNodeDaemonAgentConnection":    StopNodeDaemonAgentConnection,
	"UpdateNodeDaemonAgentConnection":  UpdateNodeDaemonAgentConnection,
}

func (s *Service) HandleGenerated(c context.Context, ctx *app.RequestContext, name string) {
	if handler, ok := generatedHandlerBridge[name]; ok {
		handler(c, ctx)
		return
	}
	if handler, ok := generatedNodeDaemonHandlerBridge[name]; ok {
		handler(c, ctx)
		return
	}
	writeEndpointResult(ctx, http.StatusNotFound, nil, apperr.Error{
		Status:  http.StatusNotFound,
		Message: "generated handler not found",
	})
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

func (s *Service) DeleteAgent(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.GetAgentRequest,
) {
	agentID := req.GetAgentID()
	if agentID == "" {
		agentID = agentIDFromUserAgentPath(ctx)
	}
	s.handleDeleteAgent(c, ctx, agentID)
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
	name := ""
	if req != nil {
		name = req.GetName()
	}
	s.handleCreateUserAPIKey(c, ctx, CreateUserAPIKeyRequest{Name: name})
}

func (s *Service) RevokeUserAPIKey(
	c context.Context,
	ctx *app.RequestContext,
	req *hzapi.RevokeUserAPIKeyRequest,
) {
	keyID := ""
	if req != nil {
		keyID = req.GetKeyID()
	}
	if keyID == "" {
		keyID = ctx.Param("keyId")
	}
	if keyID == "" {
		keyID = ctx.Param("key_id")
	}
	s.handleRevokeUserAPIKey(c, ctx, keyID)
}

func agentIDFromUserAgentPath(ctx *app.RequestContext) string {
	if agentID := ctx.Param("agentId"); agentID != "" {
		return agentID
	}
	if agentID := ctx.Param("agent_id"); agentID != "" {
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
	for _, prefix := range []string{"/api/user/agents/", "/api/v1/user/"} {
		if !strings.Contains(path, prefix) {
			continue
		}
		_, rest, ok := strings.Cut(path, prefix)
		if !ok {
			continue
		}
		if prefix == "/api/v1/user/" {
			_, rest, ok = strings.Cut(rest, "/agents/")
			if !ok {
				continue
			}
		}
		agentID, _, _ := strings.Cut(rest, "/")
		return agentID
	}
	return ""
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
