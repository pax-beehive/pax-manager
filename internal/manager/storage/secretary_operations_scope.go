package storage

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryOperationBatchScope(job *domain.SecretaryJob, affected map[string][]string) {
	current := map[string]domain.SearchDocument{}
	for _, message := range job.Messages {
		current[message.SourceID] = message
	}
	var required []domain.SummaryRef
	var fingerprints []string
	for _, ref := range job.InvalidatedSummaries {
		inside := len(affected[ref.SummaryID]) > 0
		for _, id := range affected[ref.SummaryID] {
			if _, ok := current[id]; !ok {
				inside = false
			}
		}
		if !inside {
			continue
		}
		required = append(required, ref)
		for _, id := range affected[ref.SummaryID] {
			fingerprints = append(
				fingerprints,
				ref.SummaryID+":"+strconv.FormatInt(ref.Revision, 10)+
					":"+id+":"+current[id].ContentHash,
			)
		}
	}
	var removed []string
	for _, id := range job.RemovedMessageIDs {
		if _, ok := current[id]; ok {
			removed = append(removed, id)
		}
	}
	job.InvalidatedSummaries, job.RemovedMessageIDs = required, removed
	job.ResetRequired = len(required)+len(removed) > 0
	if !job.ResetRequired {
		job.ResetReason = ""
	}
	job.InvalidationKey = secretaryInvalidationKey(fingerprints)
}

func (s *MemoryStore) scopeMemoryOperationBatchLocked(job *domain.SecretaryJob) {
	affected := map[string][]string{}
	for _, pending := range s.secretaryLocked().invalidations {
		node := s.secretaryLocked().nodes[pending.ref.SummaryID]
		if pending.source == job.SourceID && node.OwnerUserID == job.OwnerUserID &&
			pending.acknowledgedOperationID == "" {
			affected[pending.ref.SummaryID] = append(
				affected[pending.ref.SummaryID],
				pending.message,
			)
		}
	}
	secretaryOperationBatchScope(job, affected)
}

func scopeSecretaryOperationBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	job *domain.SecretaryJob,
) error {
	affected := map[string][]string{}
	if len(job.InvalidatedSummaries) > 0 {
		rows, err := tx.QueryContext(
			ctx,
			`SELECT i.node_id,i.message_id FROM knowledge_node_invalidations i
 JOIN knowledge_nodes n ON n.id=i.node_id WHERE i.origin_source_id=$1 AND n.owner_user_id=$2
 AND i.acknowledged_operation_id=''`,
			job.SourceID,
			job.OwnerUserID,
		)
		if err != nil {
			return err
		}
		for rows.Next() {
			var node, message string
			if err = rows.Scan(&node, &message); err != nil {
				_ = rows.Close()
				return err
			}
			affected[node] = append(affected[node], message)
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
	}
	secretaryOperationBatchScope(job, affected)
	return nil
}
