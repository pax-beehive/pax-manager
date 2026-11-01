package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) ClaimSecretaryJobs(
	ctx context.Context,
	limit int,
	lease, settle time.Duration,
) ([]domain.SecretaryJob, error) {
	if limit <= 0 || limit > 10 {
		limit = 1
	}
	if lease <= 0 || lease > 30*time.Minute {
		lease = 10 * time.Minute
	}
	if settle < 0 {
		settle = 30 * time.Second
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(
		ctx,
		`SELECT j.source_id::text,j.owner_user_id,j.source_agent_id,j.source_session_id,
 cfg.agent_id,cfg.revision,j.desired_revision,j.retry_generation FROM secretary_jobs j
 JOIN secretary_settings cfg ON cfg.owner_user_id=j.owner_user_id AND cfg.enabled
 JOIN agents a ON a.agent_id=j.source_agent_id AND a.owner_user_id=j.owner_user_id AND a.deleted_at IS NULL
 JOIN agents selected ON selected.agent_id=cfg.agent_id AND selected.owner_user_id=j.owner_user_id AND selected.deleted_at IS NULL
 JOIN agent_sessions s ON s.id=j.source_id AND s.archived_at IS NULL AND s.runtime_status='idle'
 LEFT JOIN nodes n ON n.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id LEFT JOIN nodes target ON target.node_id=selected.node_id
 WHERE j.desired_revision>j.processed_revision AND j.available_at<=NOW()
 AND (j.lease_until IS NULL OR j.lease_until<=NOW()) AND j.updated_at<=NOW()-$2*INTERVAL '1 millisecond'
 AND n.deleted_at IS NULL AND sn.deleted_at IS NULL AND target.deleted_at IS NULL
 AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)
 ORDER BY j.available_at,j.source_id LIMIT $1 FOR UPDATE OF j SKIP LOCKED`,
		limit,
		settle.Milliseconds(),
	)
	if err != nil {
		return nil, err
	}
	var pending []domain.SecretaryJob
	for rows.Next() {
		var job domain.SecretaryJob
		if err = rows.Scan(&job.SourceID, &job.OwnerUserID, &job.SourceAgentID, &job.SourceSessionID, &job.AgentID, &job.SettingsRevision, &job.ThroughRevision, &job.RetryGeneration); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pending = append(pending, job)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	var result []domain.SecretaryJob
	for _, job := range pending {
		job.LeaseToken, err = newSecret("secretary")
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE secretary_jobs SET lease_token=$2,lease_until=NOW()+$3*INTERVAL '1 millisecond' WHERE source_id=$1`, job.SourceID, job.LeaseToken, lease.Milliseconds()); err != nil {
			return nil, err
		}
		if err = loadSecretaryJob(ctx, tx, &job); err != nil {
			if _, err = tx.ExecContext(ctx, `UPDATE secretary_jobs SET lease_token='',lease_until=NULL,last_error=$2,attempts=attempts+1,available_at=NOW()+INTERVAL '30 seconds' WHERE source_id=$1`, job.SourceID, secretaryErrorText(err)); err != nil {
				return nil, err
			}
			continue
		}
		if len(job.Messages) == 0 && len(job.RemovedMessageIDs) == 0 && !job.ResetRequired {
			if _, err = tx.ExecContext(ctx, `UPDATE secretary_jobs SET processed_revision=$2,lease_token='',lease_until=NULL WHERE source_id=$1`, job.SourceID, job.ThroughRevision); err != nil {
				return nil, err
			}
			continue
		}
		secretaryPrepareJob(&job)
		result = append(result, job)
	}
	return result, tx.Commit()
}

type secretaryChange struct {
	id, hash, canonical string
	revision            int64
}

func loadSecretaryChanges(
	ctx context.Context,
	tx *sql.Tx,
	source string,
) ([]secretaryChange, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.message_id,m.change_revision,
 CASE WHEN m.processed_owner_user_id=j.owner_user_id THEN m.processed_hash ELSE '' END,m.processed_canonicalization_version
 FROM secretary_message_state m JOIN secretary_jobs j USING(source_id)
 WHERE m.source_id=$1 AND m.change_revision>j.processed_revision ORDER BY m.change_revision,m.message_id LIMIT $2`, source, domain.SecretaryMaxMessages)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var changes []secretaryChange
	for rows.Next() {
		var item secretaryChange
		if err = rows.Scan(&item.id, &item.revision, &item.hash, &item.canonical); err != nil {
			return nil, err
		}
		changes = append(changes, item)
	}
	return changes, rows.Err()
}

func loadSecretaryJob(ctx context.Context, tx *sql.Tx, job *domain.SecretaryJob) error {
	if err := loadSecretaryRollingBaseline(ctx, tx, job); err != nil {
		return err
	}
	changes, err := loadSecretaryChanges(ctx, tx, job.SourceID)
	if err != nil {
		return err
	}
	through := job.ThroughRevision
	job.ThroughRevision = 0
	size := 0
	for _, change := range changes {
		doc, readErr := loadSearchMessage(ctx, tx, change.id)
		if errors.Is(readErr, ErrNotFound) {
			if change.hash != "" {
				job.RemovedMessageIDs = append(job.RemovedMessageIDs, change.id)
			}
		} else if readErr != nil {
			return readErr
		} else if doc.OwnerUserID != job.OwnerUserID {
			return ErrNotFound
		} else if doc.AgentID != job.SourceAgentID || doc.SessionID != job.SourceSessionID {
			job.RemovedMessageIDs = append(job.RemovedMessageIDs, change.id)
		} else if doc.ContentHash != change.hash || doc.CanonicalizationVersion != change.canonical {
			if len(job.Messages) > 0 && size+len(doc.Body) > domain.SecretaryMaxBytes {
				break
			}
			job.Messages = append(job.Messages, doc)
			size += len(doc.Body)
		}
		job.ThroughRevision = change.revision
	}
	if job.ThroughRevision == 0 {
		job.ThroughRevision = through
	}
	secretaryFinalizeDelta(job)
	return secretaryWithinBounds(job.Messages)
}

func (s *PostgresStore) SecretaryJobActive(
	ctx context.Context,
	job domain.SecretaryJob,
) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, secretaryActiveSQL, job.SourceID, job.OwnerUserID, job.AgentID, job.SettingsRevision, job.LeaseToken).
		Scan(&active)
	return active, err
}

