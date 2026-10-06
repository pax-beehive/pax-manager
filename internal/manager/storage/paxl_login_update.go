package storage

import (
	"context"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// The caller holds the login row lock until any credential and node are committed.
func nextPaxlLoginStatus(
	session PaxlDeviceLoginSession,
	update domain.PaxlDeviceLoginUpdate,
	now time.Time,
) (string, error) {
	if session.Protocol != domain.PaxlDeviceLoginProtocolClientCommit ||
		!session.ExpiresAt.After(now) {
		return "", ErrUnauthorized
	}
	if update.Action == "cancel" {
		switch session.Status {
		case domain.PaxlDeviceLoginStatusPending,
			domain.PaxlDeviceLoginStatusConfirmed,
			domain.PaxlDeviceLoginStatusCancelled:
			return domain.PaxlDeviceLoginStatusCancelled, nil
		default:
			return "", ErrConflict
		}
	}
	if update.ExpectedUserID == "" || update.ExpectedUserID != session.OwnerUserID {
		return "", ErrConflict
	}
	switch update.Action {
	case "commit":
		switch session.Status {
		case domain.PaxlDeviceLoginStatusConfirmed:
			if update.KeyHash == "" || update.KeyPrefix == "" || update.APIKey == "" {
				return "", ErrUnauthorized
			}
			return domain.PaxlDeviceLoginStatusApproved, nil
		case domain.PaxlDeviceLoginStatusApproved, domain.PaxlDeviceLoginStatusConsumed:
			return session.Status, nil
		}
	case "ack":
		if session.Status == domain.PaxlDeviceLoginStatusApproved ||
			session.Status == domain.PaxlDeviceLoginStatusConsumed {
			return domain.PaxlDeviceLoginStatusConsumed, nil
		}
	}
	return "", ErrConflict
}

func (s *MemoryStore) UpdatePaxlDeviceLoginSession(
	ctx context.Context,
	update domain.PaxlDeviceLoginUpdate,
) (PaxlDeviceLoginSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.paxlDeviceLogins[update.LoginID]
	if !ok || session.PollTokenHash != update.PollTokenHash {
		return PaxlDeviceLoginSession{}, ErrUnauthorized
	}
	now := s.now().UTC()
	next, err := nextPaxlLoginStatus(session, update, now)
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	if next == session.Status {
		return session, nil
	}
	if next == domain.PaxlDeviceLoginStatusApproved {
		return s.issuePaxlLoginLocked(
			session,
			session.OwnerUserID,
			update.KeyHash,
			update.KeyPrefix,
			update.APIKey,
			now,
		)
	}
	session.Status, session.APIKey = next, ""
	if next == domain.PaxlDeviceLoginStatusConsumed {
		session.ConsumedAt = &now
	}
	s.paxlDeviceLogins[session.LoginID] = session
	return session, nil
}

func (s *PostgresStore) UpdatePaxlDeviceLoginSession(
	ctx context.Context,
	update domain.PaxlDeviceLoginUpdate,
) (PaxlDeviceLoginSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	defer func() { _ = tx.Rollback() }()
	session, err := scanPaxlDeviceLoginSession(
		tx.QueryRowContext(
			ctx,
			paxlDeviceLoginSelectSQL+` WHERE login_id = $1 AND poll_token_hash = $2 FOR UPDATE`,
			update.LoginID,
			update.PollTokenHash,
		),
	)
	if errors.Is(err, ErrNotFound) {
		return PaxlDeviceLoginSession{}, ErrUnauthorized
	}
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	now := s.now().UTC()
	next, err := nextPaxlLoginStatus(session, update, now)
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	if next == session.Status {
		return session, nil
	}
	if next == domain.PaxlDeviceLoginStatusApproved {
		session, err = issuePaxlLoginTx(
			ctx,
			tx,
			session,
			session.OwnerUserID,
			update.KeyHash,
			update.KeyPrefix,
			update.APIKey,
			now,
		)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE paxl_device_login_sessions SET status = $2, api_key = NULL, consumed_at = CASE WHEN $2 = 'consumed' THEN $3 ELSE consumed_at END WHERE login_id = $1`, session.LoginID, next, now)
		session.Status, session.APIKey = next, ""
		if next == domain.PaxlDeviceLoginStatusConsumed {
			session.ConsumedAt = &now
		}
	}
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	return session, tx.Commit()
}
