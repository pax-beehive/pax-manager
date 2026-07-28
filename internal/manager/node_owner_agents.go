package manager

import (
	"context"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

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
