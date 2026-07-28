package storage

import (
	"context"
	"sort"
	"strings"
	"time"
)

// OwnerAgentFilter narrows an owner-scoped agent inventory. All fields are
// optional; the zero value returns every agent the owner owns. Filter
// semantics beyond owner scoping are applied by ListOwnerAgents.
type OwnerAgentFilter struct {
	// Query is a free-text term matched against name, alias, and description.
	Query string
	// Status filters by effective reachability: "online" or "offline". Empty
	// (or "any") does not filter by status; the "online" product default is
	// applied at the service layer, not here.
	Status string
	// OrderBy is "relevance", "last_active", or "name". Empty resolves to
	// "relevance" when Query is set and "last_active" otherwise.
	OrderBy string
	// Limit caps the number of returned agents. Zero means no cap at the store
	// layer; the service applies the product default and hard cap.
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
	`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	agents, err := scanAgents(rows)
	if err != nil {
		return nil, err
	}
	return filterOwnerAgents(agents, filter), nil
}

// ListOwnerAgents mirrors the Postgres implementation for in-memory storage.
func (s *MemoryStore) ListOwnerAgents(
	ctx context.Context,
	ownerUserID string,
	filter OwnerAgentFilter,
) ([]Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agents := make([]Agent, 0)
	for _, agent := range s.agents {
		if agent.OwnerUserID != ownerUserID {
			continue
		}
		agents = append(agents, agent)
	}
	return filterOwnerAgents(agents, filter), nil
}

// filterOwnerAgents applies status/query filtering, ordering, and limiting to a
// pre-scoped slice of owner agents. It is the single source of truth shared by
// both store implementations so behavior cannot drift between them.
func filterOwnerAgents(agents []Agent, filter OwnerAgentFilter) []Agent {
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	status := strings.ToLower(strings.TrimSpace(filter.Status))

	filtered := make([]Agent, 0, len(agents))
	for _, agent := range agents {
		if !ownerAgentMatchesStatus(agent, status) {
			continue
		}
		if query != "" && ownerAgentRelevance(agent, query) == 0 {
			continue
		}
		filtered = append(filtered, agent)
	}

	orderBy := strings.ToLower(strings.TrimSpace(filter.OrderBy))
	if orderBy == "" {
		if query != "" {
			orderBy = "relevance"
		} else {
			orderBy = "last_active"
		}
	}
	sortOwnerAgents(filtered, orderBy, query)

	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}
	return filtered
}

func ownerAgentMatchesStatus(agent Agent, status string) bool {
	switch status {
	case "online":
		return agentIsOnline(agent)
	case "offline":
		return !agentIsOnline(agent)
	default: // "", "any", or unrecognized: no status filtering
		return true
	}
}

func agentIsOnline(agent Agent) bool {
	return strings.EqualFold(strings.TrimSpace(agent.Status), "online")
}

// ownerAgentRelevance scores an agent against a lowercased query term. Zero
// means no match. Name matches outrank description matches, and exact/prefix
// matches outrank substring matches.
func ownerAgentRelevance(agent Agent, query string) int {
	name := strings.ToLower(strings.TrimSpace(agent.Name))
	switch {
	case name == query:
		return 100
	case strings.HasPrefix(name, query):
		return 80
	case strings.Contains(name, query):
		return 60
	}
	if strings.Contains(strings.ToLower(agent.Description), query) {
		return 20
	}
	return 0
}

func ownerAgentLastActive(agent Agent) time.Time {
	if agent.LastHeartbeat != nil {
		return *agent.LastHeartbeat
	}
	return agent.RegisteredAt
}

func sortOwnerAgents(agents []Agent, orderBy string, query string) {
	switch orderBy {
	case "name":
		sort.SliceStable(agents, func(i, j int) bool {
			ni := strings.ToLower(agents[i].Name)
			nj := strings.ToLower(agents[j].Name)
			if ni != nj {
				return ni < nj
			}
			return agents[i].RegisteredAt.Before(agents[j].RegisteredAt)
		})
	case "relevance":
		sort.SliceStable(agents, func(i, j int) bool {
			ri := ownerAgentRelevance(agents[i], query)
			rj := ownerAgentRelevance(agents[j], query)
			if ri != rj {
				return ri > rj
			}
			return ownerAgentLastActive(agents[i]).After(ownerAgentLastActive(agents[j]))
		})
	default: // last_active
		sort.SliceStable(agents, func(i, j int) bool {
			li := ownerAgentLastActive(agents[i])
			lj := ownerAgentLastActive(agents[j])
			if !li.Equal(lj) {
				return li.After(lj)
			}
			return agents[i].RegisteredAt.Before(agents[j].RegisteredAt)
		})
	}
}
