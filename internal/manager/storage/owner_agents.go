package storage

import (
	"context"
	"sort"
)

// OwnerAgentFilter narrows an owner-scoped agent inventory. All fields are
// optional; the zero value returns every agent the owner owns. Filter
// semantics beyond owner scoping are applied by ListOwnerAgents.
type OwnerAgentFilter struct {
	// Query is a free-text term matched against name, alias, and description.
	Query string
	// Status filters by effective reachability: "online", "offline", or "any".
	// Empty is treated as "online".
	Status string
	// OrderBy is "relevance", "last_active", or "name". Empty resolves to
	// "relevance" when Query is set and "last_active" otherwise.
	OrderBy string
	// Limit caps the number of returned agents. Zero means the default cap.
	Limit int
}

// ListOwnerAgents returns the agents owned by ownerUserID across all of the
// owner's nodes. Unlike ListAgents it is strictly owner-scoped and does not
// include team-shared agents: this is the "my agents" inventory.
func (s *PostgresStore) ListOwnerAgents(
	ctx context.Context,
	ownerUserID string,
	filter OwnerAgentFilter,
) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+agentSelectColumns+`
		FROM agents
		WHERE deleted_at IS NULL
			AND owner_user_id = $1
		ORDER BY registered_at ASC
	`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAgents(rows)
}

// ListOwnerAgents mirrors the Postgres implementation for in-memory storage.
func (s *MemoryStore) ListOwnerAgents(
	ctx context.Context,
	ownerUserID string,
	filter OwnerAgentFilter,
) ([]Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Agent, 0)
	for _, agent := range s.agents {
		if agent.OwnerUserID != ownerUserID {
			continue
		}
		out = append(out, agent)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RegisteredAt.Before(out[j].RegisteredAt)
	})
	return out, nil
}
