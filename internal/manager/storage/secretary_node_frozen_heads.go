package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Only the persisted input directory is a current-head baseline. Later exact
// historical reads remain valid references even when their current head moves.
func secretaryFrozenSummaryHeads(raw []byte, owner string) ([]domain.SummaryRef, error) {
	var input struct{ CurrentSummaries []domain.SummaryVersion }
	if json.Unmarshal(raw, &input) != nil {
		return nil, domain.ErrSecretaryFenced
	}
	seen := map[string]int64{}
	for _, summary := range input.CurrentSummaries {
		if summary.OwnerUserID != owner || summary.SummaryID == "" || summary.Revision <= 0 {
			return nil, domain.ErrSecretaryFenced
		}
		if revision, ok := seen[summary.SummaryID]; ok && revision != summary.Revision {
			return nil, domain.ErrSummaryVersionMismatch
		}
		seen[summary.SummaryID] = summary.Revision
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	heads := make([]domain.SummaryRef, 0, len(ids))
	for _, id := range ids {
		heads = append(heads, domain.SummaryRef{SummaryID: id, Revision: seen[id]})
	}
	return heads, nil
}

func lockSecretaryFrozenHeadsTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	requests []domain.PublishSummaryRequest,
) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT input_json FROM secretary_batches WHERE batch_id=$1 AND owner_user_id=$2 AND source_id=$3`, job.BatchID, job.OwnerUserID, job.SourceID).
		Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrSecretaryFenced
	}
	if err != nil {
		return err
	}
	heads, err := secretaryFrozenSummaryHeads(raw, job.OwnerUserID)
	if err != nil {
		return err
	}
	locks := append([]domain.PublishSummaryRequest(nil), requests...)
	for _, head := range heads {
		locks = append(locks, domain.PublishSummaryRequest{SummaryID: head.SummaryID})
	}
	// Use one sorted lock set for both outputs and baseline heads.
	if err = lockSecretaryNodeIDsTx(ctx, tx, locks); err != nil {
		return err
	}
	for _, head := range heads {
		var revision int64
		err = tx.QueryRowContext(ctx, `SELECT current_revision FROM knowledge_nodes WHERE id=$1 AND owner_user_id=$2 FOR SHARE`, head.SummaryID, job.OwnerUserID).
			Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if revision != head.Revision {
			return domain.ErrSummaryVersionMismatch
		}
		if _, err = getSummaryTx(ctx, tx, job.OwnerUserID, head.SummaryID, head.Revision); err != nil {
			return err
		}
	}
	return nil
}

func (s *MemoryStore) validateMemorySecretaryFrozenHeadsLocked(job domain.SecretaryJob) error {
	batch := s.secretaryExecutionsLocked()[job.BatchID]
	if batch == nil || batch.owner != job.OwnerUserID || batch.source != job.SourceID {
		return domain.ErrSecretaryFenced
	}
	heads, err := secretaryFrozenSummaryHeads(batch.input, job.OwnerUserID)
	if err != nil {
		return err
	}
	for _, head := range heads {
		node, ok := s.secretaryLocked().nodes[head.SummaryID]
		if !ok || node.OwnerUserID != job.OwnerUserID {
			return ErrNotFound
		}
		if node.CurrentRevision != head.Revision {
			return domain.ErrSummaryVersionMismatch
		}
		if _, err = s.getMemorySummaryLocked(job.OwnerUserID, head.SummaryID, head.Revision, map[domain.SummaryRef]bool{}); err != nil {
			return err
		}
	}
	return nil
}
