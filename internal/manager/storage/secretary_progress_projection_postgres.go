package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretaryProgressProjectionVersion = "progress-v1:" + domain.SearchCanonicalizationVersion
const secretaryProgressReadBudget = 256

var _ domain.SecretaryProgressProjectionStore = (*PostgresStore)(nil)

type secretaryProgressDirty struct {
	owner, message string
	generation     int64
}

func secretaryProgressWriter(ctx context.Context, tx *sql.Tx) (bool, error) {
	var acquired bool
	err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended(current_database()||':'||current_schema()||':pax:secretary:progress:projection',0))`).
		Scan(&acquired)
	return acquired, err
}

func ensureSecretaryProgressOwner(ctx context.Context, tx *sql.Tx, owner string) error {
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO secretary_progress_owners(owner_user_id,projection_version,bootstrap_upper)
 VALUES($1,$2,(SELECT COALESCE(max(id),0) FROM messages)) ON CONFLICT DO NOTHING`,
		owner,
		secretaryProgressProjectionVersion,
	)
	if err != nil {
		return err
	}
	var version string
	if err = tx.QueryRowContext(ctx, `SELECT projection_version FROM secretary_progress_owners WHERE owner_user_id=$1`, owner).Scan(&version); err != nil {
		return err
	}
	if version == secretaryProgressProjectionVersion {
		return nil
	}
	for _, table := range []string{"secretary_progress_messages", "secretary_progress_sessions"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE owner_user_id=$1`, owner); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_progress_owners SET projection_version=$2,bootstrap_after=0,
 bootstrap_upper=(SELECT COALESCE(max(id),0) FROM messages),bootstrap_complete=FALSE,updated_at=NOW() WHERE owner_user_id=$1`,
		owner,
		secretaryProgressProjectionVersion,
	)
	return err
}

func stageSecretaryProgressBootstrap(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	limit int,
) error {
	// Bound the primary-key page before applying owner/visibility filters. A sparse
	// owner must not cause one background step to walk the entire message table.
	var after, count int64
	var rawIDs []byte
	err := tx.QueryRowContext(ctx, `WITH page AS MATERIALIZED (
 SELECT m.id,m.message_id,m.agent_id,m.session_id,m.role,m.message_type FROM secretary_progress_owners p
 JOIN messages m ON m.id>p.bootstrap_after AND m.id<=p.bootstrap_upper
 WHERE p.owner_user_id=$1 AND NOT p.bootstrap_complete ORDER BY m.id LIMIT $2
 ) SELECT COALESCE(max(m.id),0),count(*),COALESCE(jsonb_agg(m.message_id) FILTER(WHERE
 a.owner_user_id=$1 AND s.id IS NOT NULL AND m.role IN ('user','assistant')
 AND COALESCE(m.message_type,'') IN (SELECT jsonb_array_elements_text($3::jsonb))
 AND COALESCE(s.source,'')<>'secretary_internal' AND NOT starts_with(s.session_id,'sess_secretary_')
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=m.agent_id AND i.session_id=m.session_id)), '[]'::jsonb)
 FROM page m LEFT JOIN agents a ON a.agent_id=m.agent_id
 LEFT JOIN agent_sessions s ON s.agent_id=m.agent_id AND s.session_id=m.session_id`, owner, limit, secretaryProgressIDs(domain.SearchVisibleMessageTypes())).Scan(&after, &count, &rawIDs)
	if err != nil {
		return err
	}
	var ids []string
	if err = json.Unmarshal(rawIDs, &ids); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `INSERT INTO secretary_progress_dirty(owner_user_id,message_id,is_backfill)
 SELECT $1,$2,TRUE WHERE NOT EXISTS(SELECT 1 FROM secretary_progress_messages WHERE owner_user_id=$1 AND message_id=$2)
 ON CONFLICT DO NOTHING`, owner, id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_progress_owners SET bootstrap_after=GREATEST(bootstrap_after,$2),
 bootstrap_complete=bootstrap_complete OR $3,updated_at=NOW() WHERE owner_user_id=$1`,
		owner,
		after,
		count < int64(limit),
	)
	return err
}

func secretaryProgressReady(ctx context.Context, tx *sql.Tx, owner string) (bool, error) {
	var ready bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secretary_progress_owners p WHERE owner_user_id=$1
 AND projection_version=$2 AND bootstrap_complete AND NOT EXISTS(SELECT 1 FROM secretary_progress_dirty d WHERE d.owner_user_id=$1))`, owner, secretaryProgressProjectionVersion).
		Scan(&ready)
	return ready, err
}

