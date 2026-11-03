package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryStore = (*PostgresStore)(nil)

const secretarySettingsSelect = `SELECT owner_user_id, agent_id, enabled, revision, updated_at FROM secretary_settings WHERE owner_user_id=$1`

func scanSecretarySettings(
	row interface{ Scan(...any) error },
	owner string,
) (domain.SecretarySettings, error) {
	result := domain.SecretarySettings{OwnerUserID: owner}
	err := row.Scan(
		&result.OwnerUserID,
		&result.AgentID,
		&result.Enabled,
		&result.Revision,
		&result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	return result, err
}

func (s *PostgresStore) GetSecretarySettings(
	ctx context.Context,
	owner string,
) (domain.SecretarySettings, error) {
	if owner == "" {
		return domain.SecretarySettings{}, ErrUnauthorized
	}
	return scanSecretarySettings(s.db.QueryRowContext(ctx, secretarySettingsSelect, owner), owner)
}

func (s *PostgresStore) UpdateSecretarySettings(
	ctx context.Context,
	owner string,
	update domain.SecretarySettingsUpdate,
) (domain.SecretarySettings, error) {
	if owner == "" {
		return domain.SecretarySettings{}, ErrUnauthorized
	}
	if update.ExpectedRevision < 0 || update.Enabled && update.AgentID == "" {
		return domain.SecretarySettings{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SecretarySettings{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if update.AgentID != "" {
		var allowed bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents a LEFT JOIN nodes n ON n.node_id=a.node_id
 WHERE a.agent_id=$1 AND a.owner_user_id=$2 AND a.deleted_at IS NULL AND n.deleted_at IS NULL)
 OR (NOT $3 AND EXISTS(SELECT 1 FROM secretary_settings WHERE owner_user_id=$2 AND agent_id=$1))`, update.AgentID, owner, update.Enabled).
			Scan(&allowed)
		if err != nil {
			return domain.SecretarySettings{}, err
		}
		if !allowed {
			return domain.SecretarySettings{}, ErrNotFound
		}
	}
	row := tx.QueryRowContext(
		ctx,
		`INSERT INTO secretary_settings(owner_user_id,agent_id,enabled,revision,updated_at)
 VALUES($1,$2,$3,1,NOW()) ON CONFLICT(owner_user_id) DO UPDATE SET agent_id=EXCLUDED.agent_id,
 enabled=EXCLUDED.enabled,revision=secretary_settings.revision+1,updated_at=NOW()
 WHERE secretary_settings.revision=$4 RETURNING owner_user_id,agent_id,enabled,revision,updated_at`,
		owner,
		update.AgentID,
		update.Enabled,
		update.ExpectedRevision,
	)
	result, err := scanSecretarySettings(row, owner)
	if err != nil {
		return domain.SecretarySettings{}, err
	}
	if result.Revision == 0 || result.Revision == 1 && update.ExpectedRevision != 0 {
		return domain.SecretarySettings{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE secretary_jobs SET lease_token='',lease_until=NULL,available_at=NOW() WHERE owner_user_id=$1`, owner); err != nil {
		return domain.SecretarySettings{}, err
	}
	if update.Enabled {
		if err = backfillSecretaryTx(ctx, tx, owner); err != nil {
			return domain.SecretarySettings{}, err
		}
	}
	return result, tx.Commit()
}

func (s *PostgresStore) GetSecretaryStatus(
	ctx context.Context,
	owner string,
) (domain.SecretaryStatus, error) {
	cfg, err := s.GetSecretarySettings(ctx, owner)
	if err != nil {
		return domain.SecretaryStatus{}, err
	}
	result := domain.SecretaryStatus{SecretarySettings: cfg}
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE desired_revision>processed_revision),
 COALESCE(bool_or(lease_token<>'' AND lease_until>NOW()),false),max(completed_at),
 COALESCE((SELECT last_error FROM secretary_jobs WHERE owner_user_id=$1 AND last_error<>'' ORDER BY updated_at DESC LIMIT 1),'')
 FROM secretary_jobs WHERE owner_user_id=$1`, owner).
		Scan(&result.PendingCount, &result.Processing, &result.LastCompletedAt, &result.LastError)
	return result, err
}

func (s *PostgresStore) RetrySecretaryJobs(ctx context.Context, owner string) error {
	if owner == "" {
		return ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT enabled FROM secretary_settings WHERE owner_user_id=$1 FOR UPDATE`, owner).Scan(&enabled); errors.Is(
		err,
		sql.ErrNoRows,
	) {
		return ErrConflict
	} else if err != nil {
		return err
	}
	if !enabled {
		return ErrConflict
	}
	if err = backfillSecretaryTx(ctx, tx, owner); err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_jobs SET available_at=NOW(),attempts=0,last_error='',
 last_validation_failure=CASE WHEN lease_token='' OR lease_until<=NOW() THEN '' ELSE last_validation_failure END,
 auto_retry_key=CASE WHEN lease_token='' OR lease_until<=NOW() THEN '' ELSE auto_retry_key END,
 auto_retry_count=CASE WHEN lease_token='' OR lease_until<=NOW() THEN 0 ELSE auto_retry_count END,
 retry_generation=CASE WHEN lease_token='' OR lease_until<=NOW() THEN retry_generation+1 ELSE retry_generation END,
 updated_at=NOW()-INTERVAL '30 seconds' WHERE owner_user_id=$1 AND desired_revision>processed_revision`,
		owner,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}
