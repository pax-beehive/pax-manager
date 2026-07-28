package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	// defaultOwnerAgentLimit caps a discovery response when the caller does not
	// ask for a specific limit.
	defaultOwnerAgentLimit = 50
	// maxOwnerAgentLimit is the hard cap applied regardless of the requested
	// limit so a single call cannot pull an unbounded inventory.
	maxOwnerAgentLimit = 200
)

// ownerAgentView is the discovery payload for one agent. It is intentionally a
// stable, user-facing projection of domain.Agent: the fields an agent needs to
// pick a target (agent_id, name, alias, description) plus placement and
// reachability. It is what the paxd list_agents MCP tool renders.
type ownerAgentView struct {
	AgentID      string     `json:"agent_id"`
	Name         string     `json:"name,omitempty"`
	Alias        string     `json:"alias,omitempty"`
	Type         string     `json:"type,omitempty"`
	Status       string     `json:"status"`
	NodeID       string     `json:"node_id,omitempty"`
	Description  string     `json:"description,omitempty"`
	LastActiveAt *time.Time `json:"last_active_at,omitempty"`
	IsSelf       bool       `json:"is_self"`
}

// ListNodeOwnerAgents handles GET /api/v1/node/agents. The node is authenticated
// by NodeAuth; the calling agent is named by the from_agent_id query param and
// determines whose inventory is returned (its owner's, across all nodes).
func ListNodeOwnerAgents(c context.Context, ctx *app.RequestContext) {
	node := nodeFromContext(ctx)
	fromAgentID := strings.TrimSpace(string(ctx.Query("from_agent_id")))
	filter := domain.OwnerAgentFilter{
		Query:   strings.TrimSpace(string(ctx.Query("query"))),
		Status:  strings.TrimSpace(string(ctx.Query("status"))),
		OrderBy: strings.TrimSpace(string(ctx.Query("order_by"))),
	}
	if raw := strings.TrimSpace(string(ctx.Query("limit"))); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			filter.Limit = n
		}
	}

	agents, err := listNodeOwnerAgents(
		c, serviceFromContext(ctx).store, node, fromAgentID, normalizeOwnerAgentFilter(filter))
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}

	views := make([]ownerAgentView, 0, len(agents))
	for i := range agents {
		views = append(views, newOwnerAgentView(agents[i], fromAgentID))
	}
	writeData(ctx, http.StatusOK, map[string]any{"agents": views})
}

// listNodeOwnerAgents resolves the owner of the calling agent on the
// authenticated node and returns that owner's agents across all nodes. The
// calling agent must belong to the authenticated node: this is what prevents a
// node from listing another owner's inventory by supplying a foreign agent id
// (GetNodeAgent is scoped to node_id, so a foreign agent id resolves to
// ErrNotFound).
func listNodeOwnerAgents(
	ctx context.Context,
	store domain.Store,
	node domain.Node,
	fromAgentID string,
	filter domain.OwnerAgentFilter,
) ([]domain.Agent, error) {
	fromAgentID = strings.TrimSpace(fromAgentID)
	if fromAgentID == "" {
		return nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "from_agent_id is required",
		}
	}
	agent, err := store.GetNodeAgent(ctx, node.NodeID, fromAgentID)
	if err != nil {
		return nil, err
	}
	return store.ListOwnerAgents(ctx, agent.OwnerUserID, filter)
}

// normalizeOwnerAgentFilter applies the HTTP-facing product defaults the store
// and the pure service call deliberately leave out: status defaults to "online"
// (the discovery default), and the limit is defaulted and hard-capped. Passing
// status="any" explicitly opts out of status filtering.
func normalizeOwnerAgentFilter(filter domain.OwnerAgentFilter) domain.OwnerAgentFilter {
	if strings.TrimSpace(filter.Status) == "" {
		filter.Status = "online"
	}
	switch {
	case filter.Limit <= 0:
		filter.Limit = defaultOwnerAgentLimit
	case filter.Limit > maxOwnerAgentLimit:
		filter.Limit = maxOwnerAgentLimit
	}
	return filter
}

// agentAlias returns the owner-editable display alias stored under the agent's
// user_metadata "alias" key. Alias is display/search only; it is never an
// addressing key (messaging uses agent_id).
func agentAlias(agent domain.Agent) string {
	if len(agent.UserMetadata) == 0 {
		return ""
	}
	var meta struct {
		Alias string `json:"alias"`
	}
	if err := json.Unmarshal(agent.UserMetadata, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Alias)
}

func newOwnerAgentView(agent domain.Agent, fromAgentID string) ownerAgentView {
	lastActive := agent.RegisteredAt
	if agent.LastHeartbeat != nil {
		lastActive = *agent.LastHeartbeat
	}
	return ownerAgentView{
		AgentID:      agent.AgentID,
		Name:         agent.Name,
		Alias:        agentAlias(agent),
		Type:         agent.AgentType,
		Status:       agent.Status,
		NodeID:       agent.NodeID,
		Description:  agent.Description,
		LastActiveAt: &lastActive,
		IsSelf:       agent.AgentID == fromAgentID,
	}
}
