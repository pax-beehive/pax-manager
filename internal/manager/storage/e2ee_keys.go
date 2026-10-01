package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const e2eePairingRequestSelectSQL = `
	SELECT pairing_id, owner_user_id, node_id, agent_id, device_id, device_name,
		key_epoch, recipient_public_key, secret_commitment, created_at, expires_at,
		completed_at, superseded_at, protocol_version, recipient_capability_hash, cancelled_at, rejected_at
	FROM e2ee_pairing_requests`

const e2eeKeyPackageSelectSQL = `
	SELECT pairing_id, owner_user_id, node_id, agent_id, device_id, key_epoch,
		recipient_public_key, sender_ephemeral_public_key, nonce, ciphertext, created_at
	FROM e2ee_key_packages`

func (s *MemoryStore) CreateE2EEPairingRequest(
	_ context.Context,
	request E2EEPairingRequest,
) (E2EEPairingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validE2EEPairingRequest(request) {
		return E2EEPairingRequest{}, ErrConflict
	}

	if existing, ok := s.e2eePairingRequests[request.PairingID]; ok {
		if !sameE2EEPairingRequest(existing, request) {
			return E2EEPairingRequest{}, ErrConflict
		}
		if existing.SupersededAt != nil {
			return E2EEPairingRequest{}, domain.ErrE2EEPairingSuperseded
		}
		return cloneE2EEPairingRequest(existing), nil
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = s.now().UTC()
	}
	now := s.now().UTC()
	for id, previous := range s.e2eePairingRequests {
		if previous.AgentID == request.AgentID && previous.DeviceID == request.DeviceID &&
			previous.KeyEpoch == request.KeyEpoch && previous.SupersededAt == nil {
			previous.SupersededAt = &now
			s.e2eePairingRequests[id] = previous
		}
	}
	s.e2eePairingRequests[request.PairingID] = cloneE2EEPairingRequest(request)
	return cloneE2EEPairingRequest(request), nil
}

func (s *MemoryStore) GetE2EEPairingRequest(
	_ context.Context,
	ownerUserID string,
	agentID string,
	pairingID string,
) (E2EEPairingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.e2eePairingRequests[pairingID]
	if !ok || request.OwnerUserID != ownerUserID || request.AgentID != agentID {
		return E2EEPairingRequest{}, ErrNotFound
	}
	if request.SupersededAt != nil {
		return cloneE2EEPairingRequest(request), domain.ErrE2EEPairingSuperseded
	}
	return cloneE2EEPairingRequest(request), nil
}

func (s *MemoryStore) ListPendingE2EEPairingRequests(
	_ context.Context,
	ownerUserID string,
	agentID string,
	limit int,
) ([]E2EEPairingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	now := s.now().UTC()
	requests := make([]E2EEPairingRequest, 0, limit)
	for _, request := range s.e2eePairingRequests {
		if request.OwnerUserID == ownerUserID && request.AgentID == agentID &&
			request.CompletedAt == nil && request.CancelledAt == nil && request.RejectedAt == nil && request.SupersededAt == nil && request.ExpiresAt.After(now) {
			requests = append(requests, cloneE2EEPairingRequest(request))
		}
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].CreatedAt.Before(requests[j].CreatedAt)
	})
	if len(requests) > limit {
		requests = requests[:limit]
	}
	return requests, nil
}

