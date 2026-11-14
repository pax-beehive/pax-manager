package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// RuntimeConfigSchemaPath is applied by EnsureSchema and by tests.
const RuntimeConfigSchemaPath = "db/migrations/20261113_runtime_config.sql"

// PostgresRuntimeConfig implements domain.RuntimeConfigRepository.
type PostgresRuntimeConfig struct{ db *sql.DB }

func NewPostgresRuntimeConfig(db *sql.DB) *PostgresRuntimeConfig {
	return &PostgresRuntimeConfig{db: db}
}

// RuntimeConfig exposes the repository for the configuration center.
func (s *PostgresStore) RuntimeConfig() domain.RuntimeConfigRepository {
	return NewPostgresRuntimeConfig(s.db)
}

func (r *PostgresRuntimeConfig) RuntimeConfigRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT revision FROM runtime_config_revision WHERE id = 1), 0)`,
	).Scan(&revision)
	return revision, err
}

func (r *PostgresRuntimeConfig) LoadRuntimeConfig(
	ctx context.Context,
) (int64, []domain.RuntimeConfigRow, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var revision int64
	if err = tx.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT revision FROM runtime_config_revision WHERE id = 1), 0)`,
	).Scan(&revision); err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT key, value_json, version, updated_by, updated_at
FROM runtime_config ORDER BY key`)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.RuntimeConfigRow
	for rows.Next() {
		var row domain.RuntimeConfigRow
		var value []byte
		if err = rows.Scan(&row.Key, &value, &row.Version, &row.UpdatedBy,
			&row.UpdatedAt); err != nil {
			return 0, nil, err
		}
		row.ValueJSON = value
		out = append(out, row)
	}
	if err = rows.Err(); err != nil {
		return 0, nil, err
	}
	return revision, out, tx.Commit()
}

func (r *PostgresRuntimeConfig) WriteRuntimeConfig(
	ctx context.Context,
	request domain.RuntimeConfigWrite,
) (domain.RuntimeConfigWriteResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RuntimeConfigWriteResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// The revision row lock serializes writers, so the version check below
	// cannot race with another transaction.
	var revision int64
	if err = tx.QueryRowContext(ctx, `
INSERT INTO runtime_config_revision (id, revision) VALUES (1, 1)
ON CONFLICT (id) DO UPDATE SET revision = runtime_config_revision.revision + 1
RETURNING revision`).Scan(&revision); err != nil {
		return domain.RuntimeConfigWriteResult{}, err
	}
	current, present, err := readRuntimeConfigRow(ctx, tx, request.Key)
	if err != nil {
		return domain.RuntimeConfigWriteResult{}, err
	}
	if current.Version != request.ExpectedVersion {
		return domain.RuntimeConfigWriteResult{}, &domain.RuntimeConfigConflictError{
			Current: current.Version,
		}
	}
	if !present && !request.New.Present {
		return domain.RuntimeConfigWriteResult{}, nil
	}
	var lastAudit int64
	if err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM runtime_config_audit WHERE key = $1`,
		request.Key,
	).Scan(&lastAudit); err != nil {
		return domain.RuntimeConfigWriteResult{}, err
	}
	next := max(current.Version, lastAudit) + 1
	result := domain.RuntimeConfigWriteResult{Revision: revision}
	if request.New.Present {
		err = tx.QueryRowContext(ctx, `
INSERT INTO runtime_config (key, value_json, version, updated_by, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (key) DO UPDATE SET value_json = EXCLUDED.value_json,
  version = EXCLUDED.version, updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING updated_at`,
			request.Key, nullableJSON(request.New.ValueJSON), next, request.Actor,
		).Scan(&result.Row.UpdatedAt)
		result.Row.Key, result.Row.Version, result.Row.UpdatedBy = request.Key, next, request.Actor
		result.Present = true
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM runtime_config WHERE key = $1`, request.Key)
	}
	if err != nil {
		return domain.RuntimeConfigWriteResult{}, fmt.Errorf("write runtime config: %w", err)
	}
	var oldDisplay *string
	if present && request.DisplayOld != nil {
		oldDisplay = request.DisplayOld(current)
	}
	auditVersion := next
	if !request.New.Present {
		auditVersion = 0
	}
	if _, err = tx.ExecContext(ctx, `
INSERT INTO runtime_config_audit (key, action, old_display, new_display, old_value_json,
  version, actor, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		request.Key, request.Action, oldDisplay, request.NewDisplay,
		nullableJSON(current.ValueJSON), auditVersion, request.Actor, request.Reason,
	); err != nil {
		return domain.RuntimeConfigWriteResult{}, fmt.Errorf("write runtime config audit: %w", err)
	}
	return result, tx.Commit()
}

func nullableJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func readRuntimeConfigRow(
	ctx context.Context,
	tx *sql.Tx,
	key string,
) (domain.RuntimeConfigRow, bool, error) {
	row := domain.RuntimeConfigRow{Key: key}
	var value []byte
	err := tx.QueryRowContext(ctx, `
SELECT value_json, version, updated_by, updated_at
FROM runtime_config WHERE key = $1 FOR UPDATE`, key).Scan(
		&value, &row.Version, &row.UpdatedBy, &row.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RuntimeConfigRow{Key: key}, false, nil
	}
	row.ValueJSON = value
	return row, err == nil, err
}

const runtimeConfigAuditColumns = `id, key, action, old_display, new_display, old_value_json,
  version, actor, COALESCE(reason, ''), created_at`

func scanRuntimeConfigAudit(scanner rowScanner) (domain.RuntimeConfigAudit, error) {
	var record domain.RuntimeConfigAudit
	var value []byte
	err := scanner.Scan(&record.ID, &record.Key, &record.Action, &record.OldDisplay,
		&record.NewDisplay, &value, &record.Version, &record.Actor, &record.Reason,
		&record.CreatedAt)
	record.Old.ValueJSON = value
	record.Old.Present = len(value) > 0
	return record, err
}

func (r *PostgresRuntimeConfig) ListRuntimeConfigAudit(
	ctx context.Context,
	query domain.RuntimeConfigAuditQuery,
) ([]domain.RuntimeConfigAudit, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+runtimeConfigAuditColumns+`
FROM runtime_config_audit
WHERE ($1 = '' OR key = $1) AND ($2 = 0 OR id < $2)
ORDER BY id DESC LIMIT $3`, query.Key, query.BeforeID, query.Limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.RuntimeConfigAudit
	for rows.Next() {
		record, scanErr := scanRuntimeConfigAudit(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (r *PostgresRuntimeConfig) GetRuntimeConfigAudit(
	ctx context.Context,
	id int64,
) (domain.RuntimeConfigAudit, error) {
	record, err := scanRuntimeConfigAudit(r.db.QueryRowContext(ctx,
		`SELECT `+runtimeConfigAuditColumns+` FROM runtime_config_audit WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RuntimeConfigAudit{}, domain.ErrRuntimeConfigAuditNotFound
	}
	return record, err
}
