package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const shortAttemptSelect = `SELECT attempt_id,pairing_id,generation,approver_capability_hash,
 client_hello,recipient_answer,client_finish,secret_payload,stage,created_at,expires_at,confirmation_expires_at
 FROM e2ee_pairing_attempts`

func scanShortAttempt(row rowScanner) (a domain.ShortPairingAttempt, err error) {
	err = row.Scan(
		&a.AttemptID,
		&a.PairingID,
		&a.Generation,
		&a.ApproverCapabilityHash,
		&a.ClientHello,
		&a.RecipientAnswer,
		&a.ClientFinish,
		&a.SecretPayload,
		&a.Stage,
		&a.CreatedAt,
		&a.ExpiresAt,
		&a.ConfirmationExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func (s *PostgresStore) shortTransaction(
	ctx context.Context,
	request E2EEPairingRequest,
	action func(*sql.Tx, E2EEPairingRequest) error,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockE2EEPairing(ctx, tx, request); err != nil {
		return err
	}
	stored, err := scanE2EEPairingRequest(
		tx.QueryRowContext(
			ctx,
			e2eePairingRequestSelectSQL+` WHERE owner_user_id=$1 AND agent_id=$2 AND pairing_id=$3 FOR UPDATE`,
			request.OwnerUserID,
			request.AgentID,
			request.PairingID,
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err = action(tx, stored); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) CreateShortPairingAttempt(
	ctx context.Context,
	request E2EEPairingRequest,
	input domain.ShortPairingAttempt,
) (result domain.ShortPairingAttempt, err error) {
	err = s.shortTransaction(ctx, request, func(tx *sql.Tx, stored E2EEPairingRequest) error {
		if !validShortAttempt(input) {
			return ErrConflict
		}
		now := s.now().UTC()
		deadline, e := shortAttemptDeadline(stored, input.Generation, now)
		if e != nil {
			return e
		}
		old, e := scanShortAttempt(
			tx.QueryRowContext(ctx, shortAttemptSelect+` WHERE attempt_id=$1`, input.AttemptID),
		)
		if e == nil {
			if old.PairingID != stored.PairingID || old.Generation != input.Generation ||
				old.ClientHello != input.ClientHello ||
				!bytes.Equal(old.ApproverCapabilityHash, input.ApproverCapabilityHash) {
				return ErrConflict
			}
			result = old
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if e = reserveShortBudget(ctx, tx, stored, now); e != nil {
			return e
		}
		result, e = scanShortAttempt(
			tx.QueryRowContext(
				ctx,
				`INSERT INTO e2ee_pairing_attempts (attempt_id,pairing_id,generation,approver_capability_hash,client_hello,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING attempt_id,pairing_id,generation,approver_capability_hash,client_hello,recipient_answer,client_finish,secret_payload,stage,created_at,expires_at,confirmation_expires_at`,
				input.AttemptID,
				stored.PairingID,
				input.Generation,
				input.ApproverCapabilityHash,
				input.ClientHello,
				now,
				deadline,
			),
		)
		return e
	})
	return
}

func reserveShortBudget(
	ctx context.Context,
	tx *sql.Tx,
	request E2EEPairingRequest,
	now time.Time,
) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "pax/short/account/"+request.OwnerUserID); err != nil {
		return err
	}
	for _, key := range shortBudgetKeys(request) {
		var count int
		err := tx.QueryRowContext(ctx, `INSERT INTO e2ee_pairing_rate_limits(scope,window_started_at,attempt_count) VALUES($1,$2,1)
  ON CONFLICT(scope) DO UPDATE SET
  window_started_at=CASE WHEN e2ee_pairing_rate_limits.window_started_at <= $2 - interval '10 minutes' THEN $2 ELSE e2ee_pairing_rate_limits.window_started_at END,
  attempt_count=CASE WHEN e2ee_pairing_rate_limits.window_started_at <= $2 - interval '10 minutes' THEN 1 ELSE e2ee_pairing_rate_limits.attempt_count+1 END
  WHERE e2ee_pairing_rate_limits.window_started_at <= $2 - interval '10 minutes' OR e2ee_pairing_rate_limits.attempt_count<30
  RETURNING attempt_count`, key, now).
			Scan(&count)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrShortPairingLimited
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) ListShortPairingAttempts(
	ctx context.Context,
	request E2EEPairingRequest,
	capability []byte,
) (result []domain.ShortPairingAttempt, err error) {
	result = make([]domain.ShortPairingAttempt, 0)
	err = s.shortTransaction(ctx, request, func(tx *sql.Tx, stored E2EEPairingRequest) error {
		if len(capability) != 32 || !bytes.Equal(capability, stored.RecipientCapabilityHash) {
			return ErrNotFound
		}
		if e := shortPairingPending(stored, s.now().UTC()); e != nil {
			return e
		}
		rows, e := tx.QueryContext(
			ctx,
			shortAttemptSelect+` WHERE pairing_id=$1 AND stage<3 AND expires_at>$2 ORDER BY created_at LIMIT 30`,
			stored.PairingID,
			s.now().UTC(),
		)
		if e != nil {
			return e
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			a, e := scanShortAttempt(rows)
			if e != nil {
				return e
			}
			result = append(result, a)
		}
		return rows.Err()
	})
	return
}

func (s *PostgresStore) GetShortPairingAttempt(
	ctx context.Context,
	request E2EEPairingRequest,
	id string,
	capability []byte,
) (result domain.ShortPairingAttempt, err error) {
	err = s.shortTransaction(ctx, request, func(tx *sql.Tx, stored E2EEPairingRequest) error {
		a, e := scanShortAttempt(
			tx.QueryRowContext(
				ctx,
				shortAttemptSelect+` WHERE attempt_id=$1 AND pairing_id=$2`,
				id,
				stored.PairingID,
			),
		)
		if e != nil {
			return e
		}
		if len(capability) != 32 || !bytes.Equal(capability, a.ApproverCapabilityHash) {
			return ErrNotFound
		}
		result = a
		return nil
	})
	return
}

func (s *PostgresStore) AdvanceShortPairingAttempt(
	ctx context.Context,
	request E2EEPairingRequest,
	id string,
	capability []byte,
	stage int,
	payload string,
) (result domain.ShortPairingAttempt, err error) {
	err = s.shortTransaction(ctx, request, func(tx *sql.Tx, stored E2EEPairingRequest) error {
		a, e := scanShortAttempt(
			tx.QueryRowContext(
				ctx,
				shortAttemptSelect+` WHERE attempt_id=$1 AND pairing_id=$2 FOR UPDATE`,
				id,
				stored.PairingID,
			),
		)
		if e != nil {
			return e
		}
		result, e = shortAdvance(stored, a, capability, stage, payload, s.now().UTC())
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(
			ctx,
			`UPDATE e2ee_pairing_attempts SET recipient_answer=$2,client_finish=$3,secret_payload=$4,stage=$5,confirmation_expires_at=$6 WHERE attempt_id=$1`,
			id,
			result.RecipientAnswer,
			result.ClientFinish,
			result.SecretPayload,
			result.Stage,
			result.ConfirmationExpiresAt,
		)
		return e
	})
	return
}

func (s *PostgresStore) EndShortPairing(
	ctx context.Context,
	request E2EEPairingRequest,
	capability []byte,
	reason string,
) error {
	return s.shortTransaction(ctx, request, func(tx *sql.Tx, stored E2EEPairingRequest) error {
		var attempt domain.ShortPairingAttempt
		if reason == "rejected" {
			var e error
			attempt, e = scanShortAttempt(
				tx.QueryRowContext(
					ctx,
					shortAttemptSelect+` WHERE attempt_id=$1 AND pairing_id=$2`,
					request.ApprovalAttemptID,
					stored.PairingID,
				),
			)
			if e != nil {
				return e
			}
		}
		if e := shortEnd(stored, request, attempt, capability, reason, s.now().UTC()); e != nil {
			return e
		}
		if stored.CancelledAt != nil || stored.RejectedAt != nil {
			return nil
		}
		query := `UPDATE e2ee_pairing_requests SET cancelled_at=$2 WHERE pairing_id=$1`
		if reason == "rejected" {
			query = `UPDATE e2ee_pairing_requests SET rejected_at=$2 WHERE pairing_id=$1`
		} else if reason != "cancelled" {
			return ErrConflict
		}
		_, e := tx.ExecContext(ctx, query, stored.PairingID, s.now().UTC())
		return e
	})
}
