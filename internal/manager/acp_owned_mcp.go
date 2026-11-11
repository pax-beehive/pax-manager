package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Match only the tools registered by the per-session Pax conversation server.
// Agent adapters render MCP identities differently; keep each form explicit.
func paxConversationToolName(name, sessionID string) bool {
	server := agentConversationMCPServerName(sessionID)
	normalized := strings.ReplaceAll(server, "-", "_")
	for _, tool := range []string{
		"list_agents", "ask", "reply", "publish_artifact",
		"workspace_search", "workspace_read", "workspace_expand",
		"workspace_history", "workspace_summaries",
		"workspace_request_access",
	} {
		for _, candidate := range []string{
			"mcp__" + server + "__" + tool,
			"mcp__" + normalized + "__" + tool,
			"mcp_" + normalized + "_" + tool,
			"mcp." + server + "." + tool,
		} {
			if name == candidate {
				return true
			}
		}
	}
	return false
}

func (m acpApprovalMiddleware) autoApproveOwnedMCP(
	ctx context.Context,
	frame *acpFrameContext,
	params map[string]any,
) (bool, error) {
	call, ok := m.ownedMCPToolCall(ctx, frame, params)
	if !ok {
		return false, nil
	}
	// Never select a guessed native option or create a pending audit record for
	// a request whose adapter does not offer a one-time grant.
	if len(frame.frame.ID) == 0 {
		return true, errors.New("permission request missing JSON-RPC id")
	}
	if acpPermissionOptionIDByKind(frame.payload, "allow_once") == "" {
		return false, nil
	}
	toolCall, _ := params["toolCall"].(map[string]any)
	toolCall["title"] = call.Title
	toolCall["kind"] = call.Kind
	if len(call.RawInput) > 0 && string(call.RawInput) != "null" {
		toolCall["rawInput"] = call.RawInput
	}
	frame.frame.Params = mustMarshalRaw(params)
	if err := frame.replaceFrame(frame.frame); err != nil {
		return true, err
	}
	return m.autoApprovePermission(
		ctx, frame, params, frame.managerSessionID,
		json.RawMessage(`{"approval_policy":"pax_owned_mcp"}`), nil,
	)
}

func (m acpApprovalMiddleware) ownedMCPToolCall(
	ctx context.Context,
	frame *acpFrameContext,
	params map[string]any,
) (domain.RuntimeToolCall, bool) {
	if !m.ownedMCPSession(ctx, frame) {
		return domain.RuntimeToolCall{}, false
	}
	state := m.runtime.CurrentCorrelation(frame.agent.agentID, frame.managerSessionID)
	turnID := firstNonEmpty(frame.transportMetadata["turn_id"], frame.businessTurnID)
	explicitlyTagged := turnID != ""
	turnID = firstNonEmpty(turnID, state.ActiveTurnID)
	if turnID == "" || state.ActiveTurnID != turnID || state.ActivePromptRequestID == "" ||
		state.OwnerUserID != frame.agent.ownerUserID || state.NodeID != frame.agent.nodeID {
		return domain.RuntimeToolCall{}, false
	}
	if frame.businessTurnID != "" && frame.businessTurnID != turnID {
		return domain.RuntimeToolCall{}, false
	}
	call := runtimeToolCallFromPermission(params)
	if call.ToolCallID == "" {
		return domain.RuntimeToolCall{}, false
	}
	cachedInTurn := false
	for _, cached := range state.ActiveToolCalls {
		if cached.ToolCallID == call.ToolCallID {
			var ok bool
			call, ok = mergeOwnedMCPToolCall(cached, call)
			if !ok {
				return domain.RuntimeToolCall{}, false
			}
			cachedInTurn = true
			break
		}
	}
	if !explicitlyTagged && !cachedInTurn {
		return domain.RuntimeToolCall{}, false
	}
	if !pendingOwnedMCPToolCall(call, frame.managerSessionID) {
		return domain.RuntimeToolCall{}, false
	}
	frame.businessTurnID = turnID
	return call, true
}

func (m acpApprovalMiddleware) ownedMCPSession(ctx context.Context, frame *acpFrameContext) bool {
	if m.store == nil || m.runtime == nil || frame == nil || frame.agent == nil ||
		frame.managerSessionID == "" {
		return false
	}
	session, err := m.store.GetSession(ctx,
		domain.UserPrincipal{User: domain.User{UserID: frame.agent.ownerUserID}},
		frame.managerSessionID,
	)
	if err != nil || session.AgentID != frame.agent.agentID ||
		session.NodeID != frame.agent.nodeID ||
		session.ArchivedAt != nil ||
		session.Transport != domain.SessionTransportManager ||
		len(agentConversationMCPServers(session)) == 0 {
		return false
	}
	return true
}

func mergeOwnedMCPToolCall(
	cached, requested domain.RuntimeToolCall,
) (domain.RuntimeToolCall, bool) {
	if cached.Status == "completed" || cached.Status == "failed" ||
		(cached.Title != "" && requested.Title != "" && cached.Title != requested.Title) ||
		(cached.Kind != "" && requested.Kind != "" && cached.Kind != requested.Kind) ||
		!consistentPaxMCPToolIdentity(cached, requested) {
		return domain.RuntimeToolCall{}, false
	}
	return mergeRuntimeToolCall(cached, requested), true
}

func pendingOwnedMCPToolCall(call domain.RuntimeToolCall, sessionID string) bool {
	if call.Status != "pending" && call.Status != "in_progress" {
		return false
	}
	// Codex ACP labels MCP calls as execute and includes an explicit server /
	// tool identity. Require both fields and the matching rendered title; an
	// execute tool cannot gain approval by copying an MCP-looking title.
	if strings.HasPrefix(call.Title, "mcp.") {
		return structuredPaxConversationToolCall(call, sessionID)
	}
	switch call.Kind {
	case "other", "read", "search", "fetch":
		return paxConversationToolName(call.Title, sessionID)
	default:
		return false
	}
}

func structuredPaxConversationToolCall(call domain.RuntimeToolCall, sessionID string) bool {
	if call.Kind != "execute" && call.Kind != "other" {
		return false
	}
	var input struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	}
	if json.Unmarshal(call.RawInput, &input) != nil ||
		input.Server != agentConversationMCPServerName(sessionID) {
		return false
	}
	return call.Title == "mcp."+input.Server+"."+input.Tool &&
		paxConversationToolName("mcp__"+input.Server+"__"+input.Tool, sessionID)
}

// Arguments may change as an adapter fills in a tool call, but an already
// observed MCP identity must not be replaced by a different server or tool.
func consistentPaxMCPToolIdentity(cached, requested domain.RuntimeToolCall) bool {
	if !strings.HasPrefix(firstNonEmpty(cached.Title, requested.Title), "mcp.") ||
		len(cached.RawInput) == 0 || string(cached.RawInput) == "null" ||
		len(requested.RawInput) == 0 || string(requested.RawInput) == "null" {
		return true
	}
	var previous, next struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	}
	if json.Unmarshal(cached.RawInput, &previous) != nil ||
		json.Unmarshal(requested.RawInput, &next) != nil {
		return false
	}
	if previous.Server != "" && next.Server != "" && previous.Server != next.Server {
		return false
	}
	return previous.Tool == "" || next.Tool == "" || previous.Tool == next.Tool
}
