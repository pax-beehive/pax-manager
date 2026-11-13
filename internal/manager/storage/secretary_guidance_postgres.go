package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryGuidanceStore = (*PostgresStore)(nil)

const secretaryGuidanceColumns = `owner_user_id,scope,COALESCE(source_id::text,''),revision,instruction,updated_at`
const secretaryGuidanceSelect = `SELECT ` + secretaryGuidanceColumns + ` FROM secretary_guidance
 WHERE owner_user_id=$1 AND scope=$2 AND source_id IS NOT DISTINCT FROM NULLIF($3,'')::bigint`

func scanSecretaryGuidance(
	row interface{ Scan(...any) error },
	owner, scope, source string,
) (domain.SecretaryGuidance, error) {
	result := secretaryDefaultGuidance(owner, scope, source)
	err := row.Scan(
		&result.OwnerUserID,
		&result.Scope,
		&result.SourceID,
		&result.Revision,
		&result.Instruction,
		&result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	return result, err
}

func secretaryGuidanceSourceTx(
	ctx context.Context,
	tx *sql.Tx,
	owner, source string,
	lock bool,
) error {
	query := `SELECT s.id FROM agent_sessions s JOIN agents a ON a.agent_id=s.agent_id
 LEFT JOIN nodes n ON n.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id
 WHERE s.id=$1 AND a.owner_user_id=$2 AND a.deleted_at IS NULL AND s.archived_at IS NULL
 AND n.deleted_at IS NULL AND sn.deleted_at IS NULL AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT starts_with(s.session_id,'sess_secretary_')
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)`
	if lock {
		query += ` FOR SHARE OF s,a`
	}
	var id int64
	err := tx.QueryRowContext(ctx, query, source, owner).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *PostgresStore) GetSecretaryGuidance(
	ctx context.Context,
	owner, scope, source string,
) (domain.SecretaryGuidance, error) {
	if owner == "" {
		return domain.SecretaryGuidance{}, ErrUnauthorized
	}
	if err := domain.ValidateSecretaryGuidanceScope(scope, source); err != nil {
		return domain.SecretaryGuidance{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.SecretaryGuidance{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if scope == domain.SecretaryGuidanceSession {
		if err = secretaryGuidanceSourceTx(ctx, tx, owner, source, false); err != nil {
			return domain.SecretaryGuidance{}, err
		}
	}
	result, err := scanSecretaryGuidance(
		tx.QueryRowContext(ctx, secretaryGuidanceSelect, owner, scope, source),
		owner,
		scope,
		source,
	)
	if err != nil {
		return domain.SecretaryGuidance{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) UpdateSecretaryGuidance(
	ctx context.Context,
	owner, scope, source string,
	update domain.SecretaryGuidanceUpdate,
) (domain.SecretaryGuidance, error) {
	if owner == "" {
		return domain.SecretaryGuidance{}, ErrUnauthorized
	}
	if err := domain.ValidateSecretaryGuidanceScope(scope, source); err != nil {
		return domain.SecretaryGuidance{}, err
	}
	instruction, err := domain.NormalizeSecretaryInstruction(update.Instruction)
	if err != nil {
		return domain.SecretaryGuidance{}, err
	}
	if update.ExpectedRevision < 0 {
		return domain.SecretaryGuidance{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SecretaryGuidance{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if scope == domain.SecretaryGuidanceSession {
		if err = secretaryGuidanceSourceTx(ctx, tx, owner, source, true); err != nil {
			return domain.SecretaryGuidance{}, err
		}
	}
	conflict := `(owner_user_id) WHERE scope='user'`
	if scope == domain.SecretaryGuidanceSession {
		conflict = `(owner_user_id,source_id) WHERE scope='session'`
	}
	row := tx.QueryRowContext(
		ctx,
		`INSERT INTO secretary_guidance(owner_user_id,scope,source_id,revision,instruction,updated_at)
 VALUES($1,$2,NULLIF($3,'')::bigint,1,$4,NOW()) ON CONFLICT `+conflict+` DO UPDATE
 SET revision=secretary_guidance.revision+1,instruction=EXCLUDED.instruction,updated_at=NOW()
 WHERE secretary_guidance.revision=$5 RETURNING `+secretaryGuidanceColumns,
		owner,
		scope,
		source,
		instruction,
		update.ExpectedRevision,
	)
	result, err := scanSecretaryGuidance(row, owner, scope, source)
	if err != nil {
		return domain.SecretaryGuidance{}, err
	}
	if result.Revision == 0 || result.Revision == 1 && update.ExpectedRevision != 0 {
		return domain.SecretaryGuidance{}, ErrConflict
	}
	return result, tx.Commit()
}

func loadSecretaryInstructionsTx(
	ctx context.Context,
	tx *sql.Tx,
	owner, source string,
) (*domain.SecretaryInstructionSnapshot, error) {
	result := &domain.SecretaryInstructionSnapshot{
		User:    secretaryDefaultGuidance(owner, domain.SecretaryGuidanceUser, ""),
		Session: secretaryDefaultGuidance(owner, domain.SecretaryGuidanceSession, source),
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+secretaryGuidanceColumns+` FROM secretary_guidance
 WHERE owner_user_id=$1 AND (scope='user' OR scope='session' AND source_id=$2::bigint) ORDER BY scope`, owner, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		item, scanErr := scanSecretaryGuidance(rows, owner, "", "")
		if scanErr != nil {
			return nil, scanErr
		}
		if item.Scope == domain.SecretaryGuidanceUser {
			result.User = item
		} else {
			result.Session = item
		}
	}
	return result, rows.Err()
}
