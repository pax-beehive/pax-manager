package storage

import (
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type memoryNodeInvalidation struct {
	ref                          domain.SummaryRef
	source, message, fingerprint string
	changeRevision               int64
	acknowledgedOperationID      string
}

func (s *MemoryStore) refreshMemoryNodeInvalidationsLocked() {
	state := s.secretaryLocked()
	if state.invalidations == nil {
		state.invalidations = map[string]memoryNodeInvalidation{}
	}
	for id, node := range state.nodes {
		job := state.jobs[node.OriginSourceID]
		if node.OriginSourceID == "" {
			continue
		}
		ref := domain.SummaryRef{SummaryID: id, Revision: node.CurrentRevision}
		for _, source := range s.memoryNodeSourceRefsLocked(ref) {
			fingerprint, changed := s.memoryNodeSourceFingerprintLocked(node.OwnerUserID, source)
			key := id + "\x00" + source.MessageID
			if !changed {
				continue
			}
			if previous, exists := state.invalidations[key]; exists &&
				previous.fingerprint == fingerprint &&
				previous.ref == ref {
				continue
			}
			changeRevision := int64(0)
			if job != nil && job.work.OwnerUserID == node.OwnerUserID {
				job.desired++
				job.updated, job.available = s.now(), s.now()
				job.executionBlocked = false
				changeRevision = job.desired
			}
			node.Status = "stale"
			state.nodes[id] = node
			state.invalidations[key] = memoryNodeInvalidation{ref: ref, source: node.OriginSourceID,
				message: source.MessageID, fingerprint: fingerprint, changeRevision: changeRevision}
		}
	}
}

func (s *MemoryStore) memoryNodeSourceFingerprintLocked(
	owner string,
	ref domain.MessageVersionRef,
) (string, bool) {
	message, exists := s.messages[ref.MessageID]
	if exists {
		session := s.sessions[sessionKey(message.AgentID, message.SessionID)]
		source := s.secretaryLocked().jobs[secretarySourceID(session.ID)]
		if source != nil && source.work.OwnerUserID == owner && session.ArchivedAt == nil &&
			s.secretaryAgentOwnedLocked(
				owner,
				message.AgentID,
			) && s.searchNodesVisibleLocked(session.NodeID) {
			if hash := source.seen[ref.MessageID]; hash != "" && hash != "hidden" {
				return hash + ":" + domain.SearchCanonicalizationVersion, hash != ref.ContentHash ||
					ref.CanonicalizationVersion != domain.SearchCanonicalizationVersion
			}
		}
	}
	current, err := s.readSearchMessageLocked(owner, ref.MessageID)
	if err != nil {
		return "unavailable", true
	}
	return current.ContentHash + ":" + current.CanonicalizationVersion, current.ContentHash != ref.ContentHash ||
		current.CanonicalizationVersion != ref.CanonicalizationVersion
}

func (s *MemoryStore) memoryNodeSourceRefsLocked(
	root domain.SummaryRef,
) []domain.MessageVersionRef {
	pending, seen := []domain.SummaryRef{root}, map[domain.SummaryRef]bool{}
	refs := map[string]domain.MessageVersionRef{}
	for len(pending) > 0 && len(seen) <= secretaryCoverageLimit {
		ref := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		versions := s.secretaryLocked().summaries[ref.SummaryID]
		if ref.Revision <= 0 || ref.Revision > int64(len(versions)) {
			continue
		}
		version := versions[ref.Revision-1]
		for _, source := range version.Coverage {
			refs[source.Identity()] = source
		}
		pending = append(pending, version.SummarySources...)
	}
	result := make([]domain.MessageVersionRef, 0, len(refs))
	for _, ref := range refs {
		result = append(result, ref)
	}
	return result
}

func (s *MemoryStore) loadMemoryNodeInvalidationsLocked(job *domain.SecretaryJob) map[string]bool {
	invalidated := map[string]bool{}
	var fingerprints []string
	for key, pending := range s.secretaryLocked().invalidations {
		if pending.source != job.SourceID {
			continue
		}
		if node, exists := s.secretaryLocked().nodes[pending.ref.SummaryID]; exists {
			pending.ref.Revision = node.CurrentRevision
		}
		if pending.acknowledgedOperationID != "" {
			invalidated[pending.ref.SummaryID] = true
			continue
		}
		changed := false
		for _, ref := range s.memoryNodeSourceRefsLocked(pending.ref) {
			if ref.MessageID == pending.message {
				_, different := s.memoryNodeSourceFingerprintLocked(job.OwnerUserID, ref)
				changed = changed || different
			}
		}
		if !changed {
			delete(s.secretaryLocked().invalidations, key)
			s.restoreMemoryKnowledgeNodeStateLocked(pending.ref.SummaryID)
			continue
		}
		secretaryAddInvalidated(job, pending.ref)
		invalidated[pending.ref.SummaryID] = true
		job.ResetRequired, job.ResetReason = true, "source_content_changed"
		fingerprints = append(
			fingerprints,
			pending.ref.SummaryID+":"+pending.message+":"+pending.fingerprint,
		)
	}
	job.InvalidationKey = secretaryInvalidationKey(fingerprints)
	return invalidated
}

func (s *MemoryStore) restoreMemoryKnowledgeNodeStateLocked(id string) {
	node, exists := s.secretaryLocked().nodes[id]
	if !exists {
		return
	}
	versions := s.secretaryLocked().summaries[id]
	if len(versions) == 0 {
		return
	}
	node.Status = summaryNodeMetadata(versions[len(versions)-1]).NodeStatus
	for _, pending := range s.secretaryLocked().invalidations {
		if pending.ref.SummaryID == id && pending.ref.Revision == node.CurrentRevision {
			node.Status = "stale"
		}
	}
	s.secretaryLocked().nodes[id] = node
}

func (s *MemoryStore) clearMemoryNodeInvalidationsLocked(
	job domain.SecretaryJob,
	results []domain.SummaryVersion,
) {
	updated := map[string]bool{}
	for _, result := range results {
		updated[result.SummaryID] = true
	}
	for key, pending := range s.secretaryLocked().invalidations {
		if pending.source == job.SourceID && pending.changeRevision <= job.ThroughRevision &&
			updated[pending.ref.SummaryID] {
			delete(s.secretaryLocked().invalidations, key)
		}
	}
}