func drainSecretaryProgress(ctx context.Context, tx *sql.Tx, owner string, limit int) (int, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT owner_user_id,message_id,generation FROM secretary_progress_dirty
 WHERE ($1='' OR owner_user_id=$1) ORDER BY is_backfill,generation,owner_user_id,message_id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		owner,
		limit,
	)
	if err != nil {
		return 0, err
	}
	var dirty []secretaryProgressDirty
	groups := map[string][]string{}
	for rows.Next() {
		var item secretaryProgressDirty
		if err = rows.Scan(&item.owner, &item.message, &item.generation); err != nil {
			_ = rows.Close()
			return 0, err
		}
		dirty = append(dirty, item)
		groups[item.owner] = append(groups[item.owner], item.message)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	for key, ids := range groups {
		if err = ensureSecretaryProgressOwner(ctx, tx, key); err != nil {
			return 0, err
		}
		if err = refreshSecretaryProgressMessages(ctx, tx, key, ids); err != nil {
			return 0, err
		}
	}
	for _, item := range dirty {
		if _, err = tx.ExecContext(ctx, `DELETE FROM secretary_progress_dirty WHERE owner_user_id=$1 AND message_id=$2 AND generation=$3`, item.owner, item.message, item.generation); err != nil {
			return 0, err
		}
	}
	return len(dirty), nil
}

// Advance performs at most limit canonical message reads. The independent worker
// prioritizes live changes and resumes bounded historical pages after restarts.
func (s *PostgresStore) AdvanceSecretaryProgress(ctx context.Context, limit int) (bool, error) {
	if limit <= 0 || limit > secretaryProgressReadBudget {
		limit = secretaryProgressReadBudget
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := secretaryProgressWriter(ctx, tx)
	if err != nil || !locked {
		return false, err
	}
	n, err := drainSecretaryProgress(ctx, tx, "", limit)
	if err != nil {
		return false, err
	}
	if n < limit {
		var owner string
		err = tx.QueryRowContext(ctx, `SELECT owner_user_id FROM secretary_progress_owners WHERE NOT bootstrap_complete OR projection_version<>$1 ORDER BY updated_at,owner_user_id LIMIT 1`, secretaryProgressProjectionVersion).
			Scan(&owner)
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
		if err == nil {
			if err = ensureSecretaryProgressOwner(ctx, tx, owner); err != nil {
				return false, err
			}
			if err = stageSecretaryProgressBootstrap(ctx, tx, owner, limit-n); err != nil {
				return false, err
			}
			// Publish new queue rows before reading their source bodies. Deletion
			// triggers can then retain their owner while a later refresh is in flight.
			n++
		}
	}
	return n > 0, tx.Commit()
}

func (s *PostgresStore) readSecretaryProgressProjection(
	ctx context.Context,
	owner, after string,
	limit int,
) (domain.SecretaryProgressPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	ready, err := secretaryProgressReady(ctx, tx, owner)
	if err == nil && ready {
		page, readErr := readSecretaryProgressCounters(ctx, tx, owner, after, limit)
		if readErr != nil {
			_ = tx.Rollback()
			return page, readErr
		}
		return page, tx.Commit()
	}
	_ = tx.Rollback()
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	return s.prepareSecretaryProgress(ctx, owner, after, limit)
}

func (s *PostgresStore) prepareSecretaryProgress(
	ctx context.Context,
	owner, after string,
	limit int,
) (domain.SecretaryProgressPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := secretaryProgressWriter(ctx, tx)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	if !locked {
		return domain.SecretaryProgressPage{}, domain.ErrSecretaryProgressPreparing
	}
	if err = ensureSecretaryProgressOwner(ctx, tx, owner); err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	if err = stageSecretaryProgressBootstrap(ctx, tx, owner, secretaryProgressReadBudget); err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	return s.refreshSecretaryProgressOwner(ctx, owner, after, limit)
}

func (s *PostgresStore) refreshSecretaryProgressOwner(
	ctx context.Context,
	owner, after string,
	limit int,
) (domain.SecretaryProgressPage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := secretaryProgressWriter(ctx, tx)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	if !locked {
		return domain.SecretaryProgressPage{}, domain.ErrSecretaryProgressPreparing
	}
	if _, err = drainSecretaryProgress(ctx, tx, owner, secretaryProgressReadBudget); err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	ready, err := secretaryProgressReady(ctx, tx, owner)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	if !ready {
		if err = tx.Commit(); err != nil {
			return domain.SecretaryProgressPage{}, err
		}
		return domain.SecretaryProgressPage{}, domain.ErrSecretaryProgressPreparing
	}
	page, err := readSecretaryProgressCounters(ctx, tx, owner, after, limit)
	if err != nil {
		return page, err
	}
	return page, tx.Commit()
}

func secretaryProgressIDs(ids []string) string {
	raw, _ := json.Marshal(ids)
	return string(raw)
}
