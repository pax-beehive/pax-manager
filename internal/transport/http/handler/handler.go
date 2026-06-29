package handler

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	api "github.com/pax-beehive/pax-manager/internal/transport/http/model/paxmanager/api"
)

const serviceContextKey = "service"

// Service is the HTTP transport interface implemented by the manager service.
// The generated Hertz router depends only on this interface, keeping generated
// transport code independent from the concrete storage and authentication code.
type Service interface {
	HandleGenerated(context.Context, *app.RequestContext, string)
	Health(context.Context, *app.RequestContext, *api.EmptyRequest)
	RegisterAgent(context.Context, *app.RequestContext, *api.RegisterAgentRequest)
	ReportAgentStatus(context.Context, *app.RequestContext, *api.AgentStatusReportRequest)
	PullMailbox(context.Context, *app.RequestContext, *api.PullMailboxRequest)
	PullSessionMailbox(context.Context, *app.RequestContext, *api.PullSessionMailboxRequest)
	UpdateMailboxOffset(context.Context, *app.RequestContext, *api.UpdateMailboxOffsetRequest)
	ReportMessageResult(context.Context, *app.RequestContext, *api.ReportMessageResultRequest)
	ListAgents(context.Context, *app.RequestContext, *api.EmptyRequest)
	GetAgent(context.Context, *app.RequestContext, *api.GetAgentRequest)
	ListAgentSessions(context.Context, *app.RequestContext, *api.ListAgentSessionsRequest)
	GetAgentSession(context.Context, *app.RequestContext, *api.GetAgentSessionRequest)
	ListAgentMessages(context.Context, *app.RequestContext, *api.ListAgentMessagesRequest)
	CreateAgentMessage(context.Context, *app.RequestContext, *api.CreateAgentMessageRequest)
	ListAgentSessionMessages(
		context.Context,
		*app.RequestContext,
		*api.ListAgentSessionMessagesRequest,
	)
	CreateSessionMessage(context.Context, *app.RequestContext, *api.CreateSessionMessageRequest)
	ListUserAPIKeys(context.Context, *app.RequestContext, *api.EmptyRequest)
	CreateUserAPIKey(context.Context, *app.RequestContext, *api.CreateUserAPIKeyRequest)
	RevokeUserAPIKey(context.Context, *app.RequestContext, *api.RevokeUserAPIKeyRequest)
	CreateAgentRegistrationToken(
		context.Context,
		*app.RequestContext,
		*api.CreateRegistrationTokenRequest,
	)
	AgentAuth(context.Context, *app.RequestContext)
}

func serviceFromContext(ctx *app.RequestContext) Service {
	v, ok := ctx.Get(serviceContextKey)
	if !ok {
		panic("manager service missing from Hertz context")
	}
	s, ok := v.(Service)
	if !ok {
		panic("manager service has unexpected type")
	}
	return s
}

// AgentAuth is used by hz-generated route middleware for paxd-facing routes.
func AgentAuth() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		serviceFromContext(ctx).AgentAuth(c, ctx)
	}
}

func writeError(ctx *app.RequestContext, status int, message string) {
	ctx.JSON(status, map[string]string{"error": message})
	ctx.Abort()
}

func MissingService(c context.Context, ctx *app.RequestContext) {
	writeError(ctx, http.StatusInternalServerError, "manager service is not configured")
}
