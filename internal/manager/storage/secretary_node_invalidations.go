package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryNodeInvalidation struct {
	ref            domain.SummaryRef
	messageID      string
	changeRevision int64
}

func loadSecretaryInvalidationsTx(
	ctx context.Context,
	tx *sql.Tx,
	job *domain.SecretaryJob,
) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT n.id,n.current_revision,i.message_id,i.change_revision
 FROM knowledge_node_invalidations i JOIN knowledge_nodes n ON n.id=i.node_id
 WHERE n.owner_user_id=$1 AND i.origin_source_id=$2 AND i.acknowledged_operation_id=''
 ORDER BY n.id,i.message_id`, job.OwnerUserID, job.SourceID)
	if err != nil {
		return nil, err
	}
	var pending []secretaryNodeInvalidation
	for rows.Next() {
		var item secretaryNodeInvalidation
		if err = rows.Scan(&item.ref.SummaryID, &item.ref.Revision, &item.messageID, &item.changeRevision); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pending = append(pending, item)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	invalidated := map[string]bool{}
	var fingerprints []string
	for _, item := range pending {
		changed, fingerprint, checkErr := secretaryInvalidationChangedTx(
			ctx,
			tx,
			job.OwnerUserID,
			item,
		)
		if checkErr != nil {
			return nil, checkErr
		}
		if changed {
			secretaryAddInvalidated(job, item.ref)
			invalidated[item.ref.SummaryID] = true
			job.ResetRequired, job.ResetReason = true, "source_content_changed"
			fingerprints = append(
				fingerprints,
				item.ref.SummaryID+":"+item.messageID+":"+fingerprint,
			)
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge_node_invalidations WHERE node_id=$1 AND message_id=$2 AND change_revision=$3`,
			item.ref.SummaryID, item.messageID, item.changeRevision); err != nil {
			return nil, err
		}
		if err = restoreKnowledgeNodeStateTx(ctx, tx, item.ref.SummaryID); err != nil {
			return nil, err
		}
	}
	job.InvalidationKey = secretaryInvalidationKey(fingerprints)
	return invalidated, nil
}

func restoreKnowledgeNodeStateTx(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE knowledge_nodes n SET status=CASE WHEN EXISTS(
 SELECT 1 FROM knowledge_node_invalidations i WHERE i.node_id=n.id AND i.target_revision=n.current_revision)
 THEN 'stale' WHEN v.body=$2 AND v.title='Unavailable summary' THEN 'empty' ELSE 'active' END
 FROM summary_versions v WHERE n.id=$1 AND v.summary_id=n.id AND v.revision=n.current_revision`, id, domain.SecretaryEmptySummaryBody)
	return err
}

func secretaryInvalidationChangedTx(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	item secretaryNodeInvalidation,
) (bool, string, error) {
	current, err := loadSearchMessage(ctx, tx, item.messageID)
	if errors.Is(err, ErrNotFound) {
		dependent, dependencyErr := secretaryInvalidationDependsOnTx(ctx, tx, item)
		return dependent, "unavailable", dependencyErr
	}
	if err != nil {
		return false, "", err
	}
	if current.OwnerUserID != owner {
		dependent, dependencyErr := secretaryInvalidationDependsOnTx(ctx, tx, item)
		return dependent, "unavailable", dependencyErr
	}
	var changed bool
	err = tx.QueryRowContext(
		ctx,
		`WITH RECURSIVE dependencies(summary_id,revision) AS (
 SELECT $1::text,$2::bigint UNION SELECT src.child_summary_id,src.child_revision
 FROM summary_sources src JOIN dependencies d USING(summary_id,revision) WHERE src.source_kind='summary'
) SELECT EXISTS(SELECT 1 FROM summary_sources src JOIN dependencies d USING(summary_id,revision)
 WHERE src.message_id=$3 AND (src.content_hash<>$4 OR src.canonicalization_version<>$5))`,
		item.ref.SummaryID,
		item.ref.Revision,
		item.messageID,
		current.ContentHash,
		current.CanonicalizationVersion,
	).Scan(&changed)
	return changed, current.ContentHash + ":" + current.CanonicalizationVersion, err
}

func secretaryInvalidationDependsOnTx(
	ctx context.Context,
	tx *sql.Tx,
	item secretaryNodeInvalidation,
) (bool, error) {
	var dependent bool
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE dependencies(summary_id,revision) AS (
 SELECT $1::text,$2::bigint UNION SELECT src.child_summary_id,src.child_revision
 FROM summary_sources src JOIN dependencies d USING(summary_id,revision) WHERE src.source_kind='summary'
) SELECT EXISTS(SELECT 1 FROM summary_sources src JOIN dependencies d USING(summary_id,revision)
 WHERE src.message_id=$3)`, item.ref.SummaryID, item.ref.Revision, item.messageID).Scan(&dependent)
	return dependent, err
}

func clearSecretaryInvalidationsTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	results []domain.SummaryVersion,
) error {
	for _, result := range results {
		if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_node_invalidations WHERE node_id=$1 AND origin_source_id=$2 AND change_revision<=$3`,
			result.SummaryID, job.SourceID, job.ThroughRevision); err != nil {
			return err
		}
	}
	return nil
}
