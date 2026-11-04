package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryNodeCoverage(requests []domain.PublishSummaryRequest) []domain.MessageVersionRef {
	seen := map[string]bool{}
	var refs []domain.MessageVersionRef
	for _, request := range requests {
		for _, ref := range request.Coverage {
			if !seen[ref.Identity()] {
				seen[ref.Identity()] = true
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func (s *MemoryStore) validateMemoryNodeSourcesCompleteLocked(
	owner string,
	requests []domain.PublishSummaryRequest,
) error {
	job := domain.SecretaryJob{}
	for _, ref := range secretaryNodeCoverage(requests) {
		if _, exists := s.secretaryLocked().snapshots[ref.Identity()]; exists {
			continue
		}
		current, err := s.readSearchMessageLocked(owner, ref.MessageID)
		if err != nil {
			return err
		}
		job.Messages = append(job.Messages, current)
	}
	if !s.secretarySourcesCompleteLocked(job) {
		return domain.ErrSecretarySourceIncomplete
	}
	return nil
}

func validateSecretaryNodeSourcesCompleteTx(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	requests []domain.PublishSummaryRequest,
) error {
	refs := secretaryNodeCoverage(requests)
	if len(refs) == 0 {
		return nil
	}
	raw, _ := json.Marshal(refs)
	rows, err := tx.QueryContext(ctx, `SELECT ref.message_id FROM jsonb_to_recordset($1::jsonb)
 AS ref(message_id text,content_hash text,canonicalization_version text) WHERE NOT EXISTS(
 SELECT 1 FROM message_snapshots snapshot WHERE snapshot.message_id=ref.message_id
 AND snapshot.content_hash=ref.content_hash AND snapshot.canonicalization_version=ref.canonicalization_version)`, string(raw))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	groups := map[string]domain.SecretaryJob{}
	for _, id := range ids {
		current, readErr := loadSearchMessage(ctx, tx, id)
		if readErr != nil {
			return readErr
		}
		if current.OwnerUserID != owner {
			return ErrNotFound
		}
		key := current.AgentID + "\x00" + current.SessionID
		group := groups[key]
		group.SourceAgentID, group.SourceSessionID = current.AgentID, current.SessionID
		group.Messages = append(group.Messages, current)
		groups[key] = group
	}
	for _, group := range groups {
		complete, checkErr := secretarySourcesCompleteTx(ctx, tx, group)
		if checkErr != nil {
			return checkErr
		}
		if !complete {
			return domain.ErrSecretarySourceIncomplete
		}
	}
	return nil
}
