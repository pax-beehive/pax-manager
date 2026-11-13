package storage

import (
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) secretaryContentPendingLocked(job *memorySecretaryJob, settings int64) bool {
	if !s.secretaryPendingSourceVisibleLocked(job) {
		return false
	}
	if s.secretaryOperationPendingLocked(job.work.OwnerUserID, job.work.SourceID) {
		return true
	}
	if job.desired <= job.processed {
		return false
	}
	for _, batch := range s.secretaryExecutionsLocked() {
		if batch.source == job.work.SourceID &&
			currentSecretaryStatusBatch(batch, job, job.work.OwnerUserID, settings) {
			return true
		}
	}
	for id, revision := range job.changes {
		if revision <= job.processed || job.processedChanges[id] >= revision {
			continue
		}
		current, err := s.readSearchMessageLocked(job.work.OwnerUserID, id)
		if err != nil || current.AgentID != job.work.SourceAgentID ||
			current.SessionID != job.work.SourceSessionID {
			if job.checkpoint[id] != "" {
				return true
			}
			continue
		}
		if current.ContentHash != job.checkpoint[id] {
			return true
		}
	}
	return s.secretaryResetPendingLocked(job)
}

func (s *MemoryStore) secretaryOperationPendingLocked(owner, source string) bool {
	return s.secretaryPendingOperationIDLocked(owner, source) != ""
}

func (s *MemoryStore) secretaryPendingOperationIDLocked(owner, source string) string {
	var latest *memorySecretaryOperation
	for _, operation := range s.secretaryOperationsLocked() {
		if operation.owner == owner && operation.value.SourceID == source &&
			(operation.value.Status == "queued" || operation.value.Status == "running" || operation.value.Status == "needs_attention") {
			if latest == nil || operation.value.UpdatedAt.After(latest.value.UpdatedAt) ||
				operation.value.UpdatedAt.Equal(latest.value.UpdatedAt) &&
					operation.value.OperationID > latest.value.OperationID {
				latest = operation
			}
		}
	}
	if latest != nil {
		return latest.value.OperationID
	}
	return ""
}

func (s *MemoryStore) secretaryPendingSourceVisibleLocked(job *memorySecretaryJob) bool {
	session, exists := s.sessions[sessionKey(job.work.SourceAgentID, job.work.SourceSessionID)]
	return exists && session.ArchivedAt == nil &&
		session.Source != domain.SecretaryInternalSource &&
		!strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) &&
		!s.secretaryLocked().internal[sessionKey(session.AgentID, session.SessionID)] &&
		s.secretaryAgentOwnedLocked(job.work.OwnerUserID, session.AgentID) &&
		s.searchNodesVisibleLocked(session.NodeID)
}

func (s *MemoryStore) secretaryResetPendingLocked(job *memorySecretaryJob) bool {
	for _, pending := range s.secretaryLocked().invalidations {
		if pending.source != job.work.SourceID || pending.acknowledgedOperationID != "" {
			continue
		}
		node := s.secretaryLocked().nodes[pending.ref.SummaryID]
		if node.OwnerUserID != job.work.OwnerUserID {
			continue
		}
		ref := domain.SummaryRef{SummaryID: pending.ref.SummaryID, Revision: node.CurrentRevision}
		for _, source := range s.memoryNodeSourceRefsLocked(ref) {
			if source.MessageID != pending.message {
				continue
			}
			current, err := s.readSearchMessageLocked(job.work.OwnerUserID, source.MessageID)
			if err != nil || current.ContentHash != source.ContentHash ||
				current.CanonicalizationVersion != source.CanonicalizationVersion {
				return true
			}
		}
	}
	return false
}
