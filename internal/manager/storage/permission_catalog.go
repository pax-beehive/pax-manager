package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func permissionProfileKey(profileID string, revision int64) string {
	return fmt.Sprintf("%s\x00%d", profileID, revision)
}

func permissionObservationKey(agentID, identityFingerprint string) string {
	return agentID + "\x00" + identityFingerprint
}

func (s *MemoryStore) seedPermissionProfiles() {
	profile := domain.BuiltInCodexPermissionProfile(s.now().UTC())
	s.permissionProfiles[permissionProfileKey(profile.ProfileID, profile.Revision)] = profile
}

func (s *MemoryStore) UpsertAgentRuntimeIdentity(
	_ context.Context,
	identity domain.AgentRuntimeIdentity,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertAgentRuntimeIdentityLocked(identity)
}

func (s *MemoryStore) upsertAgentRuntimeIdentityLocked(
	identity domain.AgentRuntimeIdentity,
) error {
	if _, ok := s.agents[identity.AgentID]; !ok {
		return ErrNotFound
	}
	current, ok := s.agentRuntimeIdentities[identity.AgentID]
	if ok && current.ReportEpoch == identity.ReportEpoch &&
		current.ConnectionID == identity.ConnectionID &&
		current.ReportGeneration > identity.ReportGeneration {
		return nil
	}
	s.agentRuntimeIdentities[identity.AgentID] = identity
	return nil
}

func (s *MemoryStore) GetAgentRuntimeIdentity(
	_ context.Context,
	agentID string,
) (domain.AgentRuntimeIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.agentRuntimeIdentities[agentID]
	if !ok {
		return domain.AgentRuntimeIdentity{}, ErrNotFound
	}
	return identity, nil
}

func (s *MemoryStore) InsertPermissionProfile(
	_ context.Context,
	profile domain.AgentPermissionProfile,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := permissionProfileKey(profile.ProfileID, profile.Revision)
	if _, ok := s.permissionProfiles[key]; ok {
		return ErrConflict
	}
	profile.Definition = domain.ClonePermissionProfileDefinition(profile.Definition)
	s.permissionProfiles[key] = profile
	return nil
}

func (s *MemoryStore) ListActivePermissionProfiles(
	_ context.Context,
	ownerUserID string,
	agentType string,
) ([]domain.AgentPermissionProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.AgentPermissionProfile, 0)
	for _, profile := range s.permissionProfiles {
		if profile.Status != "active" ||
			(profile.OwnerUserID != "" && profile.OwnerUserID != ownerUserID) ||
			(profile.AgentType != "" && !strings.EqualFold(profile.AgentType, agentType)) {
			continue
		}
		profile.Definition = domain.ClonePermissionProfileDefinition(profile.Definition)
		out = append(out, profile)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].OwnerUserID != "") != (out[j].OwnerUserID != "") {
			return out[i].OwnerUserID != ""
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		if out[i].ProfileID != out[j].ProfileID {
			return out[i].ProfileID < out[j].ProfileID
		}
		return out[i].Revision > out[j].Revision
	})
	return out, nil
}

func (s *MemoryStore) UpsertPermissionObservation(
	_ context.Context,
	observation domain.AgentPermissionObservation,
) (domain.AgentPermissionObservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.agents[observation.AgentID]; !ok {
		return domain.AgentPermissionObservation{}, ErrNotFound
	}
	key := permissionObservationKey(observation.AgentID, observation.IdentityFingerprint)
	current, ok := s.permissionObservations[key]
	if !ok {
		observation.CatalogRevision = 1
	} else if current.CatalogHash == observation.CatalogHash {
		observation.CatalogRevision = current.CatalogRevision
	} else {
		observation.CatalogRevision = current.CatalogRevision + 1
	}
	observation.Catalog = domain.CloneObservedPermissionCatalog(observation.Catalog)
	s.permissionObservations[key] = observation
	return observation, nil
}

func (s *MemoryStore) GetPermissionObservation(
	_ context.Context,
	agentID string,
	identityFingerprint string,
) (domain.AgentPermissionObservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	observation, ok := s.permissionObservations[permissionObservationKey(agentID, identityFingerprint)]
	if !ok {
		return domain.AgentPermissionObservation{}, ErrNotFound
	}
	observation.Catalog = domain.CloneObservedPermissionCatalog(observation.Catalog)
	return observation, nil
}
