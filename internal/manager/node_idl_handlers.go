package manager

import (
	"context"
	"encoding/json"
	"net/http"
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
	status, data, err := serviceFromContext(
		ctx,
	).paxd.RegisterNodeAgent(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ReportNodeStatus(c context.Context, ctx *app.RequestContext) {
	var req NodeStatusReport
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).paxd.ReportNodeStatus(c, nodeFromContext(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func ReportNodeAgentSessions(c context.Context, ctx *app.RequestContext) {
	var req NodeAgentSessionReport
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).paxd.ReportNodeAgentSessions(
		c,
		nodeFromContext(ctx),
		ctx.Param("agent_id"),
		req,
	)
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
	req.MessageID = ctx.Param("message_id")
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
	req.MessageID = ctx.Param("message_id")
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

func ResolveNodeSecret(c context.Context, ctx *app.RequestContext) {
	var req ResolveSecretRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).paxd.ResolveSecret(c, nodeFromContext(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func WriteNodeSecretVersion(c context.Context, ctx *app.RequestContext) {
	var req WriteSecretVersionRequest
	decodeBody(ctx, &req)
	req.SecretID = ctx.Param("secret_id")
	status, data, err := serviceFromContext(
		ctx,
	).paxd.WriteSecretVersion(
		c,
		nodeFromContext(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeAgentApproval(c context.Context, ctx *app.RequestContext) {
	var req CreateApprovalRequest
	decodeBody(ctx, &req)
	req.AgentID = firstString(req.AgentID, ctx.Param("agent_id"))
	appendAllowAlwaysOnAllAgentsOption(&req.Options)
	approval, err := serviceFromContext(ctx).store.CreateApproval(c, nodeFromContext(ctx), req)
	writeEndpointResult(ctx, httpStatusOK(err), map[string]any{"approval": approval}, err)
}

func GetNodeAgentApproval(c context.Context, ctx *app.RequestContext) {
	approval, err := serviceFromContext(ctx).store.GetNodeApproval(
		c,
		nodeFromContext(ctx),
		ctx.Param("agent_id"),
		ctx.Param("approval_id"),
	)
	writeEndpointResult(ctx, httpStatusOK(err), map[string]any{"approval": approval}, err)
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

func GetNodeDaemonStatus(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetNodeDaemonStatus(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListNodeDaemonHarnesses(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListNodeDaemonHarnesses(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		queryBool(ctx, "include_missing"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func DiscoverNodeDaemonHarnesses(c context.Context, ctx *app.RequestContext) {
	var req DiscoverNodeDaemonHarnessesRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	status, data, err := serviceFromContext(ctx).userapi.DiscoverNodeDaemonHarnesses(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListNodeDaemonAgentConnections(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListNodeDaemonAgentConnections(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		queryBool(ctx, "include_disabled"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeDaemonAgentConnection(c context.Context, ctx *app.RequestContext) {
	var req CreateNodeDaemonAgentConnectionRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	status, data, err := serviceFromContext(ctx).userapi.CreateNodeDaemonAgentConnection(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func UpdateNodeDaemonAgentConnection(c context.Context, ctx *app.RequestContext) {
	var req UpdateNodeDaemonAgentConnectionRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	req.ConnectionID = ctx.Param("connection_id")
	status, data, err := serviceFromContext(ctx).userapi.UpdateNodeDaemonAgentConnection(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func StopNodeDaemonAgentConnection(c context.Context, ctx *app.RequestContext) {
	var req NodeDaemonAgentConnectionActionRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	req.ConnectionID = ctx.Param("connection_id")
	status, data, err := serviceFromContext(ctx).userapi.StopNodeDaemonAgentConnection(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func RestartNodeDaemonAgentConnection(c context.Context, ctx *app.RequestContext) {
	var req NodeDaemonAgentConnectionActionRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	req.ConnectionID = ctx.Param("connection_id")
	status, data, err := serviceFromContext(ctx).userapi.RestartNodeDaemonAgentConnection(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func RemoveNodeDaemonAgentConnection(c context.Context, ctx *app.RequestContext) {
	var req NodeDaemonAgentConnectionActionRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	req.ConnectionID = ctx.Param("connection_id")
	status, data, err := serviceFromContext(ctx).userapi.RemoveNodeDaemonAgentConnection(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetNodeDaemonCommand(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetNodeDaemonCommand(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		ctx.Param("command_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func UpdateNode(c context.Context, ctx *app.RequestContext) {
	var req UpdateNodeRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	status, data, err := serviceFromContext(ctx).userapi.UpdateNode(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func DeleteNode(c context.Context, ctx *app.RequestContext) {
	req := DeleteNodeRequest{
		UserID: ctx.Param("user_id"),
		NodeID: ctx.Param("node_id"),
	}
	status, data, err := serviceFromContext(ctx).userapi.DeleteNode(
		c,
		requestMetadata(ctx),
		req,
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
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
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
	status, data, err := serviceFromContext(ctx).userapi.GetNodeAgent(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		ctx.Param("agent_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func UpdateNodeAgent(c context.Context, ctx *app.RequestContext) {
	var req UpdateAgentProfileRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	req.AgentID = ctx.Param("agent_id")
	status, data, err := serviceFromContext(ctx).userapi.UpdateNodeAgent(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func DeleteNodeAgent(c context.Context, ctx *app.RequestContext) {
	req := DeleteAgentRequest{
		UserID:  ctx.Param("user_id"),
		NodeID:  ctx.Param("node_id"),
		AgentID: ctx.Param("agent_id"),
	}
	status, data, err := serviceFromContext(ctx).userapi.DeleteNodeAgent(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListNodeAgentMessages(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListNodeMailbox(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
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
	req.NodeID = ctx.Param("node_id")
	req.AgentID = ctx.Param("agent_id")
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
	status, data, err := serviceFromContext(ctx).userapi.ListNodeAgentSessions(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		ctx.Param("agent_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListSessions(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListSessions(
		c,
		requestMetadata(ctx),
		ctx.Param("user_id"),
		string(ctx.QueryArgs().Peek("node_id")),
		string(ctx.QueryArgs().Peek("agent_id")),
		string(ctx.QueryArgs().Peek("primary_project_id")),
		queryInt(ctx, "page_size"),
		queryInt(ctx, "page_num"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListUserSessions(c context.Context, ctx *app.RequestContext) {
	ListSessions(c, ctx)
}

func CreateNodeAgentSession(c context.Context, ctx *app.RequestContext) {
	var req CreateSessionRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	req.AgentID = ctx.Param("agent_id")
	status, data, err := serviceFromContext(ctx).userapi.CreateNodeAgentSession(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetNodeAgentSession(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetNodeAgentSession(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		ctx.Param("agent_id"),
		ctx.Param("session_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func UpdateNodeAgentSession(c context.Context, ctx *app.RequestContext) {
	var req UpdateSessionRequest
	decodeBody(ctx, &req)
	req.UserID = ctx.Param("user_id")
	req.NodeID = ctx.Param("node_id")
	req.AgentID = ctx.Param("agent_id")
	req.SessionID = ctx.Param("session_id")
	status, data, err := serviceFromContext(ctx).userapi.UpdateNodeAgentSession(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListNodeAgentSessionMessages(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListNodeAgentSessionMessages(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		ctx.Param("agent_id"),
		ctx.Param("session_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListAgentSessionHistory(c context.Context, ctx *app.RequestContext) {
	beforeID, validBeforeID := queryOptionalPositiveInt64(ctx, "before_id")
	if !validBeforeID {
		writeError(ctx, http.StatusBadRequest, "before_id must be a positive integer")
		return
	}
	status, data, err := serviceFromContext(ctx).userapi.ListAgentSessionHistory(
		c,
		requestMetadata(ctx),
		firstString(ctx.Param("agentId"), ctx.Param("agent_id"), ctx.Param("agentID")),
		firstString(ctx.Param("sessionId"), ctx.Param("session_id"), ctx.Param("sessionID")),
		queryInt(ctx, "limit"),
		beforeID,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListSessionHistory(c context.Context, ctx *app.RequestContext) {
	beforeID, validBeforeID := queryOptionalPositiveInt64(ctx, "before_id")
	if !validBeforeID {
		writeError(ctx, http.StatusBadRequest, "before_id must be a positive integer")
		return
	}
	status, data, err := serviceFromContext(ctx).userapi.ListSessionHistory(
		c,
		requestMetadata(ctx),
		firstString(ctx.Param("sessionId"), ctx.Param("session_id"), ctx.Param("sessionID")),
		queryInt(ctx, "limit"),
		beforeID,
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateNodeAgentSessionMessage(c context.Context, ctx *app.RequestContext) {
	var req CreateMailboxRequest
	decodeBody(ctx, &req)
	req.NodeID = ctx.Param("node_id")
	req.AgentID = ctx.Param("agent_id")
	req.SessionID = ctx.Param("session_id")
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

func CreateUserSecret(c context.Context, ctx *app.RequestContext) {
	var req CreateSecretRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateSecret(c, requestMetadata(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func ListUserSecrets(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListSecrets(c, requestMetadata(ctx))
	writeEndpointResult(ctx, status, data, err)
}

func GetUserSecret(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetSecret(
		c,
		requestMetadata(ctx),
		ctx.Param("secret_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListUserApprovals(c context.Context, ctx *app.RequestContext) {
	principal, err := serviceFromContext(ctx).userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	approvals, err := serviceFromContext(ctx).store.ListApprovals(c, ApprovalFilter{
		Principal:        principal,
		Status:           string(ctx.QueryArgs().Peek("status")),
		Decision:         string(ctx.QueryArgs().Peek("decision")),
		Domain:           string(ctx.QueryArgs().Peek("domain")),
		Operation:        string(ctx.QueryArgs().Peek("operation")),
		ResourceType:     string(ctx.QueryArgs().Peek("resource_type")),
		ResourceRef:      string(ctx.QueryArgs().Peek("resource_ref")),
		RequestNodeID:    string(ctx.QueryArgs().Peek("request_node_id")),
		RequestAgentID:   string(ctx.QueryArgs().Peek("request_agent_id")),
		RequestSessionID: string(ctx.QueryArgs().Peek("request_session_id")),
		DecisionScope:    string(ctx.QueryArgs().Peek("decision_scope")),
		IncludeRevoked:   queryBool(ctx, "include_revoked"),
		Limit:            queryInt(ctx, "limit"),
	})
	writeEndpointResult(ctx, httpStatusOK(err), map[string]any{"approvals": approvals}, err)
}

func ListUserAuditEvents(c context.Context, ctx *app.RequestContext) {
	principal, err := serviceFromContext(ctx).userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	events, err := serviceFromContext(ctx).store.ListAuditEvents(c, AuditEventFilter{
		Principal:  principal,
		Query:      string(ctx.QueryArgs().Peek("q")),
		EventType:  string(ctx.QueryArgs().Peek("event_type")),
		AgentID:    string(ctx.QueryArgs().Peek("agent_id")),
		SessionID:  string(ctx.QueryArgs().Peek("session_id")),
		ApprovalID: string(ctx.QueryArgs().Peek("approval_id")),
		Decision:   string(ctx.QueryArgs().Peek("decision")),
		Limit:      queryInt(ctx, "limit"),
	})
	writeEndpointResult(ctx, httpStatusOK(err), map[string]any{"events": events}, err)
}

func GetUserApproval(c context.Context, ctx *app.RequestContext) {
	principal, err := serviceFromContext(ctx).userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	approval, err := serviceFromContext(ctx).store.GetApproval(
		c,
		principal,
		ctx.Param("approval_id"),
	)
	writeEndpointResult(ctx, httpStatusOK(err), map[string]any{"approval": approval}, err)
}

func DecideUserApproval(c context.Context, ctx *app.RequestContext) {
	principal, err := serviceFromContext(ctx).userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var req ApprovalDecisionRequest
	decodeBody(ctx, &req)
	approval, err := serviceFromContext(ctx).store.DecideApproval(
		c,
		principal,
		ctx.Param("approval_id"),
		req,
	)
	writeEndpointResult(ctx, httpStatusOK(err), map[string]any{"approval": approval}, err)
}

func ListUserApprovalGrants(c context.Context, ctx *app.RequestContext) {
	principal, err := serviceFromContext(ctx).userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	grants, err := serviceFromContext(ctx).store.ListApprovalGrants(c, ApprovalGrantFilter{
		Principal:      principal,
		Domain:         string(ctx.QueryArgs().Peek("domain")),
		Operation:      string(ctx.QueryArgs().Peek("operation")),
		ResourceType:   string(ctx.QueryArgs().Peek("resource_type")),
		ResourceRef:    string(ctx.QueryArgs().Peek("resource_ref")),
		DecisionScope:  string(ctx.QueryArgs().Peek("decision_scope")),
		GrantNodeID:    string(ctx.QueryArgs().Peek("grant_node_id")),
		GrantAgentID:   string(ctx.QueryArgs().Peek("grant_agent_id")),
		GrantSessionID: string(ctx.QueryArgs().Peek("grant_session_id")),
		ActiveOnly:     queryBoolDefault(ctx, "active_only", true),
		Limit:          queryInt(ctx, "limit"),
	})
	writeEndpointResult(ctx, httpStatusOK(err), map[string]any{"grants": grants}, err)
}

func RevokeUserApprovalGrant(c context.Context, ctx *app.RequestContext) {
	principal, err := serviceFromContext(ctx).userPrincipal(c, ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	var req RevokeApprovalGrantRequest
	decodeBody(ctx, &req)
	approval, err := serviceFromContext(ctx).store.RevokeApprovalGrant(
		c,
		principal,
		ctx.Param("grant_id"),
		req,
	)
	writeEndpointResult(ctx, httpStatusOK(err), map[string]any{"approval": approval}, err)
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

func queryOptionalPositiveInt64(ctx *app.RequestContext, key string) (int64, bool) {
	raw := string(ctx.QueryArgs().Peek(key))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value > 0
}

func queryInt(ctx *app.RequestContext, key string) int {
	value, err := strconv.Atoi(string(ctx.QueryArgs().Peek(key)))
	if err != nil {
		return 0
	}
	return value
}

func queryBool(ctx *app.RequestContext, key string) bool {
	return queryBoolDefault(ctx, key, false)
}

func queryBoolDefault(ctx *app.RequestContext, key string, fallback bool) bool {
	raw := string(ctx.QueryArgs().Peek(key))
	if raw == "" {
		return fallback
	}
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func httpStatusOK(err error) int {
	if err != nil {
		return 0
	}
	return 200
}

func appendAllowAlwaysOnAllAgentsOption(options *[]ApprovalOption) {
	for _, option := range *options {
		if option.OptionID == "allow_always_on_all_agents" {
			return
		}
	}
	*options = append(*options, ApprovalOption{
		OptionID: "allow_always_on_all_agents",
		Label:    "Allow always on all agents",
		Decision: "allow",
		Scope:    "across_all_agents",
	})
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
