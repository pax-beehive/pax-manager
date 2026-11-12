package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryRunStore = (*PostgresStore)(nil)
var _ domain.SecretaryRunQueueStore = (*PostgresStore)(nil)

func secretaryRunTransaction(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='2s'`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func secretaryRunReceipt(
	ctx context.Context,
	tx *sql.Tx,
	owner, id string,
) (domain.SecretaryRunReceipt, error) {
	result := domain.SecretaryRunReceipt{RequestID: id, State: "accepted"}
	err := tx.QueryRowContext(ctx, `SELECT accepted_at FROM secretary_run_commands WHERE owner_user_id=$1 AND request_id=$2`, owner, id).
		Scan(&result.AcceptedAt)
	return result, err
}

func (s *PostgresStore) RequestSecretaryRun(
	ctx context.Context,
	owner, id string,
) (domain.SecretaryRunReceipt, error) {
	if err := secretaryRunRequestError(ctx, owner, id); err != nil {
		return domain.SecretaryRunReceipt{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := secretaryRunTransaction(ctx, s.db)
	if err != nil {
		return domain.SecretaryRunReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := secretaryRunReceipt(ctx, tx, owner, id)
	if err == nil {
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.SecretaryRunReceipt{}, err
	}
	var enabled bool
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT enabled,revision FROM secretary_settings WHERE owner_user_id=$1 FOR SHARE`, owner).
		Scan(&enabled, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.SecretaryRunReceipt{}, err
	}
	if !enabled {
		// A concurrent acceptance can precede a later settings disable.
		result, readErr := secretaryRunReceipt(ctx, tx, owner, id)
		if readErr == nil {
			return result, tx.Commit()
		}
		if !errors.Is(readErr, sql.ErrNoRows) {
			return domain.SecretaryRunReceipt{}, readErr
		}
		return domain.SecretaryRunReceipt{}, ErrConflict
	}
	result = domain.SecretaryRunReceipt{RequestID: id, State: "accepted"}
	err = tx.QueryRowContext(ctx, `INSERT INTO secretary_run_commands(owner_user_id,request_id,settings_revision,command_version,source_upper)
VALUES($1,$2,$3,$4,(SELECT COALESCE(max(source_id),0) FROM secretary_jobs))
ON CONFLICT(owner_user_id,request_id) DO NOTHING RETURNING accepted_at`, owner, id, revision, secretaryRunCommandVersion).
		Scan(&result.AcceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		result, err = secretaryRunReceipt(ctx, tx, owner, id)
	}
	if err != nil {
		return domain.SecretaryRunReceipt{}, err
	}
	return result, tx.Commit()
}

type secretaryRunCommandRecord struct {
	owner, id, version string
	settings           int64
	upper, after       int64
}

func secretaryRunCandidates(ctx context.Context, tx *sql.Tx) ([]secretaryRunCommandRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT owner_user_id,request_id FROM secretary_run_commands
WHERE state='pending' ORDER BY accepted_at,owner_user_id,request_id LIMIT 16`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var candidates []secretaryRunCommandRecord
	for rows.Next() {
		var command secretaryRunCommandRecord
		if err = rows.Scan(&command.owner, &command.id); err != nil {
			return nil, err
		}
		candidates = append(candidates, command)
	}
	return candidates, rows.Err()
}

func (s *PostgresStore) AdvanceSecretaryRuns(ctx context.Context, limit int) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := secretaryRunTransaction(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	candidates, err := secretaryRunCandidates(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		var locked bool
		err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended(
current_database()||':'||current_schema()||':pax:secretary:runs:'||$1,0))`, candidate.owner).Scan(&locked)
		if err != nil {
			return false, err
		}
		if !locked {
			continue
		}
		worked, advanceErr := advanceSecretaryRunCommand(
			ctx,
			tx,
			candidate,
			secretaryRunLimit(limit),
		)
		if advanceErr != nil {
			return false, advanceErr
		}
		return worked, tx.Commit()
	}
	return false, tx.Commit()
}

func advanceSecretaryRunCommand(
	ctx context.Context,
	tx *sql.Tx,
	command secretaryRunCommandRecord,
	limit int,
) (bool, error) {
	var enabled bool
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT enabled,revision FROM secretary_settings WHERE owner_user_id=$1 FOR SHARE`, command.owner).
		Scan(&enabled, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT settings_revision,command_version,source_upper,source_after
FROM secretary_run_commands WHERE owner_user_id=$1 AND request_id=$2 AND state='pending' FOR UPDATE`, command.owner, command.id).
		Scan(&command.settings, &command.version, &command.upper, &command.after)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !enabled || revision != command.settings || command.version != secretaryRunCommandVersion {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_run_commands SET state='canceled',completed_at=NOW()
WHERE owner_user_id=$1 AND request_id=$2`,
			command.owner,
			command.id,
		)
		return true, err
	}
	var after int64
	var visited, queued int
	err = tx.QueryRowContext(ctx, secretaryRunPageSQL, command.owner, command.after, command.upper, limit).
		Scan(&after, &visited, &queued)
	if err != nil {
		return false, err
	}
	complete := visited < limit || after >= command.upper
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_run_commands SET source_after=$3,queued_count=queued_count+$4,
state=CASE WHEN $5 THEN 'completed' ELSE state END,completed_at=CASE WHEN $5 THEN NOW() ELSE completed_at END
WHERE owner_user_id=$1 AND request_id=$2`,
		command.owner,
		command.id,
		after,
		queued,
		complete,
	)
	return true, err
}
