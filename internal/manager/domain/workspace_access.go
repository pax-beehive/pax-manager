package domain

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

// WorkspaceSearchAccess limits searches authorized for one business turn.
type WorkspaceSearchAccess struct {
	Queries    []string `json:"queries,omitempty"`
	AgentIDs   []string `json:"agent_ids,omitempty"`
	SessionIDs []string `json:"session_ids,omitempty"`
}

// WorkspaceSearchScope is mandatory on ordinary agent searches.
type WorkspaceSearchScope struct {
	AgentIDs   []string `json:"agent_ids"`
	SessionIDs []string `json:"session_ids"`
}

// NormalizeWorkspaceSearchAccess preserves nil to distinguish absent preauthorization.
func NormalizeWorkspaceSearchAccess(scope *WorkspaceSearchAccess) (*WorkspaceSearchAccess, error) {
	if scope == nil {
		return nil, nil
	}
	queries, err := normalizeWorkspaceAccessValues(scope.Queries, true)
	if err != nil {
		return nil, err
	}
	sessions, err := normalizeWorkspaceAccessValues(scope.SessionIDs, false)
	if err != nil {
		return nil, err
	}
	agents, err := normalizeWorkspaceAccessValues(scope.AgentIDs, false)
	if err != nil {
		return nil, err
	}
	return &WorkspaceSearchAccess{Queries: queries, AgentIDs: agents, SessionIDs: sessions}, nil
}

func NormalizeWorkspaceSearchScope(
	scope *WorkspaceSearchScope,
	agentID string,
) (WorkspaceSearchScope, error) {
	if scope == nil || len(scope.AgentIDs) == 0 || len(scope.SessionIDs) == 0 {
		return WorkspaceSearchScope{}, errors.New(
			"explicit scope is required: use agent_ids [self] and session_ids [*] to search this agent's sessions",
		)
	}
	agents, err := normalizeWorkspaceAccessValues(scope.AgentIDs, false)
	if err != nil {
		return WorkspaceSearchScope{}, err
	}
	sessions, err := normalizeWorkspaceAccessValues(scope.SessionIDs, false)
	if err != nil {
		return WorkspaceSearchScope{}, err
	}
	return WorkspaceSearchScope{
		AgentIDs:   ResolveWorkspaceSearchAgents(agents, agentID),
		SessionIDs: sessions,
	}, nil
}

func ResolveWorkspaceSearchAgents(values []string, agentID string) []string {
	resolved := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "self" {
			value = agentID
		}
		if !seen[value] {
			seen[value] = true
			resolved = append(resolved, value)
		}
	}
	sort.Strings(resolved)
	return resolved
}

func (scope WorkspaceSearchAccess) Covers(request WorkspaceSearchScope) bool {
	return workspaceScopeContains(scope.AgentIDs, request.AgentIDs) &&
		workspaceScopeContains(scope.SessionIDs, request.SessionIDs)
}

func workspaceScopeContains(allowed, requested []string) bool {
	if len(allowed) == 1 && allowed[0] == "*" {
		return true
	}
	for _, value := range requested {
		found := false
		for _, candidate := range allowed {
			if candidate == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(requested) > 0
}

func normalizeWorkspaceAccessValues(values []string, queries bool) ([]string, error) {
	if values == nil {
		return []string{"*"}, nil
	}
	if len(values) == 0 || len(values) > 50 {
		return nil, errors.New("workspace search access requires 1 to 50 scope values")
	}
	unique := make(map[string]bool, len(values))
	maximum := 4096
	if queries {
		maximum = 256
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if queries {
			value = NormalizeWorkspaceSearchQuery(value)
		}
		if value == "" || utf8.RuneCountInString(value) > maximum ||
			(value == "*" && len(values) != 1) {
			return nil, errors.New("invalid workspace search access scope")
		}
		if queries && len(SearchTerms(value)) > MaxSearchTerms {
			return nil, errors.New("workspace search access query has too many terms")
		}
		unique[value] = true
	}
	normalized := make([]string, 0, len(unique))
	for value := range unique {
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized, nil
}

// NormalizeWorkspaceSearchQuery compares the complete literal query, not one matching term.
func NormalizeWorkspaceSearchQuery(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}

func (scope WorkspaceSearchAccess) AllowsQuery(query string) bool {
	query = NormalizeWorkspaceSearchQuery(query)
	for _, allowed := range scope.Queries {
		if allowed == "*" || allowed == query {
			return query != ""
		}
	}
	return false
}