func (s *MemoryStore) CompleteE2EEPairing(
	_ context.Context,
	request E2EEPairingRequest,
	keyPackage E2EEKeyPackage,
) (E2EEKeyPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.e2eePairingRequests[request.PairingID]
	if !ok || stored.OwnerUserID != request.OwnerUserID || stored.AgentID != request.AgentID {
		return E2EEKeyPackage{}, ErrNotFound
	}
	if stored.SupersededAt != nil {
		return E2EEKeyPackage{}, domain.ErrE2EEPairingSuperseded
	}
	if stored.CancelledAt != nil || stored.RejectedAt != nil {
		return E2EEKeyPackage{}, domain.ErrShortPairingEnded
	}
	if stored.ProtocolVersion == domain.ShortPairingProtocol {
		a, ok := s.shortPairingAttempts[request.ApprovalAttemptID]
		if !ok {
			return E2EEKeyPackage{}, ErrNotFound
		}
		if err := shortApproval(stored, a, request, s.now().UTC()); err != nil {
			return E2EEKeyPackage{}, err
		}
	}
	if !pairingMatchesPackage(stored, keyPackage) {
		return E2EEKeyPackage{}, ErrConflict
	}
	key := e2eeKeyPackageKey(keyPackage.AgentID, keyPackage.DeviceID, keyPackage.KeyEpoch)
	if stored.CompletedAt != nil {
		existing, exists := s.e2eeKeyPackages[key]
		if !exists || existing.PairingID != stored.PairingID {
			return E2EEKeyPackage{}, ErrConflict
		}
		return cloneE2EEKeyPackage(existing), nil
	}
	if !stored.ExpiresAt.After(s.now().UTC()) {
		return E2EEKeyPackage{}, domain.ErrE2EEPairingExpired
	}
	if keyPackage.CreatedAt.IsZero() {
		keyPackage.CreatedAt = s.now().UTC()
	}
	now := s.now().UTC()
	if stored.ProtocolVersion == domain.ShortPairingProtocol {
		a := s.shortPairingAttempts[request.ApprovalAttemptID]
		a.Stage = 4
		s.shortPairingAttempts[a.AttemptID] = a
	}
	stored.CompletedAt = &now
	s.e2eePairingRequests[stored.PairingID] = stored
	s.e2eeKeyPackages[key] = cloneE2EEKeyPackage(keyPackage)
	return cloneE2EEKeyPackage(keyPackage), nil
}

func (s *MemoryStore) GetE2EEKeyPackage(
	_ context.Context,
	ownerUserID string,
	agentID string,
	deviceID string,
	keyEpoch int64,
) (E2EEKeyPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keyPackage, ok := s.e2eeKeyPackages[e2eeKeyPackageKey(agentID, deviceID, keyEpoch)]
	if !ok || keyPackage.OwnerUserID != ownerUserID {
		return E2EEKeyPackage{}, ErrNotFound
	}
	return cloneE2EEKeyPackage(keyPackage), nil
}

func pairingMatchesPackage(request E2EEPairingRequest, keyPackage E2EEKeyPackage) bool {
	return request.PairingID == keyPackage.PairingID &&
		request.OwnerUserID == keyPackage.OwnerUserID && request.NodeID == keyPackage.NodeID &&
		request.AgentID == keyPackage.AgentID && request.DeviceID == keyPackage.DeviceID &&
		request.KeyEpoch == keyPackage.KeyEpoch &&
		bytes.Equal(request.RecipientPublicKey, keyPackage.RecipientPublicKey) &&
		len(keyPackage.SenderEphemeralPublicKey) > 0 && len(keyPackage.Nonce) == 12 &&
		len(keyPackage.Ciphertext) >= 16
}

func sameE2EEPairingRequest(left E2EEPairingRequest, right E2EEPairingRequest) bool {
	return left.PairingID == right.PairingID && left.OwnerUserID == right.OwnerUserID &&
		left.NodeID == right.NodeID && left.AgentID == right.AgentID &&
		left.DeviceID == right.DeviceID && left.DeviceName == right.DeviceName &&
		left.KeyEpoch == right.KeyEpoch && normalizedPairingProtocol(left.ProtocolVersion) == normalizedPairingProtocol(right.ProtocolVersion) && bytes.Equal(left.RecipientCapabilityHash, right.RecipientCapabilityHash) &&
		bytes.Equal(left.RecipientPublicKey, right.RecipientPublicKey) &&
		bytes.Equal(left.SecretCommitment, right.SecretCommitment)
}

func cloneE2EEPairingRequest(request E2EEPairingRequest) E2EEPairingRequest {
	request.RecipientPublicKey = append([]byte(nil), request.RecipientPublicKey...)
	request.SecretCommitment = append([]byte(nil), request.SecretCommitment...)
	request.RecipientCapabilityHash = bytes.Clone(request.RecipientCapabilityHash)
	return request
}

func cloneE2EEKeyPackage(keyPackage E2EEKeyPackage) E2EEKeyPackage {
	keyPackage.RecipientPublicKey = append([]byte(nil), keyPackage.RecipientPublicKey...)
	keyPackage.SenderEphemeralPublicKey = append(
		[]byte(nil),
		keyPackage.SenderEphemeralPublicKey...)
	keyPackage.Nonce = append([]byte(nil), keyPackage.Nonce...)
	keyPackage.Ciphertext = append([]byte(nil), keyPackage.Ciphertext...)
	return keyPackage
}

