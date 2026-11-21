package hosting

import "slices"

func validSandboxModelIDs(ids []string) bool {
	if len(ids) == 0 || len(ids) > maxAdminModels {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if len(id) > 128 || !modelPolicyIdentifier.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (g *sandboxModelGrant) markReady(parent sandboxModelParent, request *Update) error {
	if g.AgentID != request.AgentID || parent.NodeID != request.NodeID {
		return ErrUnauthorized
	}
	if request.ModelIDs != nil {
		if g.Harness != "codex" {
			return ErrConflict
		}
		g.ModelIDs = slices.Clone(request.ModelIDs)
	}
	g.State = "ready"
	if g.Permission != nil {
		g.Permission.Provisioned = true
	}
	return nil
}

func (s *Service) enabledSandboxModelIDs(state *State, reported []string) *[]string {
	ids := make([]string, 0, len(reported))
	for _, id := range reported {
		policy, exists := s.effectiveModelPolicy(state, id)
		if (exists && policy.Enabled && validModelPolicy(policy)) ||
			(!exists && id == s.Config.Model && s.legacyPolicyAllowed(state, id)) {
			ids = append(ids, id)
		}
	}
	return &ids
}