const secretaryActiveSQL = `SELECT EXISTS(SELECT 1 FROM secretary_jobs j JOIN secretary_settings cfg USING(owner_user_id)
 JOIN agents a ON a.agent_id=cfg.agent_id AND a.owner_user_id=cfg.owner_user_id AND a.deleted_at IS NULL
 JOIN agents source ON source.agent_id=j.source_agent_id AND source.owner_user_id=j.owner_user_id AND source.deleted_at IS NULL
 JOIN agent_sessions s ON s.id=j.source_id AND s.archived_at IS NULL
 LEFT JOIN nodes n ON n.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=source.node_id LEFT JOIN nodes session_node ON session_node.node_id=s.node_id
 WHERE j.source_id=$1 AND j.owner_user_id=$2 AND cfg.agent_id=$3 AND cfg.revision=$4 AND cfg.enabled
 AND j.lease_token=$5 AND j.lease_until>clock_timestamp() AND n.deleted_at IS NULL AND sn.deleted_at IS NULL AND session_node.deleted_at IS NULL
 AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id))`

func (s *PostgresStore) RegisterSecretaryInternalSession(
	ctx context.Context,
	job domain.SecretaryJob,
	agentID, sessionID string,
) error {
	if agentID != job.AgentID || sessionID == "" {
		return ErrConflict
	}
	active, err := s.SecretaryJobActive(ctx, job)
	if err != nil {
		return err
	}
	if !active {
		return domain.ErrSecretaryFenced
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`,
		agentID,
		sessionID,
		job.OwnerUserID,
	)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE agent_sessions SET source=$3 WHERE agent_id=$1 AND session_id=$2 AND source IS DISTINCT FROM $3`,
		agentID,
		sessionID,
		domain.SecretaryInternalSource,
	)
	return err
}

func (s *PostgresStore) FailSecretaryJob(
	ctx context.Context,
	job domain.SecretaryJob,
	failure error,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE secretary_jobs SET lease_token='',lease_until=NULL,attempts=attempts+1,
 last_error=$3,available_at=NOW()+LEAST(300,POWER(2,LEAST(attempts,8))) * INTERVAL '1 second'
 WHERE source_id=$1 AND lease_token=$2`,
		job.SourceID,
		job.LeaseToken,
		secretaryErrorText(failure),
	)
	return err
}

func secretarySourceID(id int64) string { return strconv.FormatInt(id, 10) }