func e2eeKeyPackageKey(agentID string, deviceID string, keyEpoch int64) string {
	return agentID + "\x00" + deviceID + "\x00" + strconv.FormatInt(keyEpoch, 10)
}

// Creation and completion share a device-scoped transaction lock. Locking only
// a pairing row would allow a different pairing to supersede it during completion.
func lockE2EEPairing(ctx context.Context, tx *sql.Tx, request E2EEPairingRequest) error {
	key := fmt.Sprintf("e2ee-pairing/%q/%q/%d", request.AgentID, request.DeviceID, request.KeyEpoch)
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key)
	return err
}

func (s *PostgresStore) CreateE2EEPairingRequest(
	ctx context.Context,
	request E2EEPairingRequest,
) (E2EEPairingRequest, error) {
	if !validE2EEPairingRequest(request) {
		return E2EEPairingRequest{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return E2EEPairingRequest{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockE2EEPairing(ctx, tx, request); err != nil {
		return E2EEPairingRequest{}, err
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = s.now().UTC()
	}
	var createdAt time.Time
	err = tx.QueryRowContext(
		ctx,
		`
		INSERT INTO e2ee_pairing_requests (
			pairing_id, owner_user_id, node_id, agent_id, device_id, device_name,
			key_epoch, recipient_public_key, secret_commitment, created_at, expires_at,protocol_version,recipient_capability_hash
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (pairing_id) DO NOTHING
		RETURNING created_at`,
		request.PairingID,
		request.OwnerUserID,
		request.NodeID,
		request.AgentID,
		request.DeviceID,
		request.DeviceName,
		request.KeyEpoch,
		request.RecipientPublicKey,
		request.SecretCommitment,
		request.CreatedAt,
		request.ExpiresAt,
		normalizedPairingProtocol(request.ProtocolVersion),
		request.RecipientCapabilityHash,
	).Scan(&createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		// A creation retry must never reactivate a superseded request.
		request, err = existingE2EEPairing(ctx, tx, request)
	} else if err == nil {
		request.CreatedAt = createdAt.UTC()
		_, err = tx.ExecContext(ctx, `
			UPDATE e2ee_pairing_requests SET superseded_at = $1
			WHERE agent_id = $2 AND device_id = $3 AND key_epoch = $4
				AND pairing_id <> $5 AND superseded_at IS NULL`,
			s.now().UTC(), request.AgentID, request.DeviceID, request.KeyEpoch, request.PairingID)
	}
	if err != nil {
		return E2EEPairingRequest{}, err
	}
	if err = tx.Commit(); err != nil {
		return E2EEPairingRequest{}, err
	}
	return request, nil
}

func existingE2EEPairing(
	ctx context.Context, tx *sql.Tx, request E2EEPairingRequest,
) (E2EEPairingRequest, error) {
	existing, err := scanE2EEPairingRequest(tx.QueryRowContext(ctx,
		e2eePairingRequestSelectSQL+`
		WHERE owner_user_id = $1 AND agent_id = $2 AND pairing_id = $3`,
		request.OwnerUserID, request.AgentID, request.PairingID))
	if errors.Is(err, sql.ErrNoRows) {
		return E2EEPairingRequest{}, ErrNotFound
	}
	if err != nil {
		return E2EEPairingRequest{}, err
	}
	if !sameE2EEPairingRequest(existing, request) {
		return E2EEPairingRequest{}, ErrConflict
	}
	if existing.SupersededAt != nil {
		return E2EEPairingRequest{}, domain.ErrE2EEPairingSuperseded
	}
	return existing, nil
}

func (s *PostgresStore) GetE2EEPairingRequest(
	ctx context.Context,
	ownerUserID string,
	agentID string,
	pairingID string,
) (E2EEPairingRequest, error) {
	request, err := scanE2EEPairingRequest(s.db.QueryRowContext(ctx,
		e2eePairingRequestSelectSQL+`
		WHERE owner_user_id = $1 AND agent_id = $2 AND pairing_id = $3`,
		ownerUserID, agentID, pairingID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return E2EEPairingRequest{}, ErrNotFound
	}
	if err == nil && request.SupersededAt != nil {
		return request, domain.ErrE2EEPairingSuperseded
	}
	return request, err
}

func (s *PostgresStore) ListPendingE2EEPairingRequests(
	ctx context.Context,
	ownerUserID string,
	agentID string,
	limit int,
) ([]E2EEPairingRequest, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, e2eePairingRequestSelectSQL+`
		WHERE owner_user_id = $1 AND agent_id = $2
			AND completed_at IS NULL AND cancelled_at IS NULL AND rejected_at IS NULL AND superseded_at IS NULL AND expires_at > $3
		ORDER BY created_at
		LIMIT $4`, ownerUserID, agentID, s.now().UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	requests := make([]E2EEPairingRequest, 0)
	for rows.Next() {
		request, scanErr := scanE2EEPairingRequest(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (s *PostgresStore) CompleteE2EEPairing(
	ctx context.Context,
	request E2EEPairingRequest,
	keyPackage E2EEKeyPackage,
) (result E2EEKeyPackage, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return E2EEKeyPackage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockE2EEPairing(ctx, tx, request); err != nil {
		return E2EEKeyPackage{}, err
	}
	stored, err := scanE2EEPairingRequest(tx.QueryRowContext(ctx,
		e2eePairingRequestSelectSQL+`
		WHERE owner_user_id = $1 AND agent_id = $2 AND pairing_id = $3
		FOR UPDATE`, request.OwnerUserID, request.AgentID, request.PairingID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return E2EEKeyPackage{}, ErrNotFound
	}
	if err != nil {
		return E2EEKeyPackage{}, err
	}
	if stored.SupersededAt != nil {
		return E2EEKeyPackage{}, domain.ErrE2EEPairingSuperseded
	}
	if err = s.validateShortApproval(ctx, tx, stored, request); err != nil {
		return E2EEKeyPackage{}, err
	}
	if !pairingMatchesPackage(stored, keyPackage) {
		return E2EEKeyPackage{}, ErrConflict
	}
	if stored.CompletedAt != nil {
		result, err = scanE2EEKeyPackage(tx.QueryRowContext(ctx,
			e2eeKeyPackageSelectSQL+`
			WHERE owner_user_id = $1 AND agent_id = $2 AND device_id = $3 AND key_epoch = $4`,
			keyPackage.OwnerUserID, keyPackage.AgentID, keyPackage.DeviceID, keyPackage.KeyEpoch,
		))
		if err != nil {
			return E2EEKeyPackage{}, err
		}
		if result.PairingID != stored.PairingID {
			return E2EEKeyPackage{}, ErrConflict
		}

		if err = tx.Commit(); err != nil {
			return E2EEKeyPackage{}, err
		}
		return result, nil
	}
	if !stored.ExpiresAt.After(s.now().UTC()) {
		return E2EEKeyPackage{}, domain.ErrE2EEPairingExpired
	}
	if keyPackage.CreatedAt.IsZero() {
		keyPackage.CreatedAt = s.now().UTC()
	}
	var createdAt time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO e2ee_key_packages (
			pairing_id, owner_user_id, node_id, agent_id, device_id, key_epoch,
			recipient_public_key, sender_ephemeral_public_key, nonce, ciphertext, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (agent_id, device_id, key_epoch) DO UPDATE SET
			pairing_id = EXCLUDED.pairing_id,
			owner_user_id = EXCLUDED.owner_user_id,
			node_id = EXCLUDED.node_id,
			recipient_public_key = EXCLUDED.recipient_public_key,
			sender_ephemeral_public_key = EXCLUDED.sender_ephemeral_public_key,
			nonce = EXCLUDED.nonce,
			ciphertext = EXCLUDED.ciphertext,
			created_at = EXCLUDED.created_at
		RETURNING created_at`,
		keyPackage.PairingID, keyPackage.OwnerUserID, keyPackage.NodeID, keyPackage.AgentID,
		keyPackage.DeviceID, keyPackage.KeyEpoch, keyPackage.RecipientPublicKey,
		keyPackage.SenderEphemeralPublicKey, keyPackage.Nonce, keyPackage.Ciphertext,
		keyPackage.CreatedAt,
	).Scan(&createdAt)
	if err != nil {
		return E2EEKeyPackage{}, err
	}
	keyPackage.CreatedAt = createdAt.UTC()
	result = keyPackage
	_, err = tx.ExecContext(ctx, `
		UPDATE e2ee_pairing_requests
		SET completed_at = COALESCE(completed_at, $1)
		WHERE pairing_id = $2`, s.now().UTC(), stored.PairingID)
	if err != nil {
		return E2EEKeyPackage{}, err
	}
	if stored.ProtocolVersion == domain.ShortPairingProtocol {
		if _, err = tx.ExecContext(ctx, `UPDATE e2ee_pairing_attempts SET stage=4 WHERE attempt_id=$1`, request.ApprovalAttemptID); err != nil {
			return E2EEKeyPackage{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return E2EEKeyPackage{}, err
	}
	return result, nil
}

func (s *PostgresStore) GetE2EEKeyPackage(
	ctx context.Context,
	ownerUserID string,
	agentID string,
	deviceID string,
	keyEpoch int64,
) (E2EEKeyPackage, error) {
	keyPackage, err := scanE2EEKeyPackage(s.db.QueryRowContext(ctx,
		e2eeKeyPackageSelectSQL+`
		WHERE owner_user_id = $1 AND agent_id = $2 AND device_id = $3 AND key_epoch = $4`,
		ownerUserID, agentID, deviceID, keyEpoch,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return E2EEKeyPackage{}, ErrNotFound
	}
	return keyPackage, err
}

func validE2EEPairingRequest(request E2EEPairingRequest) bool {
	return request.PairingID != "" && request.OwnerUserID != "" && request.NodeID != "" &&
		request.AgentID != "" && request.DeviceID != "" && request.KeyEpoch > 0 &&
		len(request.RecipientPublicKey) > 0 && len(request.SecretCommitment) > 0 &&
		!request.ExpiresAt.IsZero() && validPairingProtocol(request)
}
func validPairingProtocol(request E2EEPairingRequest) bool {
	switch normalizedPairingProtocol(request.ProtocolVersion) {
	case "legacy-v1":
		return len(request.RecipientCapabilityHash) == 0
	case domain.ShortPairingProtocol:
		return len(request.RecipientCapabilityHash) == 32
	default:
		return false
	}
}

func scanE2EEPairingRequest(row rowScanner) (E2EEPairingRequest, error) {
	var request E2EEPairingRequest
	err := row.Scan(
		&request.PairingID,
		&request.OwnerUserID,
		&request.NodeID,
		&request.AgentID,
		&request.DeviceID,
		&request.DeviceName,
		&request.KeyEpoch,
		&request.RecipientPublicKey,
		&request.SecretCommitment,
		&request.CreatedAt,
		&request.ExpiresAt,
		&request.CompletedAt,
		&request.SupersededAt,
		&request.ProtocolVersion,
		&request.RecipientCapabilityHash,
		&request.CancelledAt,
		&request.RejectedAt,
	)
	return request, err
}

func scanE2EEKeyPackage(row rowScanner) (E2EEKeyPackage, error) {
	var keyPackage E2EEKeyPackage
	err := row.Scan(
		&keyPackage.PairingID, &keyPackage.OwnerUserID, &keyPackage.NodeID,
		&keyPackage.AgentID, &keyPackage.DeviceID, &keyPackage.KeyEpoch,
		&keyPackage.RecipientPublicKey, &keyPackage.SenderEphemeralPublicKey,
		&keyPackage.Nonce, &keyPackage.Ciphertext, &keyPackage.CreatedAt,
	)
	return keyPackage, err
}

func normalizedPairingProtocol(value string) string {
	if value == "" {
		return "legacy-v1"
	}
	return value
}

func (s *PostgresStore) validateShortApproval(
	ctx context.Context,
	tx *sql.Tx,
	stored, request E2EEPairingRequest,
) error {
	if stored.CancelledAt != nil || stored.RejectedAt != nil {
		return domain.ErrShortPairingEnded
	}
	if stored.ProtocolVersion != domain.ShortPairingProtocol {
		return nil
	}
	attempt, err := scanShortAttempt(
		tx.QueryRowContext(
			ctx,
			shortAttemptSelect+` WHERE attempt_id=$1 AND pairing_id=$2 FOR UPDATE`,
			request.ApprovalAttemptID,
			stored.PairingID,
		),
	)
	if err != nil {
		return err
	}
	return shortApproval(stored, attempt, request, s.now().UTC())
}
