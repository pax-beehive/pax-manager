package storage

import (
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) memorySecretaryUncoveredChangeHiddenLocked(
	job *memorySecretaryJob,
	id string,
) bool {
	if job.checkpoint[id] != "" {
		return false
	}
	message := s.messages[id]
	if domain.SearchVisibleMessageMetadata(message) {
		switch message.Status {
		case "streaming", "running", "pending", "in_progress":
			return false
		}
	}
	_, err := s.readSearchMessageLocked(job.work.OwnerUserID, id)
	return errors.Is(err, ErrNotFound)
}

// Ignore never-covered tool rows without hashing or loading session history.
// Previously covered removals and incomplete visible messages remain holes.
var secretaryCursorAdvanceSQL = `UPDATE secretary_jobs j SET processed_revision=GREATEST(processed_revision,
 COALESCE((SELECT min(pending.change_revision)-1 FROM (
 SELECT state.change_revision FROM secretary_message_state state WHERE state.source_id=j.source_id
 AND state.change_revision>j.processed_revision AND (state.processed_owner_user_id<>j.owner_user_id
 OR state.processed_change_revision<state.change_revision)
 AND ((state.processed_owner_user_id=j.owner_user_id AND state.processed_hash<>'') OR EXISTS (
 SELECT 1` + searchMessageVisibilityFromSQL + ` WHERE m.message_id=state.message_id
 AND a.owner_user_id=j.owner_user_id AND m.agent_id=j.source_agent_id AND m.session_id=j.source_session_id
 AND m.role IN ('user','assistant') AND COALESCE(m.message_type,'') IN (` + secretaryPendingKindsSQL() + `)` +
	searchMessageVisibilityConditionsSQL + ` AND (m.status IN ('streaming','running','pending','in_progress')
 OR (NOT EXISTS(SELECT 1 FROM message_parts invalid WHERE invalid.message_id=m.message_id AND invalid.part_index<0)
 AND EXISTS(SELECT 1 FROM message_parts part WHERE part.message_id=m.message_id AND part.part_type='text'
 AND part.part_index>=0 AND btrim(COALESCE(part.text,''),$2)<>'')))))
 UNION ALL SELECT i.change_revision FROM knowledge_node_invalidations i JOIN knowledge_nodes n ON n.id=i.node_id
 WHERE i.origin_source_id=j.source_id::text AND n.owner_user_id=j.owner_user_id
 AND i.acknowledged_operation_id='' AND i.change_revision>j.processed_revision
 ) pending),desired_revision)),completed_at=NOW() WHERE source_id=$1`
