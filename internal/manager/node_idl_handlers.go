package manager

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
)

func RegisterNode(c context.Context, ctx *app.RequestContext) {
	var req RegisterNodeRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).paxd.RegisterNode(c, requestMetadata(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func RegisterNodeAgent(c context.Context, ctx *app.RequestContext) {
	var req RegisterNodeAgentRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).paxd.RegisterNodeAgent(c, requestMetadata(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func ReportNodeStatus(c context.Context, ctx *app.RequestContext) {
	var req NodeStatusReport
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).paxd.ReportNodeStatus(c, nodeFromContext(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func PullNodeMailbox(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).paxd.PullNodeMailbox(
		c,
		nodeFromContext(ctx),
		"",
		"",
		queryInt64(ctx, "offset"),
		queryInt(ctx, "limit"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func PullNodeAgentMailbox(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).paxd.PullNodeMailbox(
		c,
		nodeFromContext(ctx),
		ctx.Param("agent_id"),
		"",
		queryInt64(ctx, "offset"),
		queryInt(ctx, "limit"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func PullNodeAgentSessionMailbox(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).paxd.PullNodeMailbox(
		c,
		nodeFromContext(ctx),
		ctx.Param("agent_id"),
		ctx.Param("session_id"),
		queryInt64(ctx, "offset"),
		queryInt(ctx, "limit"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func UpdateNodeMailboxOffset(c context.Context, ctx *app.RequestContext) {
	var req OffsetRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).paxd.UpdateNodeOffset(
		c,
		nodeFromContext(ctx),
		req.Offset,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ReportNodeMessageResult(c context.Context, ctx *app.RequestContext) {
	var req MessageResultRequest
	decodeBody(ctx, &req)
	req.MessageID = firstString(req.MessageID, ctx.Param("message_id"))
	status, data, err := serviceFromContext(ctx).paxd.ReportNodeMessageResult(
		c,
		nodeFromContext(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func MarkNodeMessageDelivered(c context.Context, ctx *app.RequestContext) {
	var req MarkDeliveredRequest
	decodeBody(ctx, &req)
	req.MessageID = firstString(req.MessageID, ctx.Param("message_id"))
	status, data, err := serviceFromContext(ctx).paxd.MarkNodeMessageDelivered(
		c,
		nodeFromContext(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeOutboundMessage(c context.Context, ctx *app.RequestContext) {
	var req CreateOutboundMessageRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).paxd.CreateNodeOutboundMessage(
		c,
		nodeFromContext(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetCurrentUser(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.CurrentUser(c, requestMetadata(ctx))
	writeEndpointResult(ctx, status, data, err)
}

func ListNodes(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListNodes(c, requestMetadata(ctx))
	writeEndpointResult(ctx, status, data, err)
}

func GetNode(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetNode(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListNodeAgents(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListNodeAgents(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeAgent(c context.Context, ctx *app.RequestContext) {
	var req CreateAgentRequest
	decodeBody(ctx, &req)
	req.UserID = firstString(req.UserID, ctx.Param("user_id"))
	req.NodeID = firstString(req.NodeID, ctx.Param("node_id"))
	status, data, err := serviceFromContext(
		ctx,
	).userapi.CreateNodeAgent(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetNodeAgent(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetAgent(
		c,
		requestMetadata(ctx),
		ctx.Param("agent_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListNodeAgentMessages(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListMailbox(
		c,
		requestMetadata(ctx),
		ctx.Param("agent_id"),
		string(ctx.QueryArgs().Peek("session_id")),
		string(ctx.QueryArgs().Peek("status")),
		queryInt(ctx, "limit"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeAgentMessage(c context.Context, ctx *app.RequestContext) {
	var req CreateMailboxRequest
	decodeBody(ctx, &req)
	req.NodeID = firstString(req.NodeID, ctx.Param("node_id"))
	req.AgentID = firstString(req.AgentID, ctx.Param("agent_id"))
	status, data, err := serviceFromContext(
		ctx,
	).userapi.CreateMailboxMessage(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListNodeAgentSessions(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListAgentSessions(
		c,
		requestMetadata(ctx),
		ctx.Param("agent_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeAgentSession(c context.Context, ctx *app.RequestContext) {
	var req CreateSessionRequest
	decodeBody(ctx, &req)
	req.UserID = firstString(req.UserID, ctx.Param("user_id"))
	req.NodeID = firstString(req.NodeID, ctx.Param("node_id"))
	req.AgentID = firstString(req.AgentID, ctx.Param("agent_id"))
	status, data, err := serviceFromContext(ctx).userapi.CreateNodeAgentSession(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetNodeAgentSession(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetAgentSession(
		c,
		requestMetadata(ctx),
		ctx.Param("agent_id"),
		ctx.Param("session_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListNodeAgentSessionMessages(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListAgentSessionMessages(
		c,
		requestMetadata(ctx),
		ctx.Param("agent_id"),
		ctx.Param("session_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeAgentSessionMessage(c context.Context, ctx *app.RequestContext) {
	var req CreateMailboxRequest
	decodeBody(ctx, &req)
	req.NodeID = firstString(req.NodeID, ctx.Param("node_id"))
	req.AgentID = firstString(req.AgentID, ctx.Param("agent_id"))
	req.SessionID = firstString(req.SessionID, ctx.Param("session_id"))
	status, data, err := serviceFromContext(
		ctx,
	).userapi.CreateSessionMessage(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeRegistrationToken(c context.Context, ctx *app.RequestContext) {
	var req CreateRegistrationTokenRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateRegistrationToken(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func decodeBody(ctx *app.RequestContext, v any) {
	if len(ctx.Request.Body()) == 0 {
		return
	}
	_ = json.Unmarshal(ctx.Request.Body(), v)
}

func queryInt64(ctx *app.RequestContext, key string) int64 {
	value, err := strconv.ParseInt(string(ctx.QueryArgs().Peek(key)), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func queryInt(ctx *app.RequestContext, key string) int {
	value, err := strconv.Atoi(string(ctx.QueryArgs().Peek(key)))
	if err != nil {
		return 0
	}
	return value
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
