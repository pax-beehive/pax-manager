package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func loadSecretaryRollingBaseline(ctx context.Context, tx *sql.Tx, job *domain.SecretaryJob) error {
	job.TargetSummaryID = secretarySummaryID(job.OwnerUserID, job.SourceID)
	var roots []byte
	var processed, reset int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(s.custom_session_name,''),s.session_name,''),
 COALESCE(s.primary_project_id,''),COALESCE(s.workspace_roots,'[]'::jsonb),
 COALESCE(s.metadata #>> '{pax_config,cwd}',s.metadata->>'initial_cwd',''),
 j.processed_revision,j.reset_revision FROM agent_sessions s JOIN secretary_jobs j ON j.source_id=s.id
 WHERE s.id=$1`, job.SourceID).
		Scan(&job.SourceMetadata.Title, &job.SourceMetadata.PrimaryProjectID,
			&roots, &job.SourceMetadata.CWD, &processed, &reset)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(roots, &job.SourceMetadata.WorkspaceRoots); err != nil {
		return err
	}
	current, err := scanSummary(tx.QueryRowContext(ctx, summarySelectSQL+`
 WHERE summary_id=$1 AND owner_user_id=$2 ORDER BY revision DESC LIMIT 1`, job.TargetSummaryID, job.OwnerUserID))
	marker := int64(-1)
	if errors.Is(err, ErrNotFound) {
		if processed == 0 {
			return nil
		}
		job.ResetReason = "rolling_summary_rebuild"
	} else if err != nil {
		return err
	} else {
		job.ExpectedRevision = current.Revision
		if err = loadSummarySources(ctx, tx, &current); err != nil {
			return err
		}
		err = validateSummaryAccess(ctx, tx, job.OwnerUserID, current.SummaryID, current.Revision)
		if err == nil {
			job.CurrentSummary = &current
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		marker = current.Revision
		job.ResetReason = "source_visibility_changed"
		for _, ref := range current.Coverage {
			if _, err = loadSearchMessage(ctx, tx, ref.MessageID); errors.Is(err, ErrNotFound) {
				job.RemovedMessageIDs = append(job.RemovedMessageIDs, ref.MessageID)
			} else if err != nil {
				return err
			}
		}
	}
	job.ResetRequired = true
	if reset == marker {
		return nil
	}
	return requeueSecretaryReset(ctx, tx, job, marker)
}

func requeueSecretaryReset(
	ctx context.Context,
	tx *sql.Tx,
	job *domain.SecretaryJob,
	marker int64,
) error {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT message_id FROM messages WHERE agent_id=$1 AND session_id=$2 ORDER BY session_seq,id`,
		job.SourceAgentID,
		job.SourceSessionID,
	)
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
	var visible []string
	for _, id := range ids {
		doc, readErr := loadSearchMessage(ctx, tx, id)
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		if doc.OwnerUserID != job.OwnerUserID {
			return ErrNotFound
		}
		visible = append(visible, id)
	}
	err = tx.QueryRowContext(ctx, `UPDATE secretary_jobs SET reset_revision=$2,desired_revision=desired_revision+$3
 WHERE source_id=$1 RETURNING desired_revision`, job.SourceID, marker, len(visible)).
		Scan(&job.ThroughRevision)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE secretary_message_state SET processed_hash='',processed_canonicalization_version='',processed_owner_user_id='' WHERE source_id=$1`, job.SourceID); err != nil {
		return err
	}
	for i, id := range visible {
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO secretary_message_state(source_id,message_id,change_revision) VALUES($1,$2,$3)
 ON CONFLICT(source_id,message_id) DO UPDATE SET change_revision=EXCLUDED.change_revision`,
			job.SourceID,
			id,
			job.ThroughRevision-int64(len(visible))+int64(i)+1,
		)
		if err != nil {
			return err
		}
	}
	return nil
}
