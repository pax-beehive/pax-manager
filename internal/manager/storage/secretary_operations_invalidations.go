package storage

import (
	"context"
	"database/sql"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) acknowledgeMemoryOperationInvalidationsLocked(job *memorySecretaryJob,
	operation domain.SecretaryOperation,
) {
	selected := map[string]bool{}
	for _, target := range operation.Targets {
		selected[target.MessageID] = true
	}
	for key, pending := range s.secretaryLocked().invalidations {
		node := s.secretaryLocked().nodes[pending.ref.SummaryID]
		if pending.source == job.work.SourceID && node.OwnerUserID == job.work.OwnerUserID &&
			selected[pending.message] && pending.changeRevision <= job.desired {
			pending.acknowledgedOperationID = operation.OperationID
			s.secretaryLocked().invalidations[key] = pending
		}
	}
}

func acknowledgeSecretaryOperationInvalidationsTx(ctx context.Context, tx *sql.Tx,
	job domain.SecretaryJob, operation domain.SecretaryOperation,
) error {
	if len(operation.Targets) == 0 {
		return nil
	}
	ids := make([]string, 0, len(operation.Targets))
	for _, target := range operation.Targets {
		ids = append(ids, target.MessageID)
	}
	_, err := tx.ExecContext(
		ctx,
		`UPDATE knowledge_node_invalidations i SET acknowledged_operation_id=$4
 FROM knowledge_nodes n,secretary_jobs j WHERE i.node_id=n.id AND n.owner_user_id=$1
 AND i.origin_source_id=$2 AND i.message_id=ANY($3) AND j.source_id::text=$2
 AND j.owner_user_id=$1 AND i.change_revision<=j.desired_revision`,
		job.OwnerUserID,
		job.SourceID,
		ids,
		operation.OperationID,
	)
	return err
}
