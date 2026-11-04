package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretaryLegacyMissingTunnel = "secretary execution outcome is unknown\nagent tunnel not connected"

// Only this exact legacy pre-dispatch error proves the native prompt was never sent.
// Absence of a session alone is not sufficient evidence to retry an unknown execution.
const secretaryConnectionRepairSQL = `SELECT b.batch_id,a.attempt_id,b.source_id::text
 FROM secretary_batches b JOIN secretary_jobs j ON j.source_id=b.source_id AND j.owner_user_id=b.owner_user_id
 JOIN secretary_attempts a ON a.attempt_id=b.current_attempt_id AND a.batch_id=b.batch_id
 JOIN secretary_settings cfg ON cfg.owner_user_id=b.owner_user_id AND cfg.enabled
 JOIN agents selected ON selected.agent_id=cfg.agent_id AND selected.owner_user_id=b.owner_user_id AND selected.deleted_at IS NULL
 CROSS JOIN LATERAL (SELECT 'sess_secretary_'||left(encode(sha256(convert_to(b.owner_user_id,'UTF8')||
 decode('00','hex')||convert_to(a.attempt_id,'UTF8')),'hex'),32) session_id) execution
 WHERE b.state='unknown' AND a.state='unknown' AND NOT a.not_dispatched AND a.output_text=''
 AND a.last_error=$1 AND b.connection_failures=0
 AND b.settings_revision=cfg.revision AND b.retry_epoch=j.retry_epoch
 AND b.input_json->>'AgentID'=cfg.agent_id AND b.input_json->>'OwnerUserID'=b.owner_user_id
 AND b.input_json->>'SourceID'=j.source_id::text
 AND b.input_json->>'BatchID'=b.batch_id
 AND b.input_json->>'SettingsRevision'=b.settings_revision::text
 AND b.input_json->>'RetryEpoch'=b.retry_epoch::text
 AND b.input_json->>'ThroughRevision'=b.through_revision::text
 AND b.through_revision>0 AND b.through_revision<=j.desired_revision
 AND b.input_json->>'SourceAgentID'=j.source_agent_id AND b.input_json->>'SourceSessionID'=j.source_session_id
 AND j.desired_revision>j.processed_revision
 AND (j.lease_token='' OR j.lease_until IS NULL OR j.lease_until<=NOW())
 AND NOT EXISTS(SELECT 1 FROM agent_sessions s WHERE s.session_id=execution.session_id)
 AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.session_id=execution.session_id)
 FOR UPDATE OF j,b,a SKIP LOCKED`

func repairSecretaryConnectionsTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, secretaryConnectionRepairSQL, secretaryLegacyMissingTunnel)
	if err != nil {
		return err
	}
	var jobs []domain.SecretaryJob
	for rows.Next() {
		var job domain.SecretaryJob
		if err = rows.Scan(&job.BatchID, &job.AttemptID, &job.SourceID); err != nil {
			_ = rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		err = writeSecretaryConnectionFailureTx(ctx, tx, job, secretaryExecutionBatch{},
			domain.SecretaryAttempt{ID: job.AttemptID}, domain.ErrSecretaryNotDispatched, now)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *MemoryStore) repairSecretaryConnectionsLocked() {
	for id, batch := range s.secretaryExecutionsLocked() {
		if batch.state != "unknown" || batch.connectionFailures != 0 || len(batch.attempts) == 0 {
			continue
		}
		attempt := &batch.attempts[len(batch.attempts)-1]
		queued := s.secretaryLocked().jobs[batch.source]
		if queued == nil || attempt.BatchID != id || !legacySecretaryAttemptRecoverable(*attempt) ||
			queued.work.LeaseToken != "" && queued.leaseUntil.After(s.now()) {
			continue
		}
		current := queued.work
		if !s.legacySecretaryInputMatchesLocked(id, batch, queued) ||
			s.legacySecretarySessionExistsLocked(batch.owner, attempt.ID) {
			continue
		}
		s.failSecretaryConnectionLocked(current, batch, attempt, domain.ErrSecretaryNotDispatched)
	}
}

func legacySecretaryAttemptRecoverable(attempt domain.SecretaryAttempt) bool {
	return attempt.State == "unknown" && !attempt.NotDispatched && attempt.Output == "" &&
		attempt.Error == secretaryLegacyMissingTunnel
}

func (s *MemoryStore) legacySecretaryInputMatchesLocked(
	id string,
	batch *memorySecretaryExecutionBatch,
	queued *memorySecretaryJob,
) bool {
	cfg := s.secretaryLocked().settings[batch.owner]
	current := queued.work
	current.BatchID, current.SettingsRevision, current.AgentID = id, cfg.Revision, cfg.AgentID
	current.ThroughRevision = queued.desired
	var stored domain.SecretaryJob
	return cfg.Enabled && batch.owner == current.OwnerUserID && batch.settings == cfg.Revision &&
		batch.epoch == current.RetryEpoch && s.secretaryAgentOwnedLocked(batch.owner, cfg.AgentID) &&
		json.Unmarshal(
			batch.input,
			&stored,
		) == nil && validateSecretaryBatchIdentity(stored, current) == nil &&
		queued.desired > queued.processed
}

func (s *MemoryStore) legacySecretarySessionExistsLocked(owner, attempt string) bool {
	sessionID := domain.SecretaryInternalSessionPrefix +
		strings.TrimPrefix(domain.SearchContentHash(owner+"\x00"+attempt), "sha256:")[:32]
	for _, session := range s.sessions {
		if session.SessionID == sessionID {
			return true
		}
	}
	for _, message := range s.messages {
		if message.SessionID == sessionID {
			return true
		}
	}
	return false
}
