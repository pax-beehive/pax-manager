package hosting

import (
	"fmt"
	"sort"
	"strings"
)

const maxAdminModels = 128

func (s *Service) legacyPolicyAllowed(state *State, id string) bool {
	_, registered := state.Admin.CreatedModels[id]
	return len(s.Config.Models) == 0 && !registered
}

func (s *Service) defaultModelOutputLimit(state *State) int64 {
	if policy, exists := s.effectiveModelPolicy(state, s.Config.Model); exists {
		return policy.MaxOutputTokens
	}
	return s.Config.MaxOutputTokens
}

func (s *Service) baseModelPolicy(state *State, id string) (ModelPolicy, bool) {
	if base, exists := s.Config.configuredModelPolicy(id); exists {
		return base, true
	}
	base, exists := state.Admin.CreatedModels[id]
	return base, exists && base.ID == id && validModelPolicy(base) &&
		validManagedModelIdentity(base.Provider, base.UpstreamModel)
}

func (s *Service) modelIDConflict(state *State, id string) bool {
	_, created := state.Admin.CreatedModels[id]
	_, configured := s.Config.configuredModelPolicy(id)
	return created && configured
}

func (s *Service) modelBases(state *State) []ModelPolicy {
	bases := append([]ModelPolicy(nil), s.Config.Models...)
	seen := make(map[string]bool, len(bases))
	for _, base := range bases {
		seen[base.ID] = true
	}
	ids := make([]string, 0, len(state.Admin.CreatedModels))
	for id := range state.Admin.CreatedModels {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if base, valid := s.baseModelPolicy(state, id); valid {
			bases = append(bases, base)
		}
	}
	return bases
}

func validManagedProvider(provider string) bool {
	switch provider {
	case "openrouter", "openai", "anthropic":
		return true
	default:
		return false
	}
}

func validManagedModelIdentity(provider, upstream string) bool {
	if !validManagedProvider(provider) || len(provider)+1+len(upstream) > 256 {
		return false
	}
	for _, segment := range strings.Split(upstream, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validAdminModelCreate(update *AdminUpdate) bool {
	if update.TargetID == "" || !validManagedModelIdentity(update.Provider, update.UpstreamModel) ||
		update.UpstreamModel == "" || strings.Contains(update.TargetID, "://") ||
		strings.Contains(update.UpstreamModel, "://") || update.MonthlyMicrousd != nil ||
		update.MonthlyTokens != nil || update.AccessPaused != nil ||
		(update.Enabled != nil && *update.Enabled) || update.Moderation == nil ||
		update.MaxOutputTokens == nil || update.InputPrice == nil || update.OutputPrice == nil {
		return false
	}
	return validModelPolicy(createdModelPolicy(update, "admin-1"))
}

func createdModelPolicy(update *AdminUpdate, version string) ModelPolicy {
	return ModelPolicy{
		ID: update.TargetID, Provider: update.Provider, UpstreamModel: update.UpstreamModel,
		Moderation: *update.Moderation, PolicyVersion: version,
		MaxOutputTokens: *update.MaxOutputTokens, InputPrice: *update.InputPrice,
		OutputPrice: *update.OutputPrice, Enabled: false,
	}
}

func (s *Service) applyAdminModelCreate(state *State, update *AdminUpdate) error {
	if _, exists := s.baseModelPolicy(state, update.TargetID); exists {
		return ErrAdminModelConflict
	}
	if _, exists := state.Admin.CreatedModels[update.TargetID]; exists ||
		(len(s.Config.Models) == 0 && s.Config.Model == update.TargetID) {
		return ErrAdminModelConflict
	}
	for _, policy := range s.modelBases(state) {
		if policy.Provider == update.Provider && policy.UpstreamModel == update.UpstreamModel {
			return ErrAdminModelConflict
		}
	}
	if len(s.Config.Models)+len(state.Admin.CreatedModels) >= maxAdminModels {
		return ErrAdminUpdate
	}
	version := fmt.Sprintf("admin-%d", state.Admin.Revision+1)
	state.Admin.CreatedModels[update.TargetID] = createdModelPolicy(update, version)
	return nil
}
